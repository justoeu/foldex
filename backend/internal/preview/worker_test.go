package preview

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/imageopt"
	"foldex/internal/links"
)

type fakeScreenshotter struct {
	calls   int
	lastURL string
	payload []byte
	err     error
}

func (f *fakeScreenshotter) Capture(_ context.Context, pageURL string) ([]byte, error) {
	f.calls++
	f.lastURL = pageURL
	return f.payload, f.err
}

type fakeUploader struct {
	calls int
	last  struct {
		key, ct string
		data    []byte
	}
	deleted             []string
	deleteContextErrors []error
	err                 error
}

type fakePreviewRepo struct {
	work          links.PreviewWork
	pending       []links.PreviewWork
	nextUpdatedAt time.Time
	getCalls      int
	updates       []fakePreviewCAS
	publications  []fakePreviewPublication
	finishes      []fakePreviewCAS
}

type fakePreviewCAS struct {
	id         int64
	updatedAt  time.Time
	generation int64
	status     links.PreviewStatus
}

type fakePreviewPublication struct {
	fakePreviewCAS
	imageURL string
}

func (f *fakePreviewRepo) SystemGetPreview(context.Context, int64) (links.PreviewWork, error) {
	f.getCalls++
	return f.work, nil
}

func (f *fakePreviewRepo) SystemUpdatePreviewIfUnchanged(_ context.Context, id int64, updatedAt time.Time, generation int64, status links.PreviewStatus, _ *string, imageURL *string, _ *string, _ *string) (bool, error) {
	f.updates = append(f.updates, fakePreviewCAS{id: id, updatedAt: updatedAt, generation: generation, status: status})
	if !f.matchesPendingCAS(id, updatedAt, generation) {
		return false, nil
	}
	f.work.PreviewStatus = status
	if imageURL != nil && (f.work.OGImageURL == nil || *f.work.OGImageURL == "") {
		value := *imageURL
		f.work.OGImageURL = &value
	}
	if !f.nextUpdatedAt.IsZero() {
		f.work.UpdatedAt = f.nextUpdatedAt
	}
	return true, nil
}

func (f *fakePreviewRepo) SystemUpdateOGImage(_ context.Context, id int64, imageURL string, updatedAt time.Time, generation int64) (bool, error) {
	f.publications = append(f.publications, fakePreviewPublication{
		fakePreviewCAS: fakePreviewCAS{id: id, updatedAt: updatedAt, generation: generation},
		imageURL:       imageURL,
	})
	if !f.matchesPendingCAS(id, updatedAt, generation) || (f.work.OGImageURL != nil && *f.work.OGImageURL != "") {
		return false, nil
	}
	f.work.OGImageURL = &imageURL
	f.work.PreviewStatus = links.StatusOK
	return true, nil
}

func (f *fakePreviewRepo) SystemFinishScreenshotFallback(_ context.Context, id int64, updatedAt time.Time, generation int64) (bool, error) {
	f.finishes = append(f.finishes, fakePreviewCAS{id: id, updatedAt: updatedAt, generation: generation, status: links.StatusOK})
	if !f.matchesPendingCAS(id, updatedAt, generation) || (f.work.OGImageURL != nil && *f.work.OGImageURL != "") {
		return false, nil
	}
	f.work.PreviewStatus = links.StatusOK
	return true, nil
}

func (f *fakePreviewRepo) SystemPendingPreviews(context.Context, int) ([]links.PreviewWork, error) {
	return f.pending, nil
}

func (f *fakePreviewRepo) matchesPendingCAS(id int64, updatedAt time.Time, generation int64) bool {
	return f.work.ID == id && f.work.UpdatedAt.Equal(updatedAt) &&
		f.work.Generation == generation && f.work.PreviewStatus == links.StatusPending
}

func (f *fakeUploader) Upload(_ context.Context, key string, data []byte, ct string) error {
	f.calls++
	f.last.key, f.last.data, f.last.ct = key, data, ct
	return f.err
}

