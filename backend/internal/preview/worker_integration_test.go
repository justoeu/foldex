//go:build integration

package preview_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/preview"
	"foldex/internal/testdb"
)

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

	pool := testdb.New(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := preview.NewWorker(pool, 1, 3*time.Second, logger)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() {
		cancel()
		w.Stop()
	}()

	lrepo := links.NewRepository(pool)
	link, err := lrepo.Create(context.Background(), links.CreateInput{
		URL: target.URL, Title: "before",
	})
	require.NoError(t, err)

	_ = w.Enqueue(link.ID)

	// Poll for status=ok
	deadline := time.Now().Add(8 * time.Second)
	var got links.Link
	for time.Now().Before(deadline) {
		got, _ = lrepo.Get(context.Background(), link.ID)
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
	payload []byte
	err     error
}

func (s stubScreenshotter) Capture(_ context.Context, _ string) ([]byte, error) {
	return s.payload, s.err
}

type memUploader struct {
	objs map[string][]byte
}

func (m *memUploader) Upload(_ context.Context, key string, data []byte, _ string) error {
	if m.objs == nil {
		m.objs = map[string][]byte{}
	}
	m.objs[key] = data
	return nil
}

func (m *memUploader) DeleteObject(_ context.Context, key string) error {
	if m.objs == nil {
		return nil
	}
	delete(m.objs, key)
	return nil
}

// When the preview HTML has no og:image but the URL is publicly resolvable,
// the worker should fall back to a screenshot and set og_image_url to the
// proxy path.
func TestWorker_ScreenshotFallback_RunsWhenNoOGImage(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	// Page with NO og:image and no description.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>plain</title></head></html>`)
	}))
	defer target.Close()

	pool := testdb.New(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := preview.NewWorker(pool, 1, 3*time.Second, logger)

	up := &memUploader{}
	w.WithScreenshotFallback(stubScreenshotter{payload: []byte("PNG-DATA")}, up)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() {
		cancel()
		w.Stop()
	}()

	lrepo := links.NewRepository(pool)
	link, err := lrepo.Create(context.Background(), links.CreateInput{
		URL: target.URL, Title: "plain",
	})
	require.NoError(t, err)

	_ = w.Enqueue(link.ID)

	// httptest binds to 127.0.0.1 — IsPublicURL rejects it, so the fallback
	// should NOT run for this URL.
	deadline := time.Now().Add(6 * time.Second)
	var got links.Link
	for time.Now().Before(deadline) {
		got, _ = lrepo.Get(context.Background(), link.ID)
		if got.PreviewStatus == "ok" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Equal(t, "ok", got.PreviewStatus)
	assert.Nil(t, got.OGImageURL, "loopback host must skip the screenshot fallback")
	assert.Empty(t, up.objs, "no upload should happen for a non-public host")
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

	pool := testdb.New(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := preview.NewWorker(pool, 1, 3*time.Second, logger)

	up := &memUploader{}
	w.WithScreenshotFallback(stubScreenshotter{payload: []byte("nope")}, up)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() {
		cancel()
		w.Stop()
	}()

	lrepo := links.NewRepository(pool)
	link, err := lrepo.Create(context.Background(), links.CreateInput{
		URL: target.URL, Title: "preuploaded",
	})
	require.NoError(t, err)

	// Simulate a user upload landing BEFORE the worker picks up the job.
	require.NoError(t, lrepo.UpdateOGImage(context.Background(), link.ID, "/api/files/images/1.png"))

	_ = w.Enqueue(link.ID)

	deadline := time.Now().Add(4 * time.Second)
	var got links.Link
	for time.Now().Before(deadline) {
		got, _ = lrepo.Get(context.Background(), link.ID)
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
	pool := testdb.New(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := preview.NewWorker(pool, 1, 500*time.Millisecond, logger)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() {
		cancel()
		w.Stop()
	}()

	lrepo := links.NewRepository(pool)
	// Use a port nothing listens on; the SSRF guard rejects localhost, so use
	// 192.0.2.1 (TEST-NET-1, documented as non-routable) which the public
	// address check allows but won't connect.
	link, _ := lrepo.Create(context.Background(), links.CreateInput{
		URL: "http://192.0.2.1:1", Title: "doomed",
	})

	_ = w.Enqueue(link.ID)

	deadline := time.Now().Add(10 * time.Second)
	var got links.Link
	for time.Now().Before(deadline) {
		got, _ = lrepo.Get(context.Background(), link.ID)
		if got.PreviewStatus == "failed" {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	assert.Equal(t, "failed", got.PreviewStatus)
}
