//go:build integration

package preview

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/pkg/authctx"
	"foldex/internal/testdb"
)

// TestMain owns the lifetime of this package's shared Postgres container.
//
// It cannot be a t.Cleanup: os.Exit skips deferred work, and a cleanup hung off
// whichever test ran first would tear the database down while the rest of the
// package still needed it. The Makefile disables testcontainers' reaper, so
// nothing else would collect it.
func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

func TestWorker_ProcessesEnqueuedJob(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	// Fake target page
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head>
            <title>Hello Foldex</title>
            <meta property="og:image" content="`+fmt.Sprintf("%s/cover.png", "http://example")+`">
            <link rel="icon" href="/fav.ico">
        </head></html>`)
	}))
	defer target.Close()

	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(pool, 1, 3*time.Second, logger)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() {
		cancel()
		w.Stop()
	}()

	lrepo := links.NewRepository(pool)
	link, err := lrepo.Create(context.Background(), uid, links.CreateInput{
		URL: target.URL, Title: "before",
	})
	require.NoError(t, err)

	_ = w.Enqueue(link.ID)

	// Poll for status=ok
	deadline := time.Now().Add(8 * time.Second)
	var got links.Link
	for time.Now().Before(deadline) {
		got, _ = lrepo.Get(context.Background(), uid, link.ID)
		if got.PreviewStatus == "ok" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Equal(t, "ok", got.PreviewStatus, "worker should mark preview as ok within 8s")
	require.NotNil(t, got.FaviconURL)
	assert.Contains(t, *got.FaviconURL, "fav.ico")
}

type stubScreenshotter struct {
	payload      []byte
	err          error
	calls        *atomic.Int64
	remaining    chan<- time.Duration
	beforeReturn func()
}

func (s stubScreenshotter) Capture(ctx context.Context, _ string) ([]byte, error) {
	if s.calls != nil {
		s.calls.Add(1)
	}
	if s.remaining != nil {
		deadline, ok := ctx.Deadline()
		remaining := time.Duration(0)
		if ok {
			remaining = time.Until(deadline)
		}
		select {
		case s.remaining <- remaining:
		default:
		}
	}
	if s.beforeReturn != nil {
		s.beforeReturn()
	}
	return s.payload, s.err
}

type memUploader struct {
	mu        sync.Mutex
	objs      map[string][]byte
	uploadErr error
	deleted   []string
}

func (m *memUploader) Upload(_ context.Context, key string, data []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uploadErr != nil {
		return m.uploadErr
	}
	if m.objs == nil {
		m.objs = map[string][]byte{}
	}
	m.objs[key] = data
	return nil
}

func (m *memUploader) DeleteObject(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, key)
	if m.objs == nil {
		return nil
	}
	delete(m.objs, key)
	return nil
}

func TestWorker_ScreenshotFallback_ManualUploadWinsAfterCapture(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>plain</title></head></html>`)
	}))
	defer target.Close()

	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "manual-race@test.local", "admin")
	repo := links.NewRepository(pool)
	link, err := repo.Create(context.Background(), uid, links.CreateInput{URL: target.URL, Title: "manual race"})
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := NewWorker(pool, 1, 3*time.Second, logger)
	worker.screenshotURLPolicy = func(context.Context, string) bool { return true }
	uploader := &memUploader{}
	manualURL := fmt.Sprintf("/api/files/images/%d.jpg", link.ID)
	worker.WithScreenshotFallback(stubScreenshotter{
		payload: testPNG(t),
		beforeReturn: func() {
			require.NoError(t, repo.UpdateOGImage(context.Background(), uid, link.ID, manualURL))
		},
	}, uploader)

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	defer func() {
		cancel()
		worker.Stop()
	}()
	require.NoError(t, worker.Enqueue(link.ID))

	got := awaitPreviewStatus(t, repo, uid, link.ID, links.StatusOK, 6*time.Second)
	require.NotNil(t, got.OGImageURL)
	assert.Equal(t, manualURL, *got.OGImageURL)
	require.Eventually(t, func() bool {
		uploader.mu.Lock()
		defer uploader.mu.Unlock()
		return len(uploader.objs) == 0 && len(uploader.deleted) == 1
	}, time.Second, 10*time.Millisecond, "superseded fallback object must be removed")
	uploader.mu.Lock()
	assert.Regexp(t, fmt.Sprintf(`^screenshots/%d\.[0-9a-f-]{36}\.jpg$`, link.ID), uploader.deleted[0])
	uploader.mu.Unlock()
}

