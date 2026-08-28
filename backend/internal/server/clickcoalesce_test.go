package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/abusepolicy"
	"foldex/internal/notes"
	"foldex/internal/pkg/clickctx"
	"foldex/internal/redirect"
)

// countingResolver stands in for links.Repository on the /go path. It consults
// the gate exactly where the repository does — inside the resolve, with the
// entity id already known — and records both what it resolved and what it
// would have written.
type countingResolver struct {
	mu      sync.Mutex
	clicks  int
	resolve int
}

func (c *countingResolver) ClickAndResolve(ctx context.Context, id int64) (string, error) {
	return c.record(ctx, id)
}

func (c *countingResolver) ClickAndResolveBySlug(ctx context.Context, _ string) (string, error) {
	return c.record(ctx, 7)
}

func (c *countingResolver) record(ctx context.Context, id int64) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resolve++
	if clickctx.Allow(ctx, "link", id) {
		c.clicks++
	}
	return "https://example.com/dest", nil
}

func (c *countingResolver) counts() (clicks, resolves int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clicks, c.resolve
}

type countingNoteResolver struct {
	mu     sync.Mutex
	clicks int
}

func (c *countingNoteResolver) SystemViewAndResolveByID(ctx context.Context, id int64) (notes.Note, error) {
	return c.record(ctx, id)
}

func (c *countingNoteResolver) SystemViewAndResolveBySlug(ctx context.Context, _ string) (notes.Note, error) {
	return c.record(ctx, 3)
}

func (c *countingNoteResolver) record(ctx context.Context, id int64) (notes.Note, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if clickctx.Allow(ctx, "note", id) {
		c.clicks++
	}
	return notes.Note{Title: "n", BodyHTML: "<p>b</p>"}, nil
}

func (c *countingNoteResolver) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clicks
}

func coalescingRouter(pol policyReader, link redirect.LinkResolver, note notes.PublicNoteResolver) http.Handler {
	r := chi.NewRouter()
	r.Use(newClickCoalescer(pol).middleware)
	redirect.NewHandler(link, true).Mount(r)
	notes.NewPublicHandler(note, true).Mount(r)
	return r
}

func visit(h http.Handler, path, ip string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = ip + ":51234"
	h.ServeHTTP(rec, req)
	return rec
}

func coalescePolicy(seconds int) fixedPolicy {
	p := abusepolicy.Default()
	p.PublicClickCoalesceSeconds = &seconds
	return fixedPolicy{p: p}
}

// 8. The same visitor hitting the same link twice inside the window writes one
// row — and gets redirected BOTH times. Coalescing suppresses the WRITE, never
// the resolution: getting this wrong breaks every shared link on the instance
// for every visitor, which is a far worse outcome than an over-counted click.
func TestClickCoalesce_ARepeatVisitWritesOneRowAndStillRedirects(t *testing.T) {
	t.Parallel()
	repo := &countingResolver{}
	h := coalescingRouter(coalescePolicy(10), repo, &countingNoteResolver{})

	for i := 0; i < 5; i++ {
		rec := visit(h, "/go/release-notes", "203.0.113.9")
		require.Equal(t, http.StatusFound, rec.Code, "visit %d must still redirect", i+1)
		require.Equal(t, "https://example.com/dest", rec.Header().Get("Location"))
	}

	clicks, resolves := repo.counts()
	assert.Equal(t, 1, clicks, "five hits from one visitor in ten seconds must be one click row")
	assert.Equal(t, 5, resolves, "the redirect must never be suppressed")
}

// The note surface is the same amplifier with a different verb, and it must
// coalesce on its own key: a visitor reading a note must not suppress a click
// on the link that happens to share the id.
func TestClickCoalesce_NotesCoalesceSeparatelyFromLinks(t *testing.T) {
	t.Parallel()
	links := &countingResolver{}
	nts := &countingNoteResolver{}
	h := coalescingRouter(coalescePolicy(10), links, nts)

	require.Equal(t, http.StatusOK, visit(h, "/n/some-note", "198.51.100.4").Code)
	require.Equal(t, http.StatusOK, visit(h, "/n/some-note", "198.51.100.4").Code)
	require.Equal(t, http.StatusFound, visit(h, "/go/some-link", "198.51.100.4").Code)

	assert.Equal(t, 1, nts.count(), "the repeat note view must be coalesced")
	clicks, _ := links.counts()
	assert.Equal(t, 1, clicks, "the link click must not be suppressed by the note's entry")
}

