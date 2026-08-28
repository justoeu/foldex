// Package clickctx carries, across one request, the decision of whether a
// public resolution should also be recorded as a click.
//
// It is a leaf — standard library only — for the same reason internal/pkg/
// auditctx is one: the thing that MAKES the decision is HTTP middleware in
// internal/server, and the thing that ACTS on it is a repository in
// internal/links and internal/notes, which internal/server imports. A shared
// leaf is what lets those two meet without a cycle.
//
// # Why a context value rather than a parameter
//
// The click row is written inside the same transaction that resolves the row,
// and the entity id only exists in the middle of that transaction — before it,
// the request holds a slug, and after it, the write has already happened. So a
// decision that keys on the entity id has to be made from inside the
// repository, while the identity it keys on (the visitor's address) is only
// available at the HTTP edge. Threading a predicate through
// publictarget.Resolve, both public handlers and four repository methods would
// put an HTTP concern in the signature of every one of them, to be passed nil
// by every other caller.
//
// The precedent is auditctx, which is the same shape run backwards: a
// middleware installs a holder, code far below annotates it, and the middleware
// reads it after. Here the middleware installs a decision and code far below
// asks it. In both, the absent case is the safe one and needs no ceremony —
// see Allow.
package clickctx

import "context"

// Gate answers "should this resolution be recorded as a click?".
//
// It is consulted once per resolution, with the entity already identified, and
// it is not a pure predicate: the implementation records that this visitor has
// now been counted for this entity, so a second call inside the window answers
// false. Implementations must be safe for concurrent use.
type Gate func(entityKind string, entityID int64) bool

type contextKey struct{}

// WithGate returns a context carrying g. A nil g is legal and means "record
// everything", which is what an instance with coalescing switched off gets.
func WithGate(ctx context.Context, g Gate) context.Context {
	return context.WithValue(ctx, contextKey{}, g)
}

// Allow reports whether this resolution should write a click row.
//
// A context with no gate answers TRUE, and that default is load-bearing. Every
// caller is on a path that has always written a row: the public routes, yes,
// but also imports, background jobs and every test. Defaulting to "suppress"
// would mean that mounting a public route anywhere the middleware is not — or
// forgetting to wire it — silently stops counting, and a click counter that
// reads zero is indistinguishable from a quiet day. Failing towards recording
// costs at most a duplicate row; failing towards suppressing loses the metric
// with nothing to show for it.
func Allow(ctx context.Context, entityKind string, entityID int64) bool {
	g, ok := ctx.Value(contextKey{}).(Gate)
	if !ok || g == nil {
		return true
	}
	return g(entityKind, entityID)
}