func (f *fakeUploader) DeleteObject(ctx context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	f.deleteContextErrors = append(f.deleteContextErrors, ctx.Err())
	return nil
}

// These unit tests exercise the worker's branching that does not require a
// real database — channel-full Enqueue path and concurrency clamping.

func TestNewWorker_ClampsZeroConcurrency(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 0, time.Second, logger)
	assert.Equal(t, 1, w.concurrent, "concurrency below 1 must be clamped")
}

func TestNewWorker_ClampsHighConcurrency(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 100_000, time.Second, logger)
	assert.Equal(t, 8, w.concurrent)
}

func TestNewWorker_QueueAllowsAtMostOneWaitingWave(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, requested := range []int{0, 1, 3, 100_000} {
		w := NewWorker(nil, requested, time.Second, logger)
		assert.Equal(t, w.concurrent, cap(w.jobs), "requested concurrency %d", requested)
	}
}

func TestWithScreenshotFallback_NilArgsIsNoop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)

	w.WithScreenshotFallback(nil, &fakeUploader{})
	assert.Nil(t, w.screenshotter, "nil screenshotter must keep fallback disabled")
	assert.Nil(t, w.uploader)

	w.WithScreenshotFallback(&fakeScreenshotter{}, nil)
	assert.Nil(t, w.screenshotter)
	assert.Nil(t, w.uploader, "nil uploader must keep fallback disabled")

	sc := &fakeScreenshotter{}
	up := &fakeUploader{}
	w.WithScreenshotFallback(sc, up)
	assert.NotNil(t, w.screenshotter, "non-nil pair must enable fallback")
	assert.NotNil(t, w.uploader)
}

func TestMaybeScreenshot_DoesNothingWhenFallbackDisabled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)
	// repo is nil; if we entered the screenshot path it would panic on Get.
	assert.Nil(t, w.maybeScreenshot(context.Background(), 1, "http://example.com", 1))
}