// 9. Two different visitors on the same entity are two clicks. The coalescer
// must be keyed by visitor, not by entity — otherwise a popular link stops
// counting anybody after the first person.
func TestClickCoalesce_DifferentVisitorsEachWriteARow(t *testing.T) {
	t.Parallel()
	repo := &countingResolver{}
	h := coalescingRouter(coalescePolicy(10), repo, &countingNoteResolver{})

	require.Equal(t, http.StatusFound, visit(h, "/go/release-notes", "203.0.113.1").Code)
	require.Equal(t, http.StatusFound, visit(h, "/go/release-notes", "203.0.113.2").Code)

	clicks, _ := repo.counts()
	assert.Equal(t, 2, clicks)
}

// 10. Zero means OFF, and off must be genuinely off: the operator who turns it
// off is buying back an exact click counter, and a residual window would make
// the setting a lie.
func TestClickCoalesce_ZeroSecondsDisablesCoalescing(t *testing.T) {
	t.Parallel()
	repo := &countingResolver{}
	h := coalescingRouter(coalescePolicy(0), repo, &countingNoteResolver{})

	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusFound, visit(h, "/go/release-notes", "203.0.113.9").Code)
	}

	clicks, _ := repo.counts()
	assert.Equal(t, 3, clicks, "with the window at 0 every hit must be recorded")
}

// Past the window the visitor counts again — the suppression is a window, not
// a permanent mute.
func TestClickCoalesce_TheWindowExpires(t *testing.T) {
	t.Parallel()
	c := newTestClock()
	repo := &countingResolver{}
	cc := newClickCoalescer(coalescePolicy(10))
	cc.now = c.now

	r := chi.NewRouter()
	r.Use(cc.middleware)
	redirect.NewHandler(repo, true).Mount(r)

	require.Equal(t, http.StatusFound, visit(r, "/go/release-notes", "203.0.113.9").Code)
	c.advance(9 * time.Second)
	require.Equal(t, http.StatusFound, visit(r, "/go/release-notes", "203.0.113.9").Code)
	c.advance(2 * time.Second)
	require.Equal(t, http.StatusFound, visit(r, "/go/release-notes", "203.0.113.9").Code)

	clicks, _ := repo.counts()
	assert.Equal(t, 2, clicks, "the second visit is inside the window, the third is past it")
}

// The dedup state must never hold the visitor's address in the clear. It lives
// in shared process memory that no privacy decision covers — persisting or
// exposing a visitor IP on the public path is a call this change does not get
// to make, so the key is a keyed digest and the raw address is never stored.
func TestClickCoalesce_TheVisitorKeyIsAKeyedDigestNotTheAddress(t *testing.T) {
	t.Parallel()
	cc := newClickCoalescer(coalescePolicy(10))
	cc.allow("link", 1, cc.visitor("203.0.113.9"), 10*time.Second)

	require.Equal(t, 1, cc.len())
	for k := range cc.seen {
		assert.NotContains(t, string(k.visitor[:]), "203.0.113.9",
			"the raw address must not be reachable from the dedup key")
	}

	// The key is per process, so the same address is a different digest in a
	// different process: the map is not a lookup table anyone can precompute.
	other := newClickCoalescer(coalescePolicy(10))
	assert.NotEqual(t, cc.visitor("203.0.113.9"), other.visitor("203.0.113.9"))
}

// "::ffff:203.0.113.9" and "203.0.113.9" are the same visitor. Without
// normalisation they are two budgets, which is a free way to double every
// counter — the same defect NormalizeIP already fixes for the blocklist.
func TestClickCoalesce_NormalisesTheAddressBeforeHashing(t *testing.T) {
	t.Parallel()
	repo := &countingResolver{}
	h := coalescingRouter(coalescePolicy(10), repo, &countingNoteResolver{})

	require.Equal(t, http.StatusFound, visit(h, "/go/release-notes", "203.0.113.9").Code)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/go/release-notes", nil)
	req.RemoteAddr = "[::ffff:203.0.113.9]:4444"
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusFound, rec.Code)

	clicks, _ := repo.counts()
	assert.Equal(t, 1, clicks, "the IPv4-mapped form is the same visitor")
}

// 7 (public half). Without a ceiling the coalescer is simply the next thing to
// fill: an attacker stops writing rows to click_log and starts writing entries
// to this map instead.
func TestClickCoalesce_TheMapNeverExceedsItsCeiling(t *testing.T) {
	t.Parallel()
	cc := newClickCoalescer(coalescePolicy(10))
	cc.maxKeys = 64

	for i := 0; i < 10_000; i++ {
		cc.allow("link", int64(i), cc.visitor(fmt.Sprintf("198.51.100.%d", i%256)), 10*time.Second)
	}

	// A ceiling assertion alone passes at ZERO — deleting the insert entirely
	// would satisfy it, and a limiter that stores nothing limits nothing. The
	// floor is what makes the ceiling mean something.
	assert.GreaterOrEqual(t, cc.len(), 64-clickCoalesceEvictionBatch,
		"the map holds %d of a 64 ceiling; the visitor keys are collapsing, not evicting", cc.len())
	assert.LessOrEqual(t, cc.len(), 64, "the dedup map grew past its ceiling")
}

