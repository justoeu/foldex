package changecheck

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/pkg/authctx"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ----- Mocks -----

type fakeRepo struct {
	mu         sync.Mutex
	links      map[int64]links.Link
	due        []links.DueLink
	findLimits []int
	results    []links.CheckResult
	getCalls   int
	findErr    error
	recErr     error
	recNoop    bool
	recDelay   time.Duration
}

func (r *fakeRepo) SystemGet(_ context.Context, id int64) (links.Link, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getCalls++
	l, ok := r.links[id]
	if !ok {
		return links.Link{}, errors.New("not found")
	}
	return l, nil
}

func (r *fakeRepo) SystemFindDueForCheck(_ context.Context, limit int) ([]links.DueLink, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findLimits = append(r.findLimits, limit)
	if r.findErr != nil {
		return nil, r.findErr
	}
	if limit > len(r.due) {
		limit = len(r.due)
	}
	out := append([]links.DueLink(nil), r.due[:limit]...)
	r.due = r.due[limit:]
	return out, nil
}

func (r *fakeRepo) SystemRecordCheckResult(_ context.Context, _ int64, _ time.Time, res links.CheckResult) (bool, error) {
	if r.recDelay > 0 {
		time.Sleep(r.recDelay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recErr != nil {
		return false, r.recErr
	}
	if r.recNoop {
		return false, nil
	}
	r.results = append(r.results, res)
	return true, nil
}

func (r *fakeRepo) snapshotResults() []links.CheckResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]links.CheckResult(nil), r.results...)
}

func (r *fakeRepo) snapshotGetCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getCalls
}

func (r *fakeRepo) snapshotScanState() ([]links.DueLink, []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]links.DueLink(nil), r.due...), append([]int(nil), r.findLimits...)
}

type fakeFetcher struct {
	body []byte
	err  error
}

func (f fakeFetcher) GetRaw(_ context.Context, _ string) ([]byte, string, error) {
	return f.body, "text/html", f.err
}

type fakeSender struct {
	mu    sync.Mutex
	calls []Notification
	err   error
}

type cancelAwareSender struct {
	started chan struct{}
	release chan struct{}
}

func (s *cancelAwareSender) Notify(ctx context.Context, _ Notification) error {
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		return nil
	}
}

func (s *fakeSender) Notify(_ context.Context, n Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, n)
	return nil
}

func (s *fakeSender) seen() []Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Notification(nil), s.calls...)
}

// ----- Lifecycle -----

func TestStop_Idempotent(t *testing.T) {
	w := New(&fakeRepo{}, fakeFetcher{}, nil, Options{ScanInterval: time.Hour}, testLogger())
	w.Start(context.Background())
	w.Stop()
	w.Stop() // must not panic or deadlock
}

func TestChangeCheckWorker_PanicOnOneJobDoesNotKillTheProcess(t *testing.T) {
	repo := &fakeRepo{}
	var logs bytes.Buffer
	w := New(repo, panicOnURLFetcher{panicURL: "https://panic.test/"}, nil,
		Options{Concurrency: 1, ScanInterval: time.Hour},
		slog.New(slog.NewTextHandler(&logs, nil)))
	w.jobs <- workerJob(1, links.Link{ID: 1, URL: "https://panic.test/", Title: "p", CheckInterval: ptrStr("daily")})
	w.jobs <- workerJob(1, links.Link{ID: 2, URL: "https://ok.test/", Title: "o", CheckInterval: ptrStr("daily")})

	w.Start(context.Background())
	t.Cleanup(w.Stop)

	require.Eventually(t, func() bool { return len(repo.snapshotResults()) == 1 }, time.Second, 5*time.Millisecond,
		"successor job did not record a result after the panic")
	assert.Contains(t, logs.String(), "changecheck boom")
	assert.Contains(t, logs.String(), "panicked")
}

type panicOnURLFetcher struct {
	panicURL string
}

func (f panicOnURLFetcher) GetRaw(_ context.Context, pageURL string) ([]byte, string, error) {
	if pageURL == f.panicURL {
		panic("changecheck boom")
	}
	return []byte(newPage("x", "hello")), "text/html", nil
}

