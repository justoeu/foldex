package preview

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/links"
)

// Screenshotter captures a URL and returns PNG bytes. Optional fallback.
type Screenshotter interface {
	Capture(ctx context.Context, pageURL string) ([]byte, error)
}

// Uploader stores PNG bytes to object storage under a key. Optional fallback.
type Uploader interface {
	Upload(ctx context.Context, key string, data []byte, contentType string) error
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
// panic. Goroutines exit on ctx.Done().
func (w *Worker) Stop() {
	w.stopOnce.Do(func() {
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

func (w *Worker) Enqueue(linkID int64) {
	select {
	case w.jobs <- linkID:
	default:
		w.logger.Warn("preview queue full, dropping job", "link_id", linkID)
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
	link, err := w.repo.Get(ctx, id)
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
	cur, err := w.repo.Get(ctx, id)
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
	key := fmt.Sprintf("screenshots/%d.png", id)
	if err := w.uploader.Upload(shotCtx, key, png, "image/png"); err != nil {
		w.logger.Warn("screenshot fallback upload failed", "link_id", id, "err", err)
		return
	}
	proxyURL := "/api/files/" + key
	if err := w.repo.UpdateOGImage(ctx, id, proxyURL); err != nil {
		w.logger.Warn("screenshot fallback db update failed", "link_id", id, "err", err)
		return
	}
	w.logger.Info("screenshot fallback ok", "link_id", id, "key", key)
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
		w.Enqueue(id)
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
