package changecheck

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"foldex/internal/links"
	"foldex/internal/pkg/authctx"
)

// ErrQueueFull is returned by Enqueue when the bounded jobs channel has no
// available slot. Same semantics as preview.ErrQueueFull — handlers/tick
// caller decides whether to retry or just drop (the next tick will pick the
// link up again).
var ErrQueueFull = errors.New("changecheck: queue full")

// ErrStopped is returned by Enqueue after Stop has been called. The jobs
// channel is intentionally not closed (send-on-closed panics under racing
// requeue + shutdown) so this flag is the explicit "no more work" signal.
var ErrStopped = errors.New("changecheck: worker stopped")

// Sender is the push notification dependency. Implemented by internal/push
// in Phase 3. Kept as a tiny interface here so the worker stays
// import-cycle-free and unit tests can inject a no-op or assertion sender.
type Sender interface {
	Notify(ctx context.Context, n Notification) error
}

// Notification is the payload the Sender encrypts and ships to the browser.
// Kept package-local so the worker doesn't depend on the push package.
type Notification struct {
	LinkID int64  `json:"link_id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Kind   string `json:"kind"` // "change_detected"
	// UserID scopes delivery: the notification goes only to the link owner's
	// push subscriptions. Without it a multi-tenant install would broadcast one
	// user's page-change (title and URL included) to everyone subscribed.
	UserID authctx.UserID `json:"-"`
}

// Repo is the storage contract used by the worker. Narrowed from
// *links.Repository so tests can mock it without standing up Postgres.
type Repo interface {
	SystemGet(ctx context.Context, id int64) (links.Link, error)
	SystemFindDueForCheck(ctx context.Context, limit int) ([]links.DueLink, error)
	SystemRecordCheckResult(ctx context.Context, id int64, res links.CheckResult) error
}

// Fetcher is the HTTP dependency. preview.Fetcher.GetRaw satisfies it via
// the fingerprinter's HTTPGetter — we re-use the SSRF-guarded transport.
type Fetcher interface {
	GetRaw(ctx context.Context, pageURL string) (body []byte, contentType string, err error)
}

type Worker struct {
	repo         Repo
	fetcher      Fetcher
	fingerprint  *Fingerprinter
	sender       Sender
	logger       *slog.Logger
	jobs         chan links.DueLink
	concurrent   int
	scanInterval time.Duration
	fetchTimeout time.Duration

	// pushSem bounds concurrent Notify goroutines; pushWg lets Stop wait for
	// in-flight pushes so we don't leak after cancel.
	pushSem chan struct{}
	pushWg  sync.WaitGroup

	wg       sync.WaitGroup
	cancel   context.CancelFunc
	stopOnce sync.Once
	stopped  atomic.Bool
}

// Options groups all knobs so callers can pass defaults without a long
// positional argument list.
type Options struct {
	Concurrency  int
	ScanInterval time.Duration
	FetchTimeout time.Duration
}

func defaultOptions(o Options) Options {
	if o.Concurrency < 1 {
		o.Concurrency = 2
	}
	if o.ScanInterval <= 0 {
		o.ScanInterval = 60 * time.Second
	}
	if o.FetchTimeout <= 0 {
		o.FetchTimeout = 20 * time.Second
	}
	return o
}

func New(repo Repo, fetcher Fetcher, sender Sender, opts Options, logger *slog.Logger) *Worker {
	opts = defaultOptions(opts)
	// Cap concurrent push deliveries to worker concurrency (not unbounded).
	pushN := opts.Concurrency
	if pushN < 1 {
		pushN = 1
	}
	if pushN > 8 {
		pushN = 8
	}
	return &Worker{
		repo:         repo,
		fetcher:      fetcher,
		fingerprint:  NewFingerprinter(fetcher),
		sender:       sender,
		logger:       logger.With("component", "changecheck"),
		jobs:         make(chan links.DueLink, 256),
		concurrent:   opts.Concurrency,
		scanInterval: opts.ScanInterval,
		fetchTimeout: opts.FetchTimeout,
		pushSem:      make(chan struct{}, pushN),
	}
}

// Start spins the goroutine pool + a single tick() goroutine. The tick is
// the only thing that calls Enqueue from inside the worker — handlers can
// also Enqueue when the user opts a link in via PATCH (so the first check
// happens immediately instead of waiting up to scanInterval).
func (w *Worker) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	for i := 0; i < w.concurrent; i++ {
		w.wg.Add(1)
		go w.loop(ctx)
	}
	w.wg.Add(1)
	go w.tick(ctx)
}

// Stop is idempotent — repeated calls block on the first wg.Wait. Setting
// stopped before cancel is important: an in-flight Enqueue racing with Stop
// reads stopped=true and refuses without sending into a channel whose
// consumers are about to exit.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() {
		w.stopped.Store(true)
		if w.cancel != nil {
			w.cancel()
		}
	})
	w.wg.Wait()
	w.pushWg.Wait()
	// Drain leftover buffered jobs so Enqueue-vs-Stop TOCTOU cannot park work
	// with no consumer (RACE-HER-010).
	for {
		select {
		case <-w.jobs:
		default:
			return
		}
	}
}

// Enqueue schedules an immediate check for job.ID. Non-blocking — returns
// ErrQueueFull / ErrStopped without sending so callers can decide whether to
// retry, log, or just drop.
func (w *Worker) Enqueue(job links.DueLink) error {
	if w.stopped.Load() {
		return ErrStopped
	}
	select {
	case w.jobs <- job:
		if w.stopped.Load() {
			return ErrStopped
		}
		return nil
	default:
		w.logger.Warn("changecheck queue full, dropping job", "link_id", job.ID)
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

func (w *Worker) tick(ctx context.Context) {
	defer w.wg.Done()
	// First tick fires immediately so a freshly opted-in link doesn't sit
	// uninspected for a full scanInterval after server boot.
	w.scan(ctx)
	t := time.NewTicker(w.scanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.scan(ctx)
		}
	}
}

func (w *Worker) scan(ctx context.Context) {
	due, err := w.repo.SystemFindDueForCheck(ctx, 256)
	if err != nil {
		w.logger.Warn("scan: find due failed", "err", err)
		return
	}
	for _, job := range due {
		if ctx.Err() != nil {
			return
		}
		_ = w.Enqueue(job) // ErrQueueFull is fine — next tick re-picks
	}
	if len(due) > 0 {
		w.logger.Info("scan: enqueued due links", "count", len(due))
	}
}

// process fetches the link's HTML, computes a fingerprint, diffs against the
// previous one, and either records a no-op check (only last_checked_at moves)
// or fires the push notification before recording the change. The whole
// path tolerates fetch failures — the worker still records the attempt
// (so the link isn't re-tried every tick) and stores the error message in
// preview_error for surfacing in the UI later.
func (w *Worker) process(ctx context.Context, job links.DueLink) {
	id := job.ID
	link, err := w.repo.SystemGet(ctx, id)
	if err != nil {
		w.logger.Warn("process: link not found", "link_id", id, "err", err)
		return
	}
	// Defensive: opt-out happened between scan and process. Recording a
	// result with empty fingerprint would be a no-op but burns work.
	if link.CheckInterval == nil {
		return
	}

	fetchCtx, cancel := context.WithTimeout(ctx, w.fetchTimeout)
	defer cancel()
	body, _, err := w.fetcher.GetRaw(fetchCtx, link.URL)
	if err != nil {
		w.logger.Info("process: fetch failed", "link_id", id, "err", err)
		if recErr := w.repo.SystemRecordCheckResult(ctx, id, links.CheckResult{
			Fingerprint: "",
			Changed:     false,
			FetchErr:    err.Error(),
		}); recErr != nil {
			w.logger.Error("process: record result failed", "link_id", id, "err", recErr)
		}
		return
	}

	kind, hash, err := w.fingerprint.Compute(fetchCtx, link.URL, body)
	if err != nil {
		w.logger.Info("process: fingerprint failed", "link_id", id, "err", err)
		if recErr := w.repo.SystemRecordCheckResult(ctx, id, links.CheckResult{
			Fingerprint: "",
			Changed:     false,
			FetchErr:    "fingerprint: " + err.Error(),
		}); recErr != nil {
			w.logger.Error("process: record result failed", "link_id", id, "err", recErr)
		}
		return
	}
	newFp := FormatFingerprint(kind, hash)

	prevKind, prevHash := "", ""
	if link.LastFingerprint != nil {
		prevKind, prevHash = SplitFingerprint(*link.LastFingerprint)
	}
	// "Changed" only when we have a previous fingerprint AND the kind matches
	// AND the hash differs. The kind-must-match rule prevents a false-positive
	// when a page first gains an RSS feed (prev=content:abc, new=feed:def) —
	// the new baseline gets stored, but no push fires.
	changed := prevHash != "" && prevKind == kind && prevHash != hash

	if err := w.repo.SystemRecordCheckResult(ctx, id, links.CheckResult{
		Fingerprint: newFp,
		Changed:     changed,
		FetchErr:    "",
	}); err != nil {
		w.logger.Error("process: record result failed", "link_id", id, "err", err)
		return
	}

	if changed && w.sender != nil {
		// Fire push outside the record path — failures must not block the
		// fingerprint update. Bounded by pushSem so a flood of changes can't
		// spawn unbounded goroutines; Stop waits on pushWg.
		w.pushWg.Add(1)
		go func(linkID int64, owner authctx.UserID, title, url string) {
			defer w.pushWg.Done()
			select {
			case w.pushSem <- struct{}{}:
				defer func() { <-w.pushSem }()
			case <-time.After(30 * time.Second):
				w.logger.Warn("push notify dropped: semaphore timeout", "link_id", linkID)
				return
			}
			pushCtx, pcancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer pcancel()
			if err := w.sender.Notify(pushCtx, Notification{
				LinkID: linkID,
				Title:  title,
				URL:    url,
				Kind:   "change_detected",
				UserID: owner,
			}); err != nil {
				w.logger.Warn("push notify failed", "link_id", linkID, "err", err)
			}
		}(link.ID, job.UserID, link.Title, link.URL)
		w.logger.Info("change detected", "link_id", id, "kind", kind)
	}
}