type refreshDuringCaptureScreenshotter struct {
	t             *testing.T
	worker        *Worker
	repo          *links.Repository
	pool          *pgxpool.Pool
	id            int64
	payload       []byte
	calls         atomic.Int64
	secondStarted chan struct{}
	releaseSecond chan struct{}
}

func (s *refreshDuringCaptureScreenshotter) Capture(context.Context, string) ([]byte, error) {
	if s.calls.Add(1) == 1 {
		claimed, err := s.repo.SystemGetPreview(context.Background(), s.id)
		require.NoError(s.t, err)
		require.NoError(s.t, s.repo.SystemUpdatePreview(context.Background(), s.id, links.StatusPending, nil, nil, nil, nil))
		// Reproduce equal timestamp tokens deterministically. A refresh is a new
		// generation even when the row clock cannot distinguish both claims.
		_, err = s.pool.Exec(context.Background(), `UPDATE link SET updated_at = $1 WHERE id = $2`, claimed.UpdatedAt, s.id)
		require.NoError(s.t, err)
		require.NoError(s.t, s.worker.Enqueue(s.id))
		return s.payload, nil
	}
	release := s.releaseSecond
	close(s.secondStarted)
	<-release
	return s.payload, nil
}

func TestWorker_ScreenshotFallback_NewerRefreshWinsAndReruns(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>plain</title></head></html>`)
	}))
	defer target.Close()

	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "refresh-race@test.local", "admin")
	repo := links.NewRepository(pool)
	link, err := repo.Create(context.Background(), uid, links.CreateInput{URL: target.URL, Title: "refresh race"})
	require.NoError(t, err)

	worker := NewWorker(pool, 1, 3*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	worker.screenshotURLPolicy = func(context.Context, string) bool { return true }
	uploader := &memUploader{}
	shot := &refreshDuringCaptureScreenshotter{
		t: t, worker: worker, repo: repo, pool: pool, id: link.ID, payload: testPNG(t),
		secondStarted: make(chan struct{}), releaseSecond: make(chan struct{}),
	}
	worker.WithScreenshotFallback(shot, uploader)
	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	defer func() {
		cancel()
		worker.Stop()
	}()
	require.NoError(t, worker.Enqueue(link.ID))

	select {
	case <-shot.secondStarted:
	case <-time.After(6 * time.Second):
		t.Fatal("newer refresh did not receive its rerun")
	}
	got, err := repo.Get(context.Background(), uid, link.ID)
	require.NoError(t, err)
	assert.Equal(t, string(links.StatusPending), got.PreviewStatus)
	uploader.mu.Lock()
	assert.Empty(t, uploader.objs, "stale fallback must delete only its operation-owned object")
	require.Len(t, uploader.deleted, 1)
	uploader.mu.Unlock()

	close(shot.releaseSecond)
	got = awaitPreviewStatus(t, repo, uid, link.ID, links.StatusOK, 6*time.Second)
	require.NotNil(t, got.OGImageURL)
	assert.Regexp(t, fmt.Sprintf(`^/api/files/screenshots/%d\.[0-9a-f-]{36}\.jpg$`, link.ID), *got.OGImageURL)
}

// A loopback page may be fetched in permissive preview mode, but screenshot
// egress remains strict and must skip it.
func TestWorker_ScreenshotFallback_SkipsLoopbackTarget(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	// Page with NO og:image and no description.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>plain</title></head></html>`)
	}))
	defer target.Close()

	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(pool, 1, 3*time.Second, logger)

	up := &memUploader{}
	w.WithScreenshotFallback(stubScreenshotter{payload: []byte("PNG-DATA")}, up)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() {
		cancel()
		w.Stop()
	}()

	lrepo := links.NewRepository(pool)
	link, err := lrepo.Create(context.Background(), uid, links.CreateInput{
		URL: target.URL, Title: "plain",
	})
	require.NoError(t, err)

	_ = w.Enqueue(link.ID)

	// httptest binds to 127.0.0.1 — IsPublicURL rejects it, so the fallback
	// should NOT run for this URL.
	deadline := time.Now().Add(6 * time.Second)
	var got links.Link
	for time.Now().Before(deadline) {
		got, _ = lrepo.Get(context.Background(), uid, link.ID)
		if got.PreviewStatus == "ok" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Equal(t, "ok", got.PreviewStatus)
	assert.Nil(t, got.OGImageURL, "loopback host must skip the screenshot fallback")
	assert.Empty(t, up.objs, "no upload should happen for a non-public host")
}