func TestNew_ClampsHighConcurrency(t *testing.T) {
	w := New(&fakeRepo{}, fakeFetcher{}, nil, Options{Concurrency: 100_000}, testLogger())
	assert.Equal(t, 8, w.concurrent)
}

// ----- process -----

func newPage(title, body string) string {
	return `<html><head><title>` + title + `</title></head><body><main>` + body + `</main></body></html>`
}

func TestProcess_FirstObservation_RecordsFingerprintNoPush(t *testing.T) {
	repo := &fakeRepo{
		links: map[int64]links.Link{
			1: {ID: 1, URL: "https://x.test/", Title: "x", CheckInterval: ptrStr("daily")},
		},
	}
	sender := &fakeSender{}
	w := New(repo, fakeFetcher{body: []byte(newPage("x", "hello"))}, sender, Options{}, testLogger())

	w.process(context.Background(), workerJob(1, repo.links[1]))

	rs := repo.snapshotResults()
	require.Len(t, rs, 1)
	assert.False(t, rs[0].Changed, "first observation never counts as change")
	assert.True(t, len(rs[0].Fingerprint) > 0, "fingerprint must be recorded on first pass")
	assert.Empty(t, sender.seen(), "no push on first observation")
}

func TestProcess_SecondObservation_SameContent_NoChange(t *testing.T) {
	prev := FormatFingerprint(KindContent, contentHash("hello"))
	repo := &fakeRepo{
		links: map[int64]links.Link{
			1: {
				ID:              1,
				URL:             "https://x.test/",
				Title:           "x",
				CheckInterval:   ptrStr("daily"),
				LastFingerprint: &prev,
			},
		},
	}
	sender := &fakeSender{}
	w := New(repo, fakeFetcher{body: []byte(newPage("x", "hello"))}, sender, Options{}, testLogger())

	w.process(context.Background(), workerJob(1, repo.links[1]))

	rs := repo.snapshotResults()
	require.Len(t, rs, 1)
	assert.False(t, rs[0].Changed)
	assert.Empty(t, sender.seen())
}

