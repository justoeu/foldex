// Package auditctx lets a handler name the row it just touched, so the
// content-audit middleware can record WHAT changed and not merely that
// something did.
//
// A mutable holder installed by the middleware, rather than a value the handler
// returns: the middleware runs around the handler and sees only the request and
// the status code, and the only party that knows a POST /api/links created the
// row titled "ADR-46 draft" is the handler itself. Threading that back as a
// return value would mean changing every handler signature in the product for
// one optional annotation.
//
// Annotation is OPTIONAL by construction. A handler that never calls Set still
// produces an audit row — action, actor, address, time — and loses only the
// human label on the owner's own-activity feed. That is the right failure: the
// trail's coverage does not depend on anyone remembering, and forgetting
// degrades one column instead of dropping the event.
package auditctx

import (
	"context"
	"net/http"
	"sync"
)

type ctxKey struct{}

// holder carries what the handler learned. Guarded by a mutex because a handler
// is free to spawn work, and the middleware reads this after the handler
// returns — a race the detector would find long after the code shipped.
type holder struct {
	mu      sync.Mutex
	kind    string
	id      *int64
	subject string
}

// With installs a holder. Called by the middleware, once per request.
func With(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, &holder{})
}

// Set names the row the request touched.
//
// subject is user CONTENT — a link's title, a folder's name — and the read
// split in internal/auth is what keeps it from reaching anyone but its owner.
// Never pass a credential, a token or a URL with one in it.
func Set(ctx context.Context, kind string, id int64, subject string) {
	h, ok := ctx.Value(ctxKey{}).(*holder)
	if !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.kind = kind
	h.subject = subject
	if id != 0 {
		v := id
		h.id = &v
	}
}

// SetRequest is Set for a handler that has the request rather than the context.
func SetRequest(r *http.Request, kind string, id int64, subject string) {
	Set(r.Context(), kind, id, subject)
}

// Get returns the annotation, or zero values when nothing annotated.
func Get(ctx context.Context) (kind string, id *int64, subject string) {
	h, ok := ctx.Value(ctxKey{}).(*holder)
	if !ok {
		return "", nil, ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.kind, h.id, h.subject
}

// trustedKey marks a request whose address a configured proxy vouched for.
type trustedKey struct{}

// MarkTrustedIP records that server.trustedProxyRealIP rewrote RemoteAddr from
// a forwarding header sent by a peer inside TRUSTED_PROXY_IPS.
//
// The flag travels in the context rather than being re-derived at the write
// site because only that middleware holds the trusted-proxy set and the peer
// address it compared against — by the time an audit write happens, RemoteAddr
// has already been overwritten and the evidence is gone. Without it the trail
// would record every address as equally authoritative, which is the exact
// property migration 000033 refused to store.
func MarkTrustedIP(ctx context.Context) context.Context {
	return context.WithValue(ctx, trustedKey{}, true)
}

// IPTrusted reports whether a configured proxy vouched for this request's
// address. False for a direct bind, which is the product's own default.
func IPTrusted(ctx context.Context) bool {
	v, _ := ctx.Value(trustedKey{}).(bool)
	return v
}