func TestWorker_ScreenshotFallbackPersistsOptimizedPublicCapture(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>plain</title></head></html>`)
	}))
	t.Cleanup(target.Close)

	const linkID int64 = 42
	claimAt := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	captureAt := claimAt.Add(time.Second)
	repo := &fakePreviewRepo{
		work: links.PreviewWork{
			ID: linkID, URL: target.URL, PreviewStatus: links.StatusPending,
			UpdatedAt: claimAt, Generation: 7,
		},
		nextUpdatedAt: captureAt,
	}
	source := workerTestPNG(t)
	expected, err := imageopt.Optimize(source, imageopt.Options{MaxDim: screenshotMaxDim, Quality: screenshotQuality})
	require.NoError(t, err)
	require.True(t, expected.Reencoded, "PNG fixture must take the optimization path")
	require.Equal(t, "image/jpeg", expected.ContentType)
	shot := &fakeScreenshotter{payload: source}
	uploader := &fakeUploader{}
	w := NewWorker(nil, 1, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.repo = repo
	w.screenshotURLPolicy = func(context.Context, string) bool { return true }
	w.WithScreenshotFallback(shot, uploader)

	w.process(context.Background(), previewJob{id: linkID})

	require.Equal(t, 1, shot.calls)
	assert.Equal(t, target.URL, shot.lastURL)
	require.Equal(t, 1, uploader.calls)
	assert.Equal(t, expected.Data, uploader.last.data)
	assert.Equal(t, expected.ContentType, uploader.last.ct)
	assert.Regexp(t, `^screenshots/42\.[0-9a-f-]{36}\.jpg$`, uploader.last.key)
	assert.Equal(t, []string{
		"screenshots/42.png",
		"screenshots/42.jpg",
		"screenshots/42.gif",
		"screenshots/42.webp",
	}, uploader.deleted)
	assert.NotContains(t, uploader.deleted, uploader.last.key)

	require.Len(t, repo.updates, 1)
	assert.Equal(t, fakePreviewCAS{id: linkID, updatedAt: claimAt, generation: 7, status: links.StatusPending}, repo.updates[0])
	require.Len(t, repo.publications, 1)
	publication := repo.publications[0]
	assert.Equal(t, linkID, publication.id)
	assert.Equal(t, captureAt, publication.updatedAt)
	assert.Equal(t, int64(7), publication.generation)
	assert.Equal(t, "/api/files/"+uploader.last.key, publication.imageURL)
	assert.Empty(t, repo.finishes, "successful publication must converge in the image CAS")
	require.NotNil(t, repo.work.OGImageURL)
	assert.Equal(t, publication.imageURL, *repo.work.OGImageURL)
	assert.Equal(t, links.StatusOK, repo.work.PreviewStatus)
}

func TestWorker_ScreenshotFallbackRejectsDecodeBombWithoutPublication(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>plain</title></head></html>`)
	}))
	t.Cleanup(target.Close)

	const linkID int64 = 43
	claimAt := time.Date(2026, time.August, 16, 13, 0, 0, 0, time.UTC)
	captureAt := claimAt.Add(time.Second)
	repo := &fakePreviewRepo{
		work: links.PreviewWork{
			ID: linkID, URL: target.URL, PreviewStatus: links.StatusPending,
			UpdatedAt: claimAt, Generation: 8,
		},
		nextUpdatedAt: captureAt,
	}
	bomb := workerDecodeBombPNG(t)
	_, err := imageopt.Optimize(bomb, imageopt.Options{MaxDim: screenshotMaxDim, Quality: screenshotQuality})
	require.ErrorIs(t, err, imageopt.ErrTooLarge)
	shot := &fakeScreenshotter{payload: bomb}
	uploader := &fakeUploader{}
	w := NewWorker(nil, 1, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.repo = repo
	w.screenshotURLPolicy = func(context.Context, string) bool { return true }
	w.WithScreenshotFallback(shot, uploader)

	w.process(context.Background(), previewJob{id: linkID})

	require.Equal(t, 1, shot.calls)
	assert.Equal(t, target.URL, shot.lastURL)
	assert.Zero(t, uploader.calls, "decode bombs must not fall back to raw PNG upload")
	assert.Empty(t, uploader.last.data)
	assert.Empty(t, uploader.last.key)
	assert.Empty(t, uploader.deleted)
	assert.Empty(t, repo.publications)
	require.Len(t, repo.finishes, 1)
	assert.Equal(t, fakePreviewCAS{id: linkID, updatedAt: captureAt, generation: 8, status: links.StatusOK}, repo.finishes[0])
	assert.Nil(t, repo.work.OGImageURL)
	assert.Equal(t, links.StatusOK, repo.work.PreviewStatus)
}

func TestProcess_RecoveryProjectionAvoidsPerItemHydration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	imageURL := "/api/files/images/42.jpg"
	work := links.PreviewWork{
		ID: 42, URL: "https://example.com", OGImageURL: &imageURL,
		PreviewStatus: links.StatusPending, UpdatedAt: time.Now(), Generation: 3,
	}
	repo := &fakePreviewRepo{work: work}
	w := NewWorker(nil, 1, time.Second, logger)
	w.repo = repo

	w.process(context.Background(), previewJob{id: work.ID, work: work, claimed: true})

	assert.Zero(t, repo.getCalls, "recovery claims must not issue one hydration query per queued link")
}

type refillPreviewRepo struct {
	mu            sync.Mutex
	work          map[int64]links.PreviewWork
	waveSize      int
	queries       int
	getCalls      int
	completed     int
	firstRelease  <-chan struct{}
	secondRelease <-chan struct{}
	thirdRelease  <-chan struct{}
	started       chan int64
	queryDone     chan int
	queryLimits   []int
	done          chan struct{}
}

