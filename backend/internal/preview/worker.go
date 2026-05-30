package preview

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/imageopt"
	"foldex/internal/links"
)

// ErrQueueFull is returned by Enqueue when the bounded jobs channel has no
// available slot. Callers can decide to retry, log + drop, or fail the request.
// Returning an error (instead of silent drop) lets handlers surface
// backpressure to the client rather than pretending success.
var ErrQueueFull = errors.New("preview: queue full")

// ErrStopped is returned by Enqueue when the worker has been Stop()ped. The
// jobs channel stays open by design (sending to a closed channel panics, and
// requeuePending could race a shutdown), so this flag is the explicit signal
// that no further work will be processed.
var ErrStopped = errors.New("preview: worker stopped")

// Same defaults as the upload handler — see internal/links.imageMaxDim. JPEG
// q≈82 with a 1024 px cap on the longest side. Worker output goes through
// the same pipeline so screenshots aren't a special case.
const (
	screenshotMaxDim  = 1024
	screenshotQuality = 82
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

type Worker struct {
	pool       *pgxpool.Pool
	repo       *links.Repository
	fetcher    *Fetcher
	jobs       chan int64
	concurrent int
	logger     *slog.Logger

	// Optional screenshot fallback. When both are non-nil and the HTML fetch
	// returned an empty og:image for a public URL, we capture a screenshot and
	// store it as the link's og_image_url. Either nil disables the fallback.
	screenshotter Screenshotter
	uploader      Uploader

	wg       sync.WaitGroup
	cancel   context.CancelFunc
	stopOnce sync.Once
	stopped  atomic.Bool
}

func NewWorker(pool *pgxpool.Pool, concurrency int, timeout time.Duration, logger *slog.Logger) *Worker {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Worker{
		pool:       pool,
		repo:       links.NewRepository(pool),
		fetcher:    NewFetcher(timeout),
		jobs:       make(chan int64, 256),
		concurrent: concurrency,
		logger:     logger.With("component", "preview"),
	}
}

// Start spins up the goroutine pool. It also re-enqueues any link still in
// preview_status='pending' (crash recovery).
func (w *Worker) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	for i := 0; i < w.concurrent; i++ {
		w.wg.Add(1)
		go w.loop(ctx)
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.requeuePending(ctx)
	}()
}

// Stop signals shutdown via the context and waits for all goroutines to drain.
// The jobs channel is intentionally not closed: Enqueue may be called from
// requeuePending or in-flight HTTP handlers, and a closed-channel send would
// panic. Goroutines exit on ctx.Done(). After Stop returns, Enqueue rejects
// with ErrStopped so callers don't push into a queue with no consumers.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() {
		w.stopped.Store(true)
		if w.cancel != nil {
			w.cancel()
		}
	})
	w.wg.Wait()
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
// ErrQueueFull when the bounded jobs channel has no slot and ErrStopped after
// Stop has been called. The caller decides what to do with the error; today
// the API handlers and importer treat it as fire-and-forget (the link row
// already exists; requeuePending will pick stragglers up on next start). The
// internal Warn on ErrQueueFull keeps the operational signal even when
// callers discard the return.
func (w *Worker) Enqueue(linkID int64) error {
	if w.stopped.Load() {
		return ErrStopped
	}
	select {
	case w.jobs <- linkID:
		return nil
	default:
		w.logger.Warn("preview queue full, dropping job", "link_id", linkID)
		return ErrQueueFull
	}
}

func (w *Worker) loop(ctx context.Context) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-w.jobs:
			w.process(ctx, id)
		}
	}
}

