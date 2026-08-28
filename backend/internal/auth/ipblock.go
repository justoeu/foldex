package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"foldex/internal/pkg/authctx"
)

// IPBlock is one permanent blocklist entry — ADR-46.
type IPBlock struct {
	ID        int64     `json:"id"`
	IP        string    `json:"ip"`
	Reason    *string   `json:"reason"`
	CreatedBy *string   `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Errors the block path returns. Each names a rail the caller tripped, because
// "invalid" alone in front of a control that can lock the operator out of their
// own instance is not an answer anyone can act on.
var (
	ErrBlockMalformed = errors.New("not an ip address")
	ErrBlockSelf      = errors.New("that is the address you are connected from")
	ErrBlockLoopback  = errors.New("loopback is how the instance is administered locally")
	ErrBlockProxy     = errors.New("that address is a configured trusted proxy")
	ErrBlockFull      = errors.New("the blocklist is full")
)

// MaxIPBlocks bounds the table. The enforcement path holds every entry in a
// map in memory and consults it on every request; an unbounded list is an
// unbounded per-request working set, installed by a control whose whole purpose
// is to be clicked in a hurry.
const MaxIPBlocks = 1000

// ValidateBlockIP applies the rails BEFORE anything is written.
//
// Every one of them exists because this control's failure mode is not "a block
// that does not work" — it is an instance nobody can reach, installed by the
// person who most needed to reach it, through a button placed next to a scary
// red number. There is no undo from outside: the unblock endpoint is behind the
// same lock as everything else.
//
//   - self: blocking the address you are connected from ends the session that
//     would remove the block. This is the rail that fires in practice, because
//     the owner investigating a burst is often behind the same NAT as it.
//   - loopback: 127.0.0.1 is how a local operator administers the instance, and
//     it is the entire access path when AUTH_ENABLED=0. Blocking it removes the
//     escape hatch that exists for exactly this kind of lockout.
//   - trusted proxy: behind nginx every request arrives from the proxy's own
//     address whenever the forwarding chain is not configured as intended.
//     Blocking it blocks all traffic, and the screen would have shown that
//     address as the busiest origin — which is precisely what invites the click.
//
// callerIP is the address the request itself arrived from, already normalized.
// isTrustedProxy is supplied by the router, which owns the parsed CIDR set: the
// configuration accepts networks, so a string comparison here would pass an
// address that "10.0.0.0/8" covers.
func ValidateBlockIP(candidate, callerIP string, isTrustedProxy func(string) bool) (string, error) {
	// normalizeAuditIP has already stripped a port, unbracketed IPv6 and
	// collapsed an IPv4-mapped address, so what parses here is the canonical
	// form the trail and the blocklist both store. Re-unmapping would be dead
	// code, and dead code next to a security rail reads as a rail.
	addr, err := netip.ParseAddr(normalizeAuditIP(candidate))
	if err != nil {
		return "", ErrBlockMalformed
	}
	norm := addr.String()
	if norm == normalizeAuditIP(callerIP) {
		return "", ErrBlockSelf
	}
	if addr.IsLoopback() || addr.IsUnspecified() {
		return "", ErrBlockLoopback
	}
	if isTrustedProxy != nil && isTrustedProxy(norm) {
		return "", ErrBlockProxy
	}
	return norm, nil
}

// BlockIP installs an entry. Idempotent: blocking an address twice is the state
// the caller asked for, and answering an error would make the button look
// broken to whoever clicked it a second time.
func (r *Repository) BlockIP(ctx context.Context, ip, reason string,
	by *authctx.UserID, byEmail string) (IPBlock, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM ip_block`).Scan(&n); err != nil {
		return IPBlock{}, fmt.Errorf("count ip blocks: %w", err)
	}
	if n >= MaxIPBlocks {
		return IPBlock{}, ErrBlockFull
	}
	var actor *int64
	if by != nil {
		id := int64(*by)
		actor = &id
	}
	var out IPBlock
	err := r.pool.QueryRow(ctx, `
		INSERT INTO ip_block (ip, reason, created_by, created_by_email)
		VALUES ($1::inet, NULLIF($2, ''), $3, NULLIF($4, ''))
		ON CONFLICT (ip) DO UPDATE SET ip = EXCLUDED.ip
		RETURNING id, host(ip), reason, created_by_email, created_at`,
		ip, truncateTo(reason, maxIPBlockReason), actor, truncateTo(byEmail, maxAuditEmail)).
		Scan(&out.ID, &out.IP, &out.Reason, &out.CreatedBy, &out.CreatedAt)
	if err != nil {
		return IPBlock{}, fmt.Errorf("block ip: %w", err)
	}
	return out, nil
}

const maxIPBlockReason = 256

// UnblockIP removes an entry, reporting whether one was there.
func (r *Repository) UnblockIP(ctx context.Context, ip string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM ip_block WHERE ip = $1::inet`, normalizeAuditIP(ip))
	if err != nil {
		return false, fmt.Errorf("unblock ip: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListIPBlocks returns the blocklist, newest first.
func (r *Repository) ListIPBlocks(ctx context.Context) ([]IPBlock, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, host(ip), reason, created_by_email, created_at
		FROM ip_block ORDER BY created_at DESC, id DESC LIMIT $1`, MaxIPBlocks)
	if err != nil {
		return nil, fmt.Errorf("list ip blocks: %w", err)
	}
	defer rows.Close()
	out := []IPBlock{}
	for rows.Next() {
		var b IPBlock
		if err := rows.Scan(&b.ID, &b.IP, &b.Reason, &b.CreatedBy, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ip block: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BlockedIPs returns every blocked address, for the enforcement snapshot.
func (r *Repository) BlockedIPs(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT host(ip) FROM ip_block LIMIT $1`, MaxIPBlocks)
	if err != nil {
		return nil, fmt.Errorf("blocked ips: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, fmt.Errorf("scan blocked ip: %w", err)
		}
		out = append(out, ip)
	}
	return out, rows.Err()
}

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
	// exp is read on EVERY request and written only by a reload, so it is an
	// atomic rather than state behind mu. Taking a process-wide mutex in front
	// of every route — before routing, before rate limiting — to compare two
	// timestamps would serialize the whole instance at its narrowest point, and
	// a slow reload would convoy every concurrent request behind it.
	exp atomic.Int64
	// mu makes the reload single-flight: without it a burst arriving on an
	// expired snapshot issues one query per request.
	mu sync.Mutex
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
	// Re-checked under the lock: a burst that all saw the same expiry would
	// otherwise each issue a query, which is the thing the lock is for.
	if time.Now().UnixNano() < b.exp.Load() {
		return
	}
	// Detached from the REQUEST's context, with its own deadline. Bound to the
	// caller's, a client that hangs up mid-reload would cancel the query, leave
	// the stale snapshot in place and — because the failure path backs off —
	// keep it stale for a full TTL. One aborted request would decide
	// enforcement for everybody else.
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), blocklistLoadTimeout)
	defer cancel()
	ips, err := b.load(loadCtx)
	if err != nil {
		// Keep the previous snapshot and back off, so a database that is down
		// does not turn into one reload attempt per request on top of it.
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

// blocklistLoadTimeout bounds one reload. Shorter than the TTL, so a hung
// database costs one deadline rather than blocking the next reload too.
const blocklistLoadTimeout = 3 * time.Second