// Entries past the window are reclaimed without anyone calling a sweeper: the
// coalescer is mounted by the router and nothing else knows it exists.
func TestClickCoalesce_ReclaimsExpiredEntriesOnItsOwn(t *testing.T) {
	t.Parallel()
	c := newTestClock()
	cc := newClickCoalescer(coalescePolicy(10))
	cc.now = c.now
	cc.maxKeys = 500

	for i := 0; i < 400; i++ {
		cc.allow("link", int64(i), cc.visitor("203.0.113.7"), 10*time.Second)
	}
	require.Equal(t, 400, cc.len())

	c.advance(time.Minute)
	cc.allow("link", 9999, cc.visitor("203.0.113.7"), 10*time.Second)
	assert.Equal(t, 1, cc.len(), "expired entries should have been reclaimed")
}

// Concurrent visitors on one entity must still produce exactly one row: a
// check-then-act coalescer lets every racing goroutine read "not seen" and all
// of them write. Run with -race.
func TestClickCoalesce_ConcurrentHitsFromOneVisitorWriteOneRow(t *testing.T) {
	t.Parallel()
	cc := newClickCoalescer(coalescePolicy(10))
	v := cc.visitor("203.0.113.9")

	var allowed atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if cc.allow("link", 1, v, 10*time.Second) {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, 1, allowed.Load(),
		"200 concurrent hits from one visitor produced %d click rows", allowed.Load())
}

// Coalescing must fall back to the compiled default rather than switching
// itself off. Both nils are asserted for the reason spelled out in
// TestAPIQuota_ANilPolicySourceEnforcesTheCompiledDefaults: the typed nil that
// router.go passes is answered by Cache.Current's nil-receiver guard, and the
// local `c.pol != nil` check only ever fires for an absent reader.
func TestClickCoalesce_ANilPolicySourceUsesTheCompiledDefault(t *testing.T) {
	t.Parallel()
	want := abusepolicy.Default().ClickCoalesceSeconds()

	var cache *abusepolicy.Cache
	cc := newClickCoalescer(cache)
	require.True(t, cc.pol != nil, "a typed nil in an interface is not a nil interface")
	assert.Equal(t, want, int(cc.window(context.Background())/time.Second))

	var absent policyReader
	cc = newClickCoalescer(absent)
	require.True(t, cc.pol == nil)
	assert.Equal(t, want, int(cc.window(context.Background())/time.Second))
}

// An address the proxy chain left unresolvable must NOT be coalesced: hashing
// "" would put every such caller under one key, and the first of them would
// then suppress the click rows of all the others. A coalescer that loses other
// people's rows has corrupted the counter it exists to protect.
func TestClickCoalesce_AnUnresolvableAddressIsNotCoalesced(t *testing.T) {
	t.Parallel()
	repo := &countingResolver{}
	r := chi.NewRouter()
	r.Use(newClickCoalescer(coalescePolicy(10)).middleware)
	redirect.NewHandler(repo, true).Mount(r)

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/go/release-notes", nil)
		req.RemoteAddr = "not-an-address"
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusFound, rec.Code)
	}

	clicks, _ := repo.counts()
	assert.Equal(t, 3, clicks, "an unidentifiable visitor must record, never share a bucket")
}

// Eviction under ceiling pressure frees a BATCH. Freeing one slot would leave
// the next insert at the ceiling again, so the sampling scan would run on every
// insert for as long as the pressure lasts — which is exactly the period when
// it can least be afforded.
func TestClickCoalesce_EvictionFreesABatchNotASingleSlot(t *testing.T) {
	t.Parallel()
	c := newTestClock()
	cc := newClickCoalescer(coalescePolicy(10))
	cc.now = c.now
	cc.maxKeys = 100

	v := cc.visitor("203.0.113.7")
	for i := 0; i < 100; i++ {
		cc.allow("link", int64(i), v, 10*time.Second)
	}
	require.Equal(t, 100, cc.len(), "the map should be exactly at its ceiling")

	// Nothing is expired and the sweep already ran this window, so this insert
	// can only make room by evicting.
	cc.allow("link", 1000, v, 10*time.Second)
	assert.LessOrEqual(t, cc.len(), 100, "the ceiling must still hold")
	assert.LessOrEqual(t, cc.len(), 100-clickCoalesceEvictionBatch+1,
		"one insert past the ceiling should have freed a batch, not a single slot")
}