func newRefillPreviewRepo(total, waveSize int, firstRelease, secondRelease, thirdRelease <-chan struct{}) *refillPreviewRepo {
	work := make(map[int64]links.PreviewWork, total)
	updatedAt := time.Date(2026, time.August, 16, 14, 0, 0, 0, time.UTC)
	for id := 1; id <= total; id++ {
		imageURL := "https://example.com/image.jpg"
		work[int64(id)] = links.PreviewWork{
			ID: int64(id), URL: "https://example.com", OGImageURL: &imageURL,
			PreviewStatus: links.StatusPending, UpdatedAt: updatedAt, Generation: 1,
		}
	}
	return &refillPreviewRepo{
		work: work, waveSize: waveSize,
		firstRelease: firstRelease, secondRelease: secondRelease, thirdRelease: thirdRelease,
		started: make(chan int64, total), queryDone: make(chan int, total), done: make(chan struct{}),
	}
}

func (r *refillPreviewRepo) SystemGetPreview(context.Context, int64) (links.PreviewWork, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getCalls++
	return links.PreviewWork{}, nil
}

func (r *refillPreviewRepo) SystemUpdatePreviewIfUnchanged(_ context.Context, id int64, updatedAt time.Time, generation int64, status links.PreviewStatus, _ *string, _ *string, _ *string, _ *string) (bool, error) {
	r.started <- id
	if id <= int64(r.waveSize) {
		<-r.firstRelease
	} else if id <= int64(2*r.waveSize) {
		<-r.secondRelease
	} else {
		<-r.thirdRelease
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	work := r.work[id]
	if work.PreviewStatus != links.StatusPending || !work.UpdatedAt.Equal(updatedAt) || work.Generation != generation {
		return false, nil
	}
	work.PreviewStatus = status
	r.work[id] = work
	r.completed++
	if r.completed == len(r.work) {
		close(r.done)
	}
	return true, nil
}

func (*refillPreviewRepo) SystemUpdateOGImage(context.Context, int64, string, time.Time, int64) (bool, error) {
	return false, nil
}

func (*refillPreviewRepo) SystemFinishScreenshotFallback(context.Context, int64, time.Time, int64) (bool, error) {
	return false, nil
}

func (r *refillPreviewRepo) SystemPendingPreviews(_ context.Context, limit int) ([]links.PreviewWork, error) {
	r.mu.Lock()
	r.queries++
	r.queryLimits = append(r.queryLimits, limit)
	query := r.queries
	ids := make([]int64, 0, len(r.work))
	for id, work := range r.work {
		if work.PreviewStatus == links.StatusPending {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	previews := make([]links.PreviewWork, 0, len(ids))
	for _, id := range ids {
		previews = append(previews, r.work[id])
	}
	r.mu.Unlock()
	r.queryDone <- query
	return previews, nil
}

func TestWorker_RecoveryRefillsAfterEachQueuedWaveWithoutTicker(t *testing.T) {
	const concurrency = 2
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	thirdRelease := make(chan struct{})
	var firstOnce, secondOnce, thirdOnce sync.Once
	releaseFirst := func() { firstOnce.Do(func() { close(firstRelease) }) }
	releaseSecond := func() { secondOnce.Do(func() { close(secondRelease) }) }
	releaseThird := func() { thirdOnce.Do(func() { close(thirdRelease) }) }
	repo := newRefillPreviewRepo(3*concurrency, concurrency, firstRelease, secondRelease, thirdRelease)
	w := NewWorker(nil, concurrency, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.repo = repo

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	t.Cleanup(func() {
		cancel()
		releaseFirst()
		releaseSecond()
		releaseThird()
		w.Stop()
	})

	select {
	case got := <-repo.queryDone:
		assert.Equal(t, 1, got)
	case <-time.After(time.Second):
		t.Fatal("initial recovery query did not run")
	}
	started := make([]int64, 0, concurrency)
	for range concurrency {
		select {
		case got := <-repo.started:
			started = append(started, got)
		case <-time.After(time.Second):
			t.Fatal("first recovery wave did not start")
		}
	}
	assert.ElementsMatch(t, []int64{1, 2}, started)
	releaseFirst()
	started = started[:0]
	for range concurrency {
		select {
		case got := <-repo.started:
			started = append(started, got)
		case <-time.After(time.Second):
			t.Fatal("second recovery wave did not start")
		}
	}
	assert.ElementsMatch(t, []int64{3, 4}, started)
	select {
	case got := <-repo.queryDone:
		assert.Equal(t, 2, got)
	case <-time.After(time.Second):
		t.Fatal("queue drain did not trigger a recovery refill query")
	}
	releaseSecond()
	started = started[:0]
	for range concurrency {
		select {
		case got := <-repo.started:
			started = append(started, got)
		case <-time.After(time.Second):
			t.Fatal("the drain wake did not start the final recovery wave")
		}
	}
	assert.ElementsMatch(t, []int64{5, 6}, started)
	releaseThird()

	select {
	case <-repo.done:
	case <-time.After(time.Second):
		t.Fatal("pending previews were not refilled before the 45-second fallback tick")
	}
	repo.mu.Lock()
	assert.Equal(t, 3*concurrency, repo.completed)
	assert.Less(t, repo.queries, repo.completed, "recovery must query per set, not per preview")
	for _, limit := range repo.queryLimits {
		assert.LessOrEqual(t, limit, 2*concurrency, "recovery queries must track queue capacity, not the 1,000-row ceiling")
	}
	assert.Zero(t, repo.getCalls, "set-wise recovery projections must avoid per-item hydration")
	repo.mu.Unlock()
}

func workerTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.White)
	var out bytes.Buffer
	require.NoError(t, png.Encode(&out, img))
	return out.Bytes()
}

func workerDecodeBombPNG(t *testing.T) []byte {
	t.Helper()
	bomb := append([]byte(nil), workerTestPNG(t)...)
	require.GreaterOrEqual(t, len(bomb), 33)
	binary.BigEndian.PutUint32(bomb[16:20], 8_000)
	binary.BigEndian.PutUint32(bomb[20:24], 8_000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	return bomb
}

func TestWorker_RemoveFallbackObjectSurvivesCallerCancellation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	uploader := &fakeUploader{}
	w := NewWorker(nil, 1, time.Second, logger)
	w.uploader = uploader
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w.removeFallbackObject(ctx, 42, "screenshots/42.version.jpg")

	assert.Equal(t, []string{"screenshots/42.version.jpg"}, uploader.deleted)
	assert.Equal(t, []error{nil}, uploader.deleteContextErrors)
}

func TestWorker_EnqueueDropsWhenChannelFull(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)
	// Saturate the channel without starting any consumers.
	capacity := cap(w.jobs)
	for i := 0; i < capacity; i++ {
		assert.NoError(t, w.Enqueue(int64(i)), "first %d sends must succeed", capacity)
	}
	// The next one must hit the `default` branch and return ErrQueueFull
	// without blocking.
	done := make(chan error, 1)
	go func() {
		done <- w.Enqueue(99999)
	}()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrQueueFull)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Enqueue blocked when channel was full")
	}
}

