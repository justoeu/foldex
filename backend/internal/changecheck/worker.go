package changecheck

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"foldex/internal/links"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/resourcebudget"
)

const (
	pushQueueSize = 32
	pushTimeout   = 15 * time.Second
)

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
	SystemFindDueForCheck(ctx context.Context, limit int) ([]links.DueLink, error)
	SystemRecordCheckResult(ctx context.Context, id int64, expectedClaimedAt time.Time, res links.CheckResult) (bool, error)
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

	pushJobs chan Notification

	wg       sync.WaitGroup
	cancel   context.CancelFunc
	stopOnce sync.Once
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
	if o.Concurrency > resourcebudget.BackgroundWorkerConcurrency {
		o.Concurrency = resourcebudget.BackgroundWorkerConcurrency
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
	return &Worker{
		repo:         repo,
		fetcher:      fetcher,
		fingerprint:  NewFingerprinter(fetcher),
		sender:       sender,
		logger:       logger.With("component", "changecheck"),
		jobs:         make(chan links.DueLink, 256),
		pushJobs:     make(chan Notification, pushQueueSize),
		concurrent:   opts.Concurrency,
		scanInterval: opts.ScanInterval,
		fetchTimeout: opts.FetchTimeout,
	}
}

// Start spins up the fetch, notification, and scan goroutines.
func (w *Worker) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	for i := 0; i < w.concurrent; i++ {
		w.wg.Add(1)
		go w.loop(ctx)
	}
	if w.sender != nil {
		for i := 0; i < w.concurrent; i++ {
			w.wg.Add(1)
			go w.pushLoop(ctx)
		}
	}
	w.wg.Add(1)
	go w.tick(ctx)
}

// Stop is idempotent — repeated calls block on the first wg.Wait.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	w.wg.Wait()
	// Drain leftover buffered work so racing producers do not retain payloads
	// after all consumers have exited.
drainJobs:
	for {
		select {
		case <-w.jobs:
		default:
			break drainJobs
		}
	}
drainPush:
	for {
		select {
		case <-w.pushJobs:
		default:
			break drainPush
		}
	}
}

func (w *Worker) loop(ctx context.Context) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-w.jobs:
			w.process(ctx, job)
		}
	}
}

func (w *Worker) pushLoop(ctx context.Context) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case notification := <-w.pushJobs:
			if ctx.Err() != nil {
				return
			}
			pushCtx, cancel := context.WithTimeout(ctx, pushTimeout)
			err := w.sender.Notify(pushCtx, notification)
			cancel()
			if err != nil && ctx.Err() == nil {
				w.logger.Warn("push notify failed", "link_id", notification.LinkID, "err", err)
			}
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
	available := cap(w.jobs) - len(w.jobs)
	if available == 0 {
		return
	}
	due, err := w.repo.SystemFindDueForCheck(ctx, available)
	if err != nil {
		w.logger.Warn("scan: find due failed", "err", err)
		return
	}
	enqueued := 0
	for _, job := range due {
		select {
		case <-ctx.Done():
			return
		case w.jobs <- job:
			enqueued++
		}
	}
	if enqueued > 0 {
		w.logger.Info("scan: enqueued due links", "count", enqueued)
	}
}

// process records the claimed configuration only if its last_checked_at token is
// still current, then queues a notification for an applied change.
func (w *Worker) process(ctx context.Context, job links.DueLink) {
	id := job.ID
	if job.CheckInterval == "" {
		return
	}

	fetchCtx, cancel := context.WithTimeout(ctx, w.fetchTimeout)
	defer cancel()
	body, _, err := w.fetcher.GetRaw(fetchCtx, job.URL)
	if err != nil {
		w.logger.Info("process: fetch failed", "link_id", id, "err", err)
		if _, recErr := w.repo.SystemRecordCheckResult(ctx, id, job.ClaimedAt, links.CheckResult{
			Fingerprint: "",
			Changed:     false,
			FetchErr:    err.Error(),
		}); recErr != nil {
			w.logger.Error("process: record result failed", "link_id", id, "err", recErr)
		}
		return
	}

	kind, hash, err := w.fingerprint.Compute(fetchCtx, job.URL, body)
	if err != nil {
		w.logger.Info("process: fingerprint failed", "link_id", id, "err", err)
		if _, recErr := w.repo.SystemRecordCheckResult(ctx, id, job.ClaimedAt, links.CheckResult{
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
	if job.LastFingerprint != nil {
		prevKind, prevHash = SplitFingerprint(*job.LastFingerprint)
	}
	// "Changed" only when we have a previous fingerprint AND the kind matches
	// AND the hash differs. The kind-must-match rule prevents a false-positive
	// when a page first gains an RSS feed (prev=content:abc, new=feed:def) —
	// the new baseline gets stored, but no push fires.
	changed := prevHash != "" && prevKind == kind && prevHash != hash

	applied, err := w.repo.SystemRecordCheckResult(ctx, id, job.ClaimedAt, links.CheckResult{
		Fingerprint: newFp,
		Changed:     changed,
		FetchErr:    "",
	})
	if err != nil {
		w.logger.Error("process: record result failed", "link_id", id, "err", err)
		return
	}
	if !applied {
		return
	}

	if changed && w.sender != nil {
		notification := Notification{
			LinkID: id,
			Title:  job.Title,
			URL:    job.URL,
			Kind:   "change_detected",
			UserID: job.UserID,
		}
		select {
		case w.pushJobs <- notification:
		default:
			w.logger.Warn("push notification queue full, dropping notification", "link_id", id)
		}
		w.logger.Info("change detected", "link_id", id, "kind", kind)
	}
}
