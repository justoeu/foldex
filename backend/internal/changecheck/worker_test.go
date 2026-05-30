package changecheck

import (
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
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ----- Mocks -----

type fakeRepo struct {
	mu       sync.Mutex
	links    map[int64]links.Link
	due      []int64
	results  []links.CheckResult
	findErr  error
	recErr   error
	recDelay time.Duration
}

func (r *fakeRepo) Get(_ context.Context, id int64) (links.Link, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.links[id]
	if !ok {
		return links.Link{}, errors.New("not found")
	}
	return l, nil
}

func (r *fakeRepo) FindDueForCheck(_ context.Context, _ int) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.findErr != nil {
		return nil, r.findErr
	}
	out := append([]int64(nil), r.due...)
	r.due = nil
	return out, nil
}

func (r *fakeRepo) RecordCheckResult(_ context.Context, _ int64, res links.CheckResult) error {
	if r.recDelay > 0 {
		time.Sleep(r.recDelay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recErr != nil {
		return r.recErr
	}
	r.results = append(r.results, res)
	return nil
}

func (r *fakeRepo) snapshotResults() []links.CheckResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]links.CheckResult(nil), r.results...)
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

func TestEnqueue_ReturnsErrStoppedAfterStop(t *testing.T) {
	w := New(&fakeRepo{}, fakeFetcher{}, nil, Options{ScanInterval: time.Hour}, testLogger())
	w.Start(context.Background())
	w.Stop()
	err := w.Enqueue(1)
	assert.ErrorIs(t, err, ErrStopped)
}

func TestEnqueue_ReturnsErrQueueFullWhenSaturated(t *testing.T) {
	// Don't Start — without consumers nothing drains the channel and we can
	// pin it to capacity.
	w := New(&fakeRepo{}, fakeFetcher{}, nil, Options{ScanInterval: time.Hour}, testLogger())
	for i := 0; i < cap(w.jobs); i++ {
		require.NoError(t, w.Enqueue(int64(i)))
	}
	err := w.Enqueue(99999)
	assert.ErrorIs(t, err, ErrQueueFull)
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

	w.process(context.Background(), 1)

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

	w.process(context.Background(), 1)

	rs := repo.snapshotResults()
	require.Len(t, rs, 1)
	assert.False(t, rs[0].Changed)
	assert.Empty(t, sender.seen())
}

func TestProcess_ContentDrift_DetectsChangeAndNotifies(t *testing.T) {
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

	w.process(context.Background(), 1)

	rs := repo.snapshotResults()
	require.Len(t, rs, 1)
	assert.True(t, rs[0].Changed)
	// Push fires on a goroutine — give it a tick.
	assert.Eventually(t, func() bool { return len(sender.seen()) == 1 }, time.Second, 10*time.Millisecond)
	got := sender.seen()[0]
	assert.Equal(t, int64(1), got.LinkID)
	assert.Equal(t, "change_detected", got.Kind)
}

func TestProcess_FetchFailure_RecordsWithoutPush(t *testing.T) {
	repo := &fakeRepo{
		links: map[int64]links.Link{
			1: {ID: 1, URL: "https://x.test/", Title: "x", CheckInterval: ptrStr("daily")},
		},
	}
	sender := &fakeSender{}
	w := New(repo, fakeFetcher{err: errors.New("network down")}, sender, Options{}, testLogger())

	w.process(context.Background(), 1)

	rs := repo.snapshotResults()
	require.Len(t, rs, 1)
	assert.False(t, rs[0].Changed)
	assert.Empty(t, rs[0].Fingerprint)
	assert.Contains(t, rs[0].FetchErr, "network down")
	assert.Empty(t, sender.seen())
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

	w.process(context.Background(), 1)

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

	w.process(context.Background(), 1)

	assert.Empty(t, repo.snapshotResults())
	assert.Empty(t, sender.seen())
}

// ----- scan -----

func TestScan_EnqueuesDueIDs(t *testing.T) {
	repo := &fakeRepo{
		due: []int64{1, 2, 3},
	}
	w := New(repo, fakeFetcher{}, nil, Options{ScanInterval: time.Hour}, testLogger())
	w.scan(context.Background())

	// Drain to verify they landed in the channel.
	got := drain(t, w, 3)
	assert.ElementsMatch(t, []int64{1, 2, 3}, got)
}

func TestScan_FindDueErrorIsTolerated(t *testing.T) {
	repo := &fakeRepo{findErr: errors.New("boom")}
	w := New(repo, fakeFetcher{}, nil, Options{ScanInterval: time.Hour}, testLogger())
	// Must not panic; the next scan retries.
	w.scan(context.Background())
}

// ----- helpers -----

func ptrStr(s string) *string { return &s }

func contentHash(s string) string {
	// Compute the canonical content fingerprint for `<main>s</main>`.
	h, err := fingerprintContent(newPage("x", s))
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

// drain pulls up to n IDs from the worker's jobs channel with a per-recv
// timeout so the test never hangs.
func drain(t *testing.T, w *Worker, n int) []int64 {
	t.Helper()
	got := make([]int64, 0, n)
	deadline := time.After(time.Second)
	for len(got) < n {
		select {
		case id := <-w.jobs:
			got = append(got, id)
		case <-deadline:
			t.Fatalf("timed out draining jobs (got %d/%d)", len(got), n)
		}
	}
	return got
}