func TestWorker_QueueDrainWithoutOverflowDoesNotWakeRecovery(t *testing.T) {
	w := NewWorker(nil, 1, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, w.Enqueue(42))
	job := <-w.jobs
	w.wakeRecoveryIfNeeded()

	select {
	case <-w.recoveryWake:
		t.Fatal("an ordinary queue drain must not trigger a recovery query")
	default:
	}
	w.finishJob(job.id)
}

func TestWorker_EnqueueDeduplicatesScheduledID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)

	assert.NoError(t, w.Enqueue(42))
	assert.NoError(t, w.Enqueue(42))
	assert.Equal(t, 1, len(w.jobs))
	w.Stop()
}

func TestWorker_EnqueueDuringRunningJobSchedulesOneRerun(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)
	assert.NoError(t, w.Enqueue(42))
	<-w.jobs
	w.startJob(42)

	assert.NoError(t, w.Enqueue(42))
	assert.NoError(t, w.Enqueue(42))
	w.finishJob(42)
	assert.Equal(t, 1, len(w.jobs))
	w.Stop()
}

func TestWorker_DroppedRerunRequestsImmediateRecovery(t *testing.T) {
	w := NewWorker(nil, 1, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, w.Enqueue(42))
	<-w.jobs
	w.startJob(42)
	require.NoError(t, w.Enqueue(42))
	require.NoError(t, w.Enqueue(99))

	w.finishJob(42)
	assert.True(t, w.recoveryNeeded.Load())
	<-w.jobs
	w.wakeRecoveryIfNeeded()
	select {
	case <-w.recoveryWake:
	case <-time.After(time.Second):
		t.Fatal("a rerun dropped from the full queue did not wake recovery after capacity returned")
	}
}

