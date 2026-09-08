package preview

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/imageopt"
	"foldex/internal/linkimage"
	"foldex/internal/links"
	"foldex/internal/pkg/resourcebudget"
	"foldex/internal/ports"
)

const (
	screenshotMaxDim         = 1024
	screenshotQuality        = 82
	screenshotCaptureTimeout = 70 * time.Second
	screenshotPolicyTimeout  = 5 * time.Second
	screenshotStorageTimeout = 10 * time.Second
	previewQueueWaves        = 1
	previewRecoveryBatch     = 1000
)

// Screenshotter captures a URL and returns PNG bytes. Optional fallback.
type Screenshotter interface {
	Capture(ctx context.Context, pageURL string) ([]byte, error)
}

// Uploader stores image bytes to object storage under a key. DeleteObject is
// used to purge sibling-extension orphans when a re-encoded screenshot lands
// at a new key (e.g. legacy .png replaced by .jpg).
type Uploader interface {
	Upload(ctx context.Context, key string, data []byte, contentType string) error
	DeleteObject(ctx context.Context, key string) error
}

type previewRepository interface {
	SystemGetPreview(context.Context, int64) (links.PreviewWork, error)
	SystemUpdatePreviewIfUnchanged(context.Context, int64, time.Time, int64, links.PreviewStatus, links.PreviewPatch) (bool, error)
	SystemUpdateOGImage(context.Context, int64, string, time.Time, int64) (bool, error)
	SystemFinishScreenshotFallback(context.Context, int64, time.Time, int64) (bool, error)
	SystemPendingPreviews(context.Context, int) ([]links.PreviewWork, error)
}

type previewJob struct {
	id      int64
	work    links.PreviewWork
	claimed bool
}

type Worker struct {
	repo       previewRepository
	fetcher    *Fetcher
	jobs       chan previewJob
	concurrent int
	logger     *slog.Logger

	// Optional screenshot fallback. When both are non-nil and the HTML fetch
	// returned an empty og:image for a public URL, we capture a screenshot and
	// store it as the link's og_image_url. Either nil disables the fallback.
	screenshotter       Screenshotter
	uploader            Uploader
	screenshotURLPolicy func(context.Context, string) bool

	wg             sync.WaitGroup
	cancel         context.CancelFunc
	stopOnce       sync.Once
	stopped        atomic.Bool
	recoveryNeeded atomic.Bool
	recoveryWake   chan struct{}
	jobsMu         sync.Mutex
	scheduled      map[int64]scheduledJob
}

type scheduledJob struct {
	running  bool
	rerun    bool
	recovery bool
}

type enqueueMode bool

const (
	explicitEnqueue enqueueMode = true
	recoveryEnqueue enqueueMode = false
)

func NewWorker(pool *pgxpool.Pool, concurrency int, timeout time.Duration, logger *slog.Logger) *Worker {
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > resourcebudget.BackgroundWorkerConcurrency {
		concurrency = resourcebudget.BackgroundWorkerConcurrency
	}
	return &Worker{
		repo:    links.NewRepository(pool),
		fetcher: NewFetcher(timeout),
		// At most one successor wave waits behind the active workers. Recovery
		// refills each drained wave, so a deeper queue adds age without throughput.
		jobs:                make(chan previewJob, concurrency*previewQueueWaves),
		concurrent:          concurrency,
		logger:              logger.With("component", "preview"),
		screenshotURLPolicy: IsPublicURL,
		recoveryWake:        make(chan struct{}, 1),
		scheduled:           make(map[int64]scheduledJob),
	}
}

// Start spins up the goroutine pool. It also re-enqueues any link still in
// preview_status='pending' (crash recovery), refilling whenever the queued
// wave drains. A periodic sweep recovers lost wakeups.
func (w *Worker) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	for i := 0; i < w.concurrent; i++ {
		w.wg.Add(1)
		go w.loop(ctx)
	}
	w.wg.Add(1)
	go w.requeueLoop(ctx)
}

func (w *Worker) requeueLoop(ctx context.Context) {
	defer w.wg.Done()
	w.requeuePending(ctx)
	t := time.NewTicker(45 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.recoveryWake:
			w.requeuePending(ctx)
		case <-t.C:
			w.requeuePending(ctx)
		}
	}
}