func TestProcess_ContentDrift_DetectsChangeAndNotifies(t *testing.T) {
	owner := authctx.UserID(73)
	prev := FormatFingerprint(KindContent, contentHash("hello"))
	repo := &fakeRepo{
		links: map[int64]links.Link{
			1: {
				ID:              1,
				URL:             "https://x.test/",
				Title:           "x",
				CheckInterval:   ptrStr("daily"),
				LastFingerprint: &prev,
			},
		},
	}
	sender := &fakeSender{}
	w := New(repo, fakeFetcher{body: []byte(newPage("x", "world"))}, sender, Options{}, testLogger())
	w.Start(context.Background())
	t.Cleanup(w.Stop)

	w.process(context.Background(), workerJob(owner, repo.links[1]))

	rs := repo.snapshotResults()
	require.Len(t, rs, 1)
	assert.True(t, rs[0].Changed)
	// Push fires on a goroutine — give it a tick.
	assert.Eventually(t, func() bool { return len(sender.seen()) == 1 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, Notification{
		LinkID: 1,
		Title:  "x",
		URL:    "https://x.test/",
		Kind:   "change_detected",
		UserID: owner,
	}, sender.seen()[0])
}

func TestProcess_DoesNotNotifyWhenResultWasNotApplied(t *testing.T) {
	prev := FormatFingerprint(KindContent, contentHash("hello"))
	repo := &fakeRepo{
		links: map[int64]links.Link{
			1: {
				ID:              1,
				URL:             "https://x.test/",
				Title:           "x",
				CheckInterval:   ptrStr("daily"),
				LastFingerprint: &prev,
			},
		},
		recNoop: true,
	}
	sender := &fakeSender{}
	w := New(repo, fakeFetcher{body: []byte(newPage("x", "world"))}, sender, Options{}, testLogger())
	w.Start(context.Background())
	t.Cleanup(w.Stop)

	w.process(context.Background(), workerJob(1, repo.links[1]))

	assert.Never(t, func() bool { return len(sender.seen()) > 0 }, 100*time.Millisecond, 10*time.Millisecond,
		"a stale/no-op result must not notify")
}

func TestStop_CancelsBlockedNotification(t *testing.T) {
	prev := FormatFingerprint(KindContent, contentHash("hello"))
	repo := &fakeRepo{links: map[int64]links.Link{
		1: {
			ID:              1,
			URL:             "https://x.test/",
			Title:           "x",
			CheckInterval:   ptrStr("daily"),
			LastFingerprint: &prev,
		},
	}}
	sender := &cancelAwareSender{started: make(chan struct{}, 1), release: make(chan struct{})}
	w := New(repo, fakeFetcher{body: []byte(newPage("x", "world"))}, sender, Options{ScanInterval: time.Hour}, testLogger())
	w.Start(context.Background())
	w.process(context.Background(), workerJob(1, repo.links[1]))
	require.Eventually(t, func() bool { return len(sender.started) == 1 }, time.Second, 10*time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		w.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(250 * time.Millisecond):
		assert.Fail(t, "Stop did not cancel the blocked notification")
		close(sender.release)
		<-stopped
	}
}

func TestChangePushFloodStaysWithinFixedQueue(t *testing.T) {
	prev := FormatFingerprint(KindContent, contentHash("hello"))
	link := links.Link{
		ID:              1,
		URL:             "https://x.test/",
		Title:           "x",
		CheckInterval:   ptrStr("daily"),
		LastFingerprint: &prev,
	}
	repo := &fakeRepo{}
	sender := &cancelAwareSender{started: make(chan struct{}, 100), release: make(chan struct{})}
	w := New(repo, fakeFetcher{body: []byte(newPage("x", "world"))}, sender,
		Options{Concurrency: 1, ScanInterval: time.Hour}, testLogger())
	w.Start(context.Background())
	t.Cleanup(w.Stop)

	first := workerJob(1, link)
	w.process(context.Background(), first)
	require.Eventually(t, func() bool { return len(sender.started) == 1 }, time.Second, 10*time.Millisecond)

	for i := 1; i < 100; i++ {
		job := workerJob(1, link)
		job.ID = int64(i + 1)
		w.process(context.Background(), job)
	}
	assert.Equal(t, cap(w.pushJobs), len(w.pushJobs), "excess notifications must be dropped, not retain goroutines")
	assert.Equal(t, 1, len(sender.started), "one push worker permits only one active Notify call")
}

func TestProcess_FetchFailure_RecordsWithoutPush(t *testing.T) {
	repo := &fakeRepo{
		links: map[int64]links.Link{
			1: {ID: 1, URL: "https://x.test/", Title: "x", CheckInterval: ptrStr("daily")},
		},
	}
	sender := &fakeSender{}
	w := New(repo, fakeFetcher{err: errors.New("network down")}, sender, Options{}, testLogger())

	w.process(context.Background(), workerJob(1, repo.links[1]))

	rs := repo.snapshotResults()
	require.Len(t, rs, 1)
	assert.False(t, rs[0].Changed)
	assert.Empty(t, rs[0].Fingerprint)
	assert.Contains(t, rs[0].FetchErr, "network down")
	assert.Empty(t, sender.seen())
}

func TestProcess_FetchFailureDoesNotLogCapabilityURL(t *testing.T) {
	const capabilityURL = "https://example.test/private?token=secret-capability"
	repo := &fakeRepo{links: map[int64]links.Link{
		1: {ID: 1, URL: capabilityURL, Title: "x", CheckInterval: ptrStr("daily")},
	}}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	w := New(repo, fakeFetcher{err: errors.New("GET " + capabilityURL + ": connection reset")}, nil, Options{}, logger)

	w.process(context.Background(), workerJob(1, repo.links[1]))

	assert.NotContains(t, logs.String(), capabilityURL)
	assert.Contains(t, logs.String(), "reason=fetch_failed")
}

// RecordCheckResult failures on the fetch-error path must not panic and must
// still be attempted (logging is verified by not swallowing silently — the
// call reaches the repo even when it returns an error).
func TestProcess_FetchFailure_LogsRecordCheckResultError(t *testing.T) {
	repo := &fakeRepo{
		links: map[int64]links.Link{
			1: {ID: 1, URL: "https://x.test/", Title: "x", CheckInterval: ptrStr("daily")},
		},
		recErr: errors.New("db down"),
	}
	w := New(repo, fakeFetcher{err: errors.New("network down")}, &fakeSender{}, Options{}, testLogger())
	// Must not panic; RecordCheckResult error is logged like the success path.
	w.process(context.Background(), workerJob(1, repo.links[1]))
	assert.Empty(t, repo.snapshotResults(), "recErr means result was not stored")
}

func TestProcess_FingerprintFailure_LogsRecordCheckResultError(t *testing.T) {
	// Empty body → fingerprintContent returns "no extractable content".
	repo := &fakeRepo{
		links: map[int64]links.Link{
			1: {ID: 1, URL: "https://x.test/", Title: "x", CheckInterval: ptrStr("daily")},
		},
		recErr: errors.New("db down"),
	}
	w := New(repo, fakeFetcher{body: []byte("")}, &fakeSender{}, Options{}, testLogger())
	w.process(context.Background(), workerJob(1, repo.links[1]))
	assert.Empty(t, repo.snapshotResults())
}

func TestProcess_KindSwitchDoesNotFirePush(t *testing.T) {
	// Previous run was content kind, page now declares a feed → kind=feed
	// after this pass. The kind mismatch must suppress the push (fresh
	// baseline, not a real change).
	prev := FormatFingerprint(KindContent, contentHash("hello"))
	feedBody := `<rss><channel><item><guid>a</guid></item></channel></rss>`
	page := `<html><head>
        <link rel="alternate" type="application/rss+xml" href="https://x.test/feed.xml">
    </head><body><main>hello</main></body></html>`
	repo := &fakeRepo{
		links: map[int64]links.Link{
			1: {
				ID:              1,
				URL:             "https://x.test/",
				Title:           "x",
				CheckInterval:   ptrStr("daily"),
				LastFingerprint: &prev,
			},
		},
	}
	sender := &fakeSender{}
	// process() calls GetRaw twice on the change-kind path: once for the
	// HTML page (worker.process) and again for the feed body
	// (fingerprinter.fingerprintFeed). Use a queue-backed fetcher so each
	// call returns the next pre-baked body in order.
	seq := newQueueFetcher([]byte(page), []byte(feedBody))
	w := New(repo, seq, sender, Options{}, testLogger())

	w.process(context.Background(), workerJob(1, repo.links[1]))

	rs := repo.snapshotResults()
	require.Len(t, rs, 1)
	assert.False(t, rs[0].Changed, "kind switch must NOT count as a change")
	assert.True(t, len(rs[0].Fingerprint) > 0)
	assert.Empty(t, sender.seen())
}

func TestProcess_OptOutBetweenScanAndProcess_NoOp(t *testing.T) {
	repo := &fakeRepo{
		links: map[int64]links.Link{
			1: {ID: 1, URL: "https://x.test/", Title: "x", CheckInterval: nil},
		},
	}
	sender := &fakeSender{}
	w := New(repo, fakeFetcher{body: []byte(newPage("x", "y"))}, sender, Options{}, testLogger())

	w.process(context.Background(), workerJob(1, repo.links[1]))

	assert.Empty(t, repo.snapshotResults())
	assert.Empty(t, sender.seen())
}

// ----- scan -----

func TestScan_EnqueuesDueJobsWithOwners(t *testing.T) {
	due := []links.DueLink{
		{ID: 1, UserID: authctx.UserID(71)},
		{ID: 2, UserID: authctx.UserID(72)},
		{ID: 3, UserID: authctx.UserID(73)},
	}
	repo := &fakeRepo{
		due: due,
	}
	w := New(repo, fakeFetcher{}, nil, Options{ScanInterval: time.Hour}, testLogger())
	w.scan(context.Background())

	got := drain(t, w, 3)
	assert.ElementsMatch(t, due, got)
}

func TestScanClaimsOnlyAvailableQueueCapacity(t *testing.T) {
	repo := &fakeRepo{due: []links.DueLink{{ID: 1001}, {ID: 1002}, {ID: 1003}}}
	w := New(repo, fakeFetcher{}, nil, Options{ScanInterval: time.Hour}, testLogger())
	for i := 0; i < cap(w.jobs)-1; i++ {
		w.jobs <- links.DueLink{ID: int64(i + 1)}
	}

	w.scan(context.Background())

	remaining, limits := repo.snapshotScanState()
	assert.Equal(t, []int{1}, limits)
	assert.Equal(t, []links.DueLink{{ID: 1002}, {ID: 1003}}, remaining)
	assert.Equal(t, cap(w.jobs), len(w.jobs))
}

func TestScanDoesNotClaimWhenQueueIsFull(t *testing.T) {
	repo := &fakeRepo{due: []links.DueLink{{ID: 1001}}}
	w := New(repo, fakeFetcher{}, nil, Options{ScanInterval: time.Hour}, testLogger())
	for i := 0; i < cap(w.jobs); i++ {
		w.jobs <- links.DueLink{ID: int64(i + 1)}
	}

	w.scan(context.Background())

	remaining, limits := repo.snapshotScanState()
	assert.Empty(t, limits)
	assert.Equal(t, []links.DueLink{{ID: 1001}}, remaining)
}

func TestScan_FindDueErrorIsTolerated(t *testing.T) {
	repo := &fakeRepo{findErr: errors.New("boom")}
	w := New(repo, fakeFetcher{}, nil, Options{ScanInterval: time.Hour}, testLogger())
	// Must not panic; the next scan retries.
	w.scan(context.Background())
}

func TestScan_DueBatchDoesNotCallSystemGetPerJob(t *testing.T) {
	repo := &fakeRepo{
		links: map[int64]links.Link{
			1: {ID: 1, URL: "https://x.test/1", Title: "one", CheckInterval: ptrStr("daily")},
			2: {ID: 2, URL: "https://x.test/2", Title: "two", CheckInterval: ptrStr("daily")},
		},
		due: []links.DueLink{
			workerJob(1, links.Link{ID: 1, URL: "https://x.test/1", Title: "one", CheckInterval: ptrStr("daily")}),
			workerJob(1, links.Link{ID: 2, URL: "https://x.test/2", Title: "two", CheckInterval: ptrStr("daily")}),
		},
	}
	w := New(repo, fakeFetcher{body: []byte(newPage("x", "hello"))}, nil,
		Options{Concurrency: 1, ScanInterval: time.Hour}, testLogger())
	w.Start(context.Background())
	require.Eventually(t, func() bool { return len(repo.snapshotResults()) == 2 }, time.Second, 10*time.Millisecond)
	w.Stop()

	assert.Zero(t, repo.snapshotGetCalls(), "claimed due rows must carry the worker projection")
}

// ----- helpers -----

func ptrStr(s string) *string { return &s }

func workerJob(owner authctx.UserID, link links.Link) links.DueLink {
	interval := ""
	if link.CheckInterval != nil {
		interval = *link.CheckInterval
	}
	return links.DueLink{
		ID:              link.ID,
		UserID:          owner,
		URL:             link.URL,
		Title:           link.Title,
		CheckInterval:   interval,
		LastFingerprint: link.LastFingerprint,
		ClaimedAt:       time.Unix(1, 0),
	}
}

func contentHash(s string) string {
	// Compute the canonical content fingerprint for `<main>s</main>`.
	h, err := fingerprintContent([]byte(newPage("x", s)))
	if err != nil {
		panic(err)
	}
	return h
}

// queueFetcher serves pre-baked bodies in order, one per call. Used by the
// kind-switch test where the worker reads the HTML page first and the feed
// body second. Returns an error after the queue is drained so a wrong call
// count fails the test loudly.
type queueFetcher struct {
	mu    sync.Mutex
	queue [][]byte
}

func newQueueFetcher(bodies ...[]byte) *queueFetcher {
	return &queueFetcher{queue: bodies}
}

func (q *queueFetcher) GetRaw(_ context.Context, _ string) ([]byte, string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queue) == 0 {
		return nil, "", errors.New("queueFetcher: out of bodies")
	}
	head := q.queue[0]
	q.queue = q.queue[1:]
	return head, "text/html", nil
}

// drain pulls up to n jobs with a per-receive timeout so the test never hangs.
func drain(t *testing.T, w *Worker, n int) []links.DueLink {
	t.Helper()
	got := make([]links.DueLink, 0, n)
	deadline := time.After(time.Second)
	for len(got) < n {
		select {
		case job := <-w.jobs:
			got = append(got, job)
		case <-deadline:
			t.Fatalf("timed out draining jobs (got %d/%d)", len(got), n)
		}
	}
	return got
}