func (w *Worker) process(ctx context.Context, id int64) {
	// GetMinimal skips the linkColumns LATERAL click_count join — the
	// worker doesn't need click stats. Saves one aggregate per preview run.
	link, err := w.repo.GetMinimal(ctx, id)
	if err != nil {
		w.logger.Warn("preview job: link not found", "link_id", id, "err", err)
		return
	}
	// Short-circuit: the user already supplied an image (uploaded between
	// Create and the worker picking up the job). No HTML fetch, no screenshot
	// — and lift the "capturando…" label by flipping preview_status to ok.
	if link.OGImageURL != nil && *link.OGImageURL != "" {
		if links.PreviewStatus(link.PreviewStatus) == links.StatusPending {
			if uErr := w.repo.UpdatePreview(ctx, id, links.StatusOK, nil, nil, nil, nil); uErr != nil {
				w.logger.Error("preview short-circuit update failed", "link_id", id, "err", uErr)
			}
		}
		w.logger.Info("preview skipped: image already present", "link_id", id)
		return
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := w.fetcher.Fetch(fetchCtx, link.URL)
	if err != nil {
		msg := err.Error()
		if uErr := w.repo.UpdatePreview(ctx, id, links.StatusFailed, nil, nil, nil, &msg); uErr != nil {
			w.logger.Error("update preview failure row", "err", uErr)
		}
		w.logger.Info("preview failed", "link_id", id, "err", err)
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
	if err := w.repo.UpdatePreview(ctx, id, firstStatus, favicon, ogImage, description, nil); err != nil {
		w.logger.Error("update preview row", "err", err)
		return
	}
	w.logger.Info("preview ok", "link_id", id)

	if willTryScreenshot {
		w.maybeScreenshot(ctx, id, link.URL)
		// Always converge to 'ok' once the screenshot phase is over. If it
		// succeeded, UpdateOGImage already set status='ok' (this is a no-op).
		// If it was skipped/failed, this flip releases the frontend poll.
		if uErr := w.repo.UpdatePreview(ctx, id, links.StatusOK, nil, nil, nil, nil); uErr != nil {
			w.logger.Error("status flip after screenshot fallback", "err", uErr)
		}
	}
}

// maybeScreenshot is the post-preview fallback. It runs only when the link has
// no og:image after the HTML fetch AND the URL is public AND the user has not
// uploaded a custom image in the meantime. Each guard short-circuits silently.
func (w *Worker) maybeScreenshot(ctx context.Context, id int64, pageURL string) {
	if w.screenshotter == nil || w.uploader == nil {
		return
	}
	// Same MinimalLink read as `process` — we only need og_image_url here
	// to decide whether the user uploaded an image while we were fetching
	// HTML; skipping the LATERAL click_count saves another aggregate.
	cur, err := w.repo.GetMinimal(ctx, id)
	if err != nil {
		return
	}
	if cur.OGImageURL != nil && *cur.OGImageURL != "" {
		// Either preview found one or the user uploaded one — leave it alone.
		return
	}
	if !IsPublicURL(ctx, pageURL) {
		w.logger.Info("screenshot fallback skipped: non-public host", "link_id", id)
		return
	}
	shotCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	png, err := w.screenshotter.Capture(shotCtx, pageURL)
	if err != nil {
		w.logger.Warn("screenshot fallback capture failed", "link_id", id, "err", err)
		return
	}

	opt, err := imageopt.Optimize(png, imageopt.Options{MaxDim: screenshotMaxDim, Quality: screenshotQuality})
	if err != nil {
		// ErrTooLarge means a hostile page returned a decode-bomb image
		// (small payload, huge declared dimensions). Storing the raw PNG
		// would let any browser opening /api/files/screenshots/{id} OOM
		// on decode. Abort the fallback entirely — link keeps og_image_url
		// empty, UI just shows no preview.
		if errors.Is(err, imageopt.ErrTooLarge) {
			w.logger.Warn("screenshot fallback rejected: decode bomb", "link_id", id, "err", err)
			return
		}
		// Other errors (truncated/corrupt encode) fall back to storing the
		// raw PNG so a re-encode bug never blocks a working screenshot —
		// ProxyFile streams bytes without re-decoding, so backend stays safe.
		w.logger.Warn("screenshot fallback optimize failed, storing original PNG",
			"link_id", id, "err", err)
		opt = imageopt.Result{Data: png, ContentType: "image/png", Ext: "png"}
	}

	key := fmt.Sprintf("screenshots/%d.%s", id, opt.Ext)
	// Purge stale sibling-extension keys so the bucket doesn't accumulate
	// orphans when a link's screenshot moves from .png to .jpg (or vice
	// versa via the fallback path). DeleteObject is idempotent.
	for _, ext := range []string{"png", "jpg", "gif", "webp"} {
		if ext == opt.Ext {
			continue
		}
		stale := fmt.Sprintf("screenshots/%d.%s", id, ext)
		if delErr := w.uploader.DeleteObject(shotCtx, stale); delErr != nil {
			w.logger.Warn("screenshot fallback purge legacy failed",
				"link_id", id, "key", stale, "err", delErr)
		}
	}
	if err := w.uploader.Upload(shotCtx, key, opt.Data, opt.ContentType); err != nil {
		w.logger.Warn("screenshot fallback upload failed", "link_id", id, "err", err)
		return
	}
	proxyURL := "/api/files/" + key
	if err := w.repo.UpdateOGImage(ctx, id, proxyURL); err != nil {
		w.logger.Warn("screenshot fallback db update failed", "link_id", id, "err", err)
		return
	}
	w.logger.Info("screenshot fallback ok",
		"link_id", id, "key", key,
		"source_bytes", len(png), "stored_bytes", len(opt.Data),
		"resized", opt.Resized, "reencoded", opt.Reencoded,
	)
}

func (w *Worker) requeuePending(ctx context.Context) {
	ids, err := w.pendingIDs(ctx)
	if err != nil {
		w.logger.Warn("requeue pending: query failed", "err", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		// Discard the Enqueue result inside requeuePending — if the queue is
		// full we already logged at the Warn level, and the next requeuePending
		// run will re-pick the still-pending IDs.
		_ = w.Enqueue(id)
	}
	if len(ids) > 0 {
		w.logger.Info("requeued pending previews", "count", len(ids))
	}
}

func (w *Worker) pendingIDs(ctx context.Context) ([]int64, error) {
	rows, err := w.pool.Query(ctx, `SELECT id FROM link WHERE preview_status = 'pending' ORDER BY id ASC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