// Stop signals shutdown via the context and waits for all goroutines to drain.
// The jobs channel is intentionally not closed: Enqueue may be called from
// requeuePending or in-flight HTTP handlers, and a closed-channel send would
// panic. Goroutines exit on ctx.Done(). After workers exit, leftover buffered
// jobs are drained so Enqueue-vs-Stop TOCTOU cannot park work forever.
// After Stop returns, Enqueue rejects with ports.ErrStopped.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() {
		w.stopped.Store(true)
		if w.cancel != nil {
			w.cancel()
		}
	})
	w.wg.Wait()
	for {
		select {
		case job := <-w.jobs:
			w.finishJob(job.id)
		default:
			return
		}
	}
}

// WithScreenshotFallback enables the post-fetch screenshot capture when the
// link has no og:image and resolves to a public host. Passing nil values is a
// no-op (fallback stays disabled).
func (w *Worker) WithScreenshotFallback(sc Screenshotter, up Uploader) {
	if sc == nil || up == nil {
		return
	}
	w.screenshotter = sc
	w.uploader = up
}

// Enqueue tries to schedule a preview job for linkID. Non-blocking — returns
// ports.ErrQueueFull when the bounded jobs channel has no slot and ports.ErrStopped after
// Stop has been called. The link row is already pending, so queue-full work is
// recovered as capacity returns. The internal Warn keeps the operational
// signal even when a caller discards the error.
func (w *Worker) Enqueue(linkID int64) error {
	return w.enqueue(previewJob{id: linkID}, explicitEnqueue)
}

func (w *Worker) enqueue(job previewJob, mode enqueueMode) error {
	linkID := job.id
	if w.stopped.Load() {
		return ports.ErrStopped
	}
	w.jobsMu.Lock()
	defer w.jobsMu.Unlock()
	if w.stopped.Load() {
		return ports.ErrStopped
	}
	if job, exists := w.scheduled[linkID]; exists {
		if mode == explicitEnqueue && (job.running || job.recovery) {
			job.rerun = true
			w.scheduled[linkID] = job
		}
		return nil
	}
	w.scheduled[linkID] = scheduledJob{recovery: job.claimed}
	select {
	case w.jobs <- job:
		// Re-check after send: Stop may have begun between Load and send. Job
		// sits in the buffer until Stop drains, so surface ErrStopped.
		if w.stopped.Load() {
			return ports.ErrStopped
		}
		return nil
	default:
		delete(w.scheduled, linkID)
		w.requireRecovery()
		w.logger.Warn("preview queue full, dropping job", "link_id", linkID)
		return ports.ErrQueueFull
	}
}

// enqueueRecovered differs from an explicit refresh: a recovery sweep must not
// request a rerun for work that is already queued or running.
func (w *Worker) enqueueRecovered(work links.PreviewWork) error {
	return w.enqueue(previewJob{id: work.ID, work: work, claimed: true}, recoveryEnqueue)
}

func (w *Worker) loop(ctx context.Context) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-w.jobs:
			if len(w.jobs) == 0 {
				w.wakeRecoveryIfNeeded()
			}
			w.startJob(job.id)
			func() {
				defer func() {
					if r := recover(); r != nil {
						w.logger.Error("preview job panicked", "link_id", job.id, "panic", r)
					}
				}()
				w.process(ctx, job)
			}()
			w.finishJob(job.id)
		}
	}
}

func (w *Worker) requireRecovery() {
	w.recoveryNeeded.Store(true)
	if len(w.jobs) == 0 {
		w.wakeRecoveryIfNeeded()
	}
}

func (w *Worker) wakeRecoveryIfNeeded() {
	if w.recoveryNeeded.CompareAndSwap(true, false) {
		w.wakeRecovery()
	}
}

func (w *Worker) wakeRecovery() {
	select {
	case w.recoveryWake <- struct{}{}:
	default:
	}
}

func (w *Worker) startJob(id int64) {
	w.jobsMu.Lock()
	job := w.scheduled[id]
	job.running = true
	w.scheduled[id] = job
	w.jobsMu.Unlock()
}