func TestWorker_ScreenshotFallback_SuccessAndFailureConverge(t *testing.T) {
	for _, tc := range []struct {
		name        string
		captureErr  error
		uploadErr   error
		wantImage   bool
		wantUpload  bool
		dbUpdateErr bool
	}{
		{name: "success", wantImage: true, wantUpload: true},
		{name: "capture failure", captureErr: fmt.Errorf("capture failed")},
		{name: "upload failure", uploadErr: fmt.Errorf("upload failed")},
		{name: "database update failure", dbUpdateErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PREVIEW_STRICT_SSRF", "")
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, `<html><head><title>plain</title></head></html>`)
			}))
			defer target.Close()

			pool := testdb.Shared(t)
			if tc.dbUpdateErr {
				_, err := pool.Exec(context.Background(), `
					CREATE FUNCTION reject_preview_screenshot_update() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN
						IF NEW.og_image_url IS DISTINCT FROM OLD.og_image_url
						   AND NEW.og_image_url LIKE '/api/files/screenshots/%' THEN
							RAISE EXCEPTION 'injected screenshot update failure';
						END IF;
						RETURN NEW;
					END $$;
					CREATE TRIGGER reject_preview_screenshot_update
					BEFORE UPDATE ON link FOR EACH ROW EXECUTE FUNCTION reject_preview_screenshot_update()`)
				require.NoError(t, err)
				t.Cleanup(func() {
					_, _ = pool.Exec(context.Background(), `
						DROP TRIGGER IF EXISTS reject_preview_screenshot_update ON link;
						DROP FUNCTION IF EXISTS reject_preview_screenshot_update()`)
				})
			}
			uid := testdb.SeedUser(t, pool, tc.name+"@test.local", "admin")
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			worker := NewWorker(pool, 1, 3*time.Second, logger)
			worker.screenshotURLPolicy = func(context.Context, string) bool { return true }
			uploader := &memUploader{uploadErr: tc.uploadErr}
			var captureCalls atomic.Int64
			captureBudget := make(chan time.Duration, 1)
			worker.WithScreenshotFallback(stubScreenshotter{
				payload: testPNG(t), err: tc.captureErr,
				calls: &captureCalls, remaining: captureBudget,
			}, uploader)

			ctx, cancel := context.WithCancel(context.Background())
			worker.Start(ctx)
			defer func() {
				cancel()
				worker.Stop()
			}()

			repo := links.NewRepository(pool)
			link, err := repo.Create(context.Background(), uid, links.CreateInput{URL: target.URL, Title: tc.name})
			require.NoError(t, err)
			require.NoError(t, worker.Enqueue(link.ID))
			got := awaitPreviewStatus(t, repo, uid, link.ID, links.StatusOK, 6*time.Second)
			assert.GreaterOrEqual(t, captureCalls.Load(), int64(1), "fallback must attempt capture")
			remaining := <-captureBudget
			assert.Greater(t, remaining, screenshotCaptureTimeout-time.Second)
			assert.LessOrEqual(t, remaining, screenshotCaptureTimeout)

			if tc.wantImage {
				require.NotNil(t, got.OGImageURL)
				assert.Regexp(t, fmt.Sprintf(`^/api/files/screenshots/%d\.[0-9a-f-]{36}\.jpg$`, link.ID), *got.OGImageURL)
			} else {
				assert.Nil(t, got.OGImageURL)
			}
			if tc.wantUpload {
				uploader.mu.Lock()
				defer uploader.mu.Unlock()
				require.Len(t, uploader.objs, 1)
				for key := range uploader.objs {
					assert.Regexp(t, fmt.Sprintf(`^screenshots/%d\.[0-9a-f-]{36}\.jpg$`, link.ID), key)
				}
			} else {
				uploader.mu.Lock()
				defer uploader.mu.Unlock()
				assert.Empty(t, uploader.objs)
			}
		})
	}
}

