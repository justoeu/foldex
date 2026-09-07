package ipblock

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Blocklist is the enforcement-side cache.
//
// A snapshot refreshed on a TTL rather than a query per request: this sits in
// front of EVERY request, and a database round trip there would make the
// blocklist a latency tax on the whole instance whether or not anyone is
// blocked.
//
// It fails OPEN. When the refresh errors the previous snapshot is kept and, if
// there is none, nobody is blocked. That is the deliberate inversion of the
// usual rule, and the reason is the same one behind every rail in
// ValidateBlockIP: this is a nuisance filter, not an authentication boundary —
// the session middleware is what actually decides who may do anything. Failing
// closed would turn a transient database blip into a total outage, and the
// people locked out would include the ones who could fix it.
type Blocklist struct {
	load func(context.Context) ([]string, error)
	ttl  time.Duration
	snap atomic.Pointer[map[string]struct{}]
	exp  atomic.Int64
	mu   sync.Mutex
}

// BlocklistTTL is how stale enforcement may be. Short enough that a block takes
// effect while the person who installed it is still watching, long enough that
// the query is rare. Invalidate makes a write immediate regardless.
const BlocklistTTL = 30 * time.Second

func NewBlocklist(load func(context.Context) ([]string, error)) *Blocklist {
	return &Blocklist{load: load, ttl: BlocklistTTL}
}

// Invalidate forces the next lookup to reload, so a block installed through the
// API is enforced on the following request rather than up to a TTL later.
func (b *Blocklist) Invalidate() { b.exp.Store(0) }

// Blocked reports whether the address is on the list.
func (b *Blocklist) Blocked(ctx context.Context, ip string) bool {
	if ip == "" {
		return false
	}
	b.refresh(ctx)
	snap := b.snap.Load()
	if snap == nil {
		return false
	}
	_, found := (*snap)[ip]
	return found
}

func (b *Blocklist) refresh(ctx context.Context) {
	if time.Now().UnixNano() < b.exp.Load() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Now().UnixNano() < b.exp.Load() {
		return
	}
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), blocklistLoadTimeout)
	defer cancel()
	ips, err := b.load(loadCtx)
	if err != nil {
		b.exp.Store(time.Now().Add(b.ttl).UnixNano())
		return
	}
	next := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		next[ip] = struct{}{}
	}
	b.snap.Store(&next)
	b.exp.Store(time.Now().Add(b.ttl).UnixNano())
}

const blocklistLoadTimeout = 3 * time.Second