func (w *Worker) finishJob(id int64) {
	w.jobsMu.Lock()
	defer w.jobsMu.Unlock()
	job, exists := w.scheduled[id]
	if !exists || w.stopped.Load() || !job.rerun {
		delete(w.scheduled, id)
		return
	}
	job.running = false
	job.rerun = false
	job.recovery = false
	w.scheduled[id] = job
	select {
	case w.jobs <- previewJob{id: id}:
	default:
		delete(w.scheduled, id)
		w.requireRecovery()
		w.logger.Warn("preview queue full, dropping rerun", "link_id", id)
	}
}

func (w *Worker) process(ctx context.Context, job previewJob) {
	id := job.id
	link := job.work
	if !job.claimed {
		var err error
		link, err = w.repo.SystemGetPreview(ctx, id)
		if err != nil {
			w.logger.Warn("preview job: link not found", "link_id", id, "err", err)
			return
		}
	}
	if link.PreviewStatus != links.StatusPending {
		return
	}
	// Short-circuit: the user already supplied an image (uploaded between
	// Create and the worker picking up the job). No HTML fetch, no screenshot
	// — and lift the "capturando…" label by flipping preview_status to ok.
	if link.OGImageURL != nil && *link.OGImageURL != "" {
		if _, uErr := w.repo.SystemUpdatePreviewIfUnchanged(ctx, id, link.UpdatedAt, link.Generation, links.StatusOK, links.PreviewPatch{}); uErr != nil {
			w.logger.Error("preview short-circuit update failed", "link_id", id, "err", uErr)
		}
		w.logger.Info("preview skipped: image already present", "link_id", id)
		return
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := w.fetcher.Fetch(fetchCtx, link.URL)
	if err != nil {
		msg := "fetch_failed"
		if _, uErr := w.repo.SystemUpdatePreviewIfUnchanged(ctx, id, link.UpdatedAt, link.Generation, links.StatusFailed, links.PreviewPatch{Error: &msg}); uErr != nil {
			w.logger.Error("update preview failure row", "err", uErr)
		}
		w.logger.Info("preview failed", "link_id", id, "reason", operationErrorReason(err))
		return
	}
	var favicon, ogImage, description *string
	if res.FaviconURL != "" {
		favicon = &res.FaviconURL
	}
	if res.OGImageURL != "" {
		ogImage = &res.OGImageURL
	}
	if res.Description != "" {
		description = &res.Description
	}

	// Hold the "capturando…" label (preview_status='pending') while the
	// screenshot fallback runs. Without this, the frontend polling stops at
	// the first UpdatePreview and never sees the screenshot land — the card
	// would only refresh after a manual reload.
	willTryScreenshot := res.OGImageURL == "" && w.screenshotter != nil && w.uploader != nil
	firstStatus := links.StatusOK
	if willTryScreenshot {
		firstStatus = links.StatusPending
	}
	applied, err := w.repo.SystemUpdatePreviewIfUnchanged(ctx, id, link.UpdatedAt, link.Generation, firstStatus, links.PreviewPatch{
		Favicon:     favicon,
		OGImage:     ogImage,
		Description: description,
	})
	if err != nil {
		w.logger.Error("update preview row", "err", err)
		return
	}
	if !applied {
		return
	}
	w.logger.Info("preview ok", "link_id", id)

	if willTryScreenshot {
		followup, finishAt := w.maybeScreenshot(ctx, id, link.URL, link.Generation, link.UpdatedAt)
		switch followup {
		case screenshotFinished:
		case screenshotNeedsRelease, screenshotAbort:
			if _, uErr := w.repo.SystemFinishScreenshotFallback(ctx, id, finishAt, link.Generation); uErr != nil {
				w.logger.Error("status flip after screenshot fallback", "err", uErr)
			}
		}
	}
}

type screenshotFollowup int

const (
	screenshotFinished screenshotFollowup = iota
	screenshotNeedsRelease
	screenshotAbort
)

func (w *Worker) maybeScreenshot(ctx context.Context, id int64, pageURL string, generation int64, knownAt time.Time) (screenshotFollowup, time.Time) {
	if w.screenshotter == nil || w.uploader == nil {
		return screenshotFinished, time.Time{}
	}
	cur, err := w.repo.SystemGetPreview(ctx, id)
	if err != nil {
		return screenshotAbort, knownAt
	}
	if cur.Generation != generation || cur.PreviewStatus != links.StatusPending {
		return screenshotFinished, time.Time{}
	}
	if cur.OGImageURL != nil && *cur.OGImageURL != "" {
		return screenshotFinished, time.Time{}
	}
	shotCtx, cancel := context.WithTimeout(ctx, screenshotCaptureTimeout)
	defer cancel()
	policyCtx, policyCancel := context.WithTimeout(shotCtx, screenshotPolicyTimeout)
	allowed := w.screenshotURLPolicy(policyCtx, pageURL)
	policyCancel()
	if !allowed {
		w.logger.Info("screenshot fallback skipped: non-public host", "link_id", id)
		return screenshotNeedsRelease, cur.UpdatedAt
	}
	png, err := w.screenshotter.Capture(shotCtx, pageURL)
	if err != nil {
		w.logger.Warn("screenshot fallback capture failed", "link_id", id, "reason", operationErrorReason(err))
		return screenshotNeedsRelease, cur.UpdatedAt
	}

	opt, err := imageopt.OptimizeForStore(png)
	if err != nil {
		// Storing the original would skip INV-077 re-encode and, for
		// ErrTooLarge, leave a payload that OOMs any decoder of /api/files.
		w.logger.Warn("screenshot fallback rejected", "link_id", id, "err", err)
		return screenshotNeedsRelease, cur.UpdatedAt
	}

	storageCtx, storageCancel := context.WithTimeout(ctx, screenshotStorageTimeout)
	defer storageCancel()
	stored, err := linkimage.Store(storageCtx, w.uploader, "screenshots", id, opt.Ext, opt.Data, opt.ContentType)
	if err != nil {
		w.logger.Warn("screenshot fallback upload failed", "link_id", id, "err", err)
		return screenshotNeedsRelease, cur.UpdatedAt
	}
	applied, err := w.repo.SystemUpdateOGImage(storageCtx, id, stored.URL, cur.UpdatedAt, generation)
	if err != nil {
		w.logger.Warn("screenshot fallback db update failed", "link_id", id, "err", err)
		w.removeFallbackObject(ctx, id, stored.Key)
		return screenshotNeedsRelease, cur.UpdatedAt
	}
	if !applied {
		w.removeFallbackObject(ctx, id, stored.Key)
		w.logger.Info("screenshot fallback superseded", "link_id", id)
		return screenshotFinished, time.Time{}
	}
	for _, purgeErr := range linkimage.PurgeLegacy(storageCtx, w.uploader, "screenshots", id) {
		w.logger.Warn("screenshot fallback purge legacy failed", "link_id", id, "err", purgeErr)
	}
	w.logger.Info("screenshot fallback ok",
		"link_id", id, "key", stored.Key,
		"source_bytes", len(png), "stored_bytes", len(opt.Data),
		"resized", opt.Resized, "reencoded", opt.Reencoded,
	)
	return screenshotFinished, time.Time{}
}

func (w *Worker) removeFallbackObject(ctx context.Context, id int64, key string) {
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), screenshotStorageTimeout)
	defer cleanupCancel()
	if err := w.uploader.DeleteObject(cleanupCtx, key); err != nil {
		w.logger.Warn("screenshot fallback orphan cleanup failed", "link_id", id, "key", key, "err", err)
	}
}

func operationErrorReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "operation_failed"
	}
}

func (w *Worker) requeuePending(ctx context.Context) {
	limit := w.recoveryQueryLimit()
	if limit == 0 {
		w.requireRecovery()
		return
	}
	previews, err := w.repo.SystemPendingPreviews(ctx, limit)
	if err != nil {
		w.logger.Warn("requeue pending: query failed", "err", err)
		return
	}
	enqueued := 0
	for _, work := range previews {
		if ctx.Err() != nil {
			return
		}
		err := w.enqueueRecovered(work)
		if errors.Is(err, ports.ErrQueueFull) {
			break
		}
		if err == nil {
			enqueued++
		}
	}
	if enqueued > 0 {
		w.logger.Info("requeued pending previews", "count", enqueued)
	}
	if len(previews) == limit {
		w.requireRecovery()
	}
}

func (w *Worker) recoveryQueryLimit() int {
	w.jobsMu.Lock()
	scheduled := len(w.scheduled)
	w.jobsMu.Unlock()
	available := cap(w.jobs) - len(w.jobs)
	if available <= 0 {
		return 0
	}
	return min(previewRecoveryBatch, scheduled+available)
}
