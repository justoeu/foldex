// Package semaphore is the one admission counter for bounded concurrent
// work. The screenshot pool (one capture at a time, callers wait) and the
// links screenshot handler (a global cap plus a per-user cap, callers are
// refused) used to hand-roll the same channel-plus-map pair; both now share
// this implementation so the acquire/release pairing is written once.
package semaphore

import (
	"context"
	"errors"
	"sync"
)

// ErrAborted reports that Acquire stopped waiting because its abort channel
// closed — for the screenshot pool, that is pool shutdown. Callers map it to
// their own domain error.
var ErrAborted = errors.New("semaphore: acquire aborted")

// Semaphore bounds how many holders run at once.
type Semaphore struct {
	ch chan struct{}
}

// New returns a semaphore admitting n concurrent holders. n is clamped to
// at least 1: a zero-capacity semaphore would refuse every caller forever,
// which is never the intent at a call site.
func New(n int) *Semaphore {
	if n < 1 {
		n = 1
	}
	return &Semaphore{ch: make(chan struct{}, n)}
}

// Cap reports how many holders the semaphore admits.
func (s *Semaphore) Cap() int { return cap(s.ch) }

// TryAcquire takes a slot without waiting and reports whether it got one.
func (s *Semaphore) TryAcquire() bool {
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// Acquire waits for a slot until one frees, ctx ends, or abort closes. A nil
// abort channel never fires, matching a plain context wait. On ctx expiry
// the returned error is ctx.Err(); on abort it is ErrAborted.
func (s *Semaphore) Acquire(ctx context.Context, abort <-chan struct{}) error {
	select {
	case s.ch <- struct{}{}:
		return nil
	case <-abort:
		return ErrAborted
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release returns one slot. Releasing more than was acquired is a caller
// bug; it is ignored rather than blocking, so a mismatched release cannot
// wedge the holder's caller the way receiving from an empty channel would.
func (s *Semaphore) Release() {
	select {
	case <-s.ch:
	default:
	}
}

// Keyed bounds concurrency globally and per key at once — the links
// screenshot handler's shape: at most N captures in flight overall and at
// most M per user, with refusal (never waiting) past either cap.
type Keyed[K comparable] struct {
	mu     sync.Mutex
	global int
	perKey int
	inUse  int
	keys   map[K]int
}

// NewKeyed returns a keyed semaphore admitting global holders in total and
// perKey holders per key. Both caps are clamped to at least 1.
func NewKeyed[K comparable](global, perKey int) *Keyed[K] {
	if global < 1 {
		global = 1
	}
	if perKey < 1 {
		perKey = 1
	}
	return &Keyed[K]{global: global, perKey: perKey, keys: make(map[K]int)}
}

// TryAcquire takes one slot for key without waiting. The per-key cap is
// checked before the global one, as the hand-rolled counter did: a user
// already at their cap is refused even while global slots remain.
func (k *Keyed[K]) TryAcquire(key K) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys[key] >= k.perKey {
		return false
	}
	if k.inUse >= k.global {
		return false
	}
	k.keys[key]++
	k.inUse++
	return true
}

// Release returns one slot held by key. Releasing a key that holds nothing
// is ignored rather than driving the counters negative.
func (k *Keyed[K]) Release(key K) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys[key] <= 0 {
		return
	}
	if k.keys[key] == 1 {
		delete(k.keys, key)
	} else {
		k.keys[key]--
	}
	k.inUse--
}