func TestWorker_RecoveryDoesNotRerunScheduledJob(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)
	assert.NoError(t, w.Enqueue(42))
	<-w.jobs
	w.startJob(42)

	assert.NoError(t, w.enqueueRecovered(links.PreviewWork{ID: 42}))
	w.finishJob(42)
	assert.Empty(t, w.jobs)
	w.Stop()
}

func TestWorker_ExplicitRefreshRerunsQueuedRecoveryClaim(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)
	assert.NoError(t, w.enqueueRecovered(links.PreviewWork{ID: 42}))
	first := <-w.jobs

	assert.NoError(t, w.Enqueue(42))
	w.startJob(first.id)
	w.finishJob(first.id)

	require.Len(t, w.jobs, 1)
	rerun := <-w.jobs
	assert.Equal(t, int64(42), rerun.id)
	assert.False(t, rerun.claimed, "rerun must hydrate the latest explicit generation")
	w.Stop()
}

// TestWorker_EnqueueAfterStopReturnsErrStopped locks the Stop drain semantics:
// post-Stop sends must not silently fill the buffer (would let new HTTP
// requests succeed but never get processed). Tests Stop+Enqueue without Start
// because Start spawns requeuePending which needs a real *pgxpool.Pool.
func TestWorker_EnqueueAfterStopReturnsErrStopped(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)
	w.Stop() // safe without Start — cancel guard is nil-safe

	err := w.Enqueue(42)
	assert.ErrorIs(t, err, ErrStopped)
}

// TestWorker_StopDrainsBufferedJobs proves that jobs accepted into the
// channel before/during Stop must not sit forever with no consumer.
func TestWorker_StopDrainsBufferedJobs(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 2, time.Second, logger)
	assert.NoError(t, w.Enqueue(1))
	assert.NoError(t, w.Enqueue(2))
	w.Stop() // no Start — Wait is no-op; drain empties buffer
	assert.Equal(t, 0, len(w.jobs))
	assert.ErrorIs(t, w.Enqueue(3), ErrStopped)
}

func TestWorker_EnqueueDuringStop_ReturnsErrStopped(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(nil, 1, time.Second, logger)
	w.stopped.Store(true)
	assert.ErrorIs(t, w.Enqueue(1), ErrStopped)

	w2 := NewWorker(nil, 1, time.Second, logger)
	assert.NoError(t, w2.Enqueue(9))
	w2.stopped.Store(true)
	// Channel still has capacity; send succeeds then post-check fails.
	err := w2.Enqueue(10)
	assert.ErrorIs(t, err, ErrStopped)
	w2.Stop()
	assert.Equal(t, 0, len(w2.jobs), "Stop must drain leftover jobs")
}