// When a link already has og_image_url (user uploaded an image), the worker
// must short-circuit: NO HTML fetch, NO screenshot, and the "capturando…"
// label disappears by flipping preview_status from pending to ok.
func TestWorker_ShortCircuitsWhenImageAlreadyPresent(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>X</title></head></html>`)
	}))
	defer target.Close()

	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(pool, 1, 3*time.Second, logger)

	up := &memUploader{}
	w.WithScreenshotFallback(stubScreenshotter{payload: []byte("nope")}, up)

	lrepo := links.NewRepository(pool)
	link, err := lrepo.Create(context.Background(), uid, links.CreateInput{
		URL: target.URL, Title: "preuploaded",
	})
	require.NoError(t, err)

	// Simulate a user upload landing BEFORE the worker starts / picks the job
	// (avoids racing Start's requeuePending ticker against the OG write).
	require.NoError(t, lrepo.UpdateOGImage(context.Background(), uid, link.ID, "/api/files/images/1.png"))

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() {
		cancel()
		w.Stop()
	}()

	_ = w.Enqueue(link.ID)

	deadline := time.Now().Add(4 * time.Second)
	var got links.Link
	for time.Now().Before(deadline) {
		got, _ = lrepo.Get(context.Background(), uid, link.ID)
		if got.PreviewStatus == "ok" {
			break
		}
		time.Sleep(80 * time.Millisecond)
	}
	require.Equal(t, "ok", got.PreviewStatus, "short-circuit must flip status to ok")
	require.NotNil(t, got.OGImageURL)
	assert.Equal(t, "/api/files/images/1.png", *got.OGImageURL, "user upload must be preserved")
	assert.Equal(t, 0, hits, "no HTTP fetch should have run")
	assert.Empty(t, up.objs, "no screenshot upload should have happened")
}

func TestWorker_MarksFailureOnUnreachable(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(pool, 1, 500*time.Millisecond, logger)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() {
		cancel()
		w.Stop()
	}()

	lrepo := links.NewRepository(pool)
	// Use a port nothing listens on. Permissive preview mode allows TEST-NET-1,
	// which is documented as non-routable and therefore won't connect.
	link, _ := lrepo.Create(context.Background(), uid, links.CreateInput{
		URL: "http://192.0.2.1:1", Title: "doomed",
	})

	_ = w.Enqueue(link.ID)

	deadline := time.Now().Add(10 * time.Second)
	var got links.Link
	for time.Now().Before(deadline) {
		got, _ = lrepo.Get(context.Background(), uid, link.ID)
		if got.PreviewStatus == "failed" {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	assert.Equal(t, "failed", got.PreviewStatus)
	require.NotNil(t, got.PreviewError)
	assert.Equal(t, "fetch_failed", *got.PreviewError)
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.White)
	var out bytes.Buffer
	require.NoError(t, png.Encode(&out, img))
	return out.Bytes()
}

func awaitPreviewStatus(t *testing.T, repo *links.Repository, uid authctx.UserID, id int64, status links.PreviewStatus, timeout time.Duration) links.Link {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got, err := repo.Get(context.Background(), uid, id)
		require.NoError(t, err)
		if got.PreviewStatus == string(status) {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("preview status did not become %q within %s", status, timeout)
	return links.Link{}
}
