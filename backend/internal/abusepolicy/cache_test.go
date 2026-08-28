package abusepolicy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stubReader struct {
	mu    sync.Mutex
	calls atomic.Int32
	p     Policy
	err   error
}

func (s *stubReader) Get(context.Context) (Policy, error) {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.p, s.err
}

func (s *stubReader) set(p Policy, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p, s.err = p, err
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCache_ServesTheStoredPolicy(t *testing.T) {
	want := Default()
	want.LoginDistinctAccountsPerIP = 42
	r := &stubReader{p: want}
	c := NewCache(r, time.Minute, quietLog())

	if got := c.Current(context.Background()).LoginDistinctAccountsPerIP; got != 42 {
		t.Fatalf("want 42, got %d", got)
	}
}

func TestCache_SanitizesWhatItServes(t *testing.T) {
	// The enforcement sites read this value and use it directly as a ceiling.
	// A store holding a nonsense number — hand-edited row, older binary — must
	// not reach a limiter, or the "configurable" surface becomes a way to
	// disable the defence by writing JSON.
	bad := Default()
	bad.LoginFailuresPerAccount = 1000000
	c := NewCache(&stubReader{p: bad}, time.Minute, quietLog())

	got := c.Current(context.Background()).LoginFailuresPerAccount
	if got != Default().LoginFailuresPerAccount {
		t.Fatalf("an out-of-range stored value must be sanitized before enforcement, got %d", got)
	}
}

func TestCache_FailStaticKeepsTheLastGoodPolicy(t *testing.T) {
	// The posture that matters: a database blip must not decide how many login
	// attempts an origin gets. It keeps enforcing the previous numbers.
	good := Default()
	good.LoginDistinctAccountsPerIP = 7
	r := &stubReader{p: good}

	now := time.Now()
	c := NewCache(r, time.Minute, quietLog()).WithClock(func() time.Time { return now })
	if got := c.Current(context.Background()).LoginDistinctAccountsPerIP; got != 7 {
		t.Fatalf("setup: want 7, got %d", got)
	}

	r.set(Policy{}, errors.New("database is down"))
	now = now.Add(2 * time.Minute) // past the TTL, so a reload is attempted

	if got := c.Current(context.Background()).LoginDistinctAccountsPerIP; got != 7 {
		t.Fatalf("a failed load must keep the previous policy, got %d", got)
	}
}

func TestCache_FallsBackToDefaultsBeforeAnySuccessfulLoad(t *testing.T) {
	// An instance whose store has never answered still has to enforce
	// something, and the compiled defaults are the only safe answer available.
	c := NewCache(&stubReader{err: errors.New("boom")}, time.Minute, quietLog())
	if got := c.Current(context.Background()); !sameAs(got, Default()) {
		t.Fatalf("want the compiled defaults, got %s", render(got))
	}
}

func TestCache_DoesNotQueryOncePerRequest(t *testing.T) {
	// The whole point of the TTL. Without it the login path issues a database
	// round-trip per unauthenticated attempt — a cheaper way to load the pool
	// than the thing being rate limited, arriving through the defence itself.
	r := &stubReader{p: Default()}
	now := time.Now()
	c := NewCache(r, time.Minute, quietLog()).WithClock(func() time.Time { return now })

	for i := 0; i < 50; i++ {
		c.Current(context.Background())
	}
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("50 reads inside the TTL must cost 1 load, cost %d", n)
	}

	now = now.Add(61 * time.Second)
	c.Current(context.Background())
	if n := r.calls.Load(); n != 2 {
		t.Fatalf("a read past the TTL must reload, calls=%d", n)
	}
}

func TestCache_FailedLoadBacksOffForTheFullTTL(t *testing.T) {
	// A store that is down must not receive one failing query per request. The
	// amplification would be inside the defence.
	r := &stubReader{err: errors.New("down")}
	now := time.Now()
	c := NewCache(r, time.Minute, quietLog()).WithClock(func() time.Time { return now })

	for i := 0; i < 20; i++ {
		c.Current(context.Background())
	}
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("a failed load must back off for the TTL; got %d attempts", n)
	}
}

func TestCache_InvalidateTakesEffectImmediately(t *testing.T) {
	// The owner just saved. Waiting out the TTL before the new limit applies
	// reads as a bug, and would send them looking for one.
	r := &stubReader{p: Default()}
	now := time.Now()
	c := NewCache(r, time.Minute, quietLog()).WithClock(func() time.Time { return now })
	c.Current(context.Background())

	next := Default()
	next.LoginDistinctAccountsPerIP = 33
	r.set(next, nil)
	c.Invalidate()

	if got := c.Current(context.Background()).LoginDistinctAccountsPerIP; got != 33 {
		t.Fatalf("Invalidate must force a reload, got %d", got)
	}
}

func TestCache_CancelledRequestDoesNotCancelTheRefresh(t *testing.T) {
	// The caller's context belongs to one request that may be abandoned at any
	// moment. Letting it cancel the refresh would leave a busy instance never
	// updating its own policy while looking like it keeps trying.
	r := &stubReader{p: Default()}
	c := NewCache(r, time.Minute, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got := c.Current(ctx); !sameAs(got, Default()) {
		t.Fatalf("unexpected policy: %s", render(got))
	}
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("the refresh must run despite the cancelled caller, calls=%d", n)
	}
}

func TestCache_NilIsUsable(t *testing.T) {
	// A nil cache is what a test harness or a partially wired binary holds.
	// Panicking here would take down the login path rather than the setting.
	var c *Cache
	if got := c.Current(context.Background()); !sameAs(got, Default()) {
		t.Fatalf("a nil cache must answer the defaults, got %s", render(got))
	}
	c.Invalidate() // must not panic
}

func TestCache_ConcurrentReadsAgreeAndDoNotRace(t *testing.T) {
	// Run under -race. The hot path reads exp without the mutex on purpose;
	// this is the test that says that choice is safe.
	want := Default()
	want.APIWritesPerMinute = 300
	c := NewCache(&stubReader{p: want}, 10*time.Millisecond, quietLog())

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if got := c.Current(context.Background()).APIWritesPerMinute; got != 300 {
					t.Errorf("concurrent read saw %d", got)
					return
				}
			}
		}()
	}
	wg.Wait()
}
