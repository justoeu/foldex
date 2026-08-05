package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/secrets"
)

// touchInterval throttles last_seen_at writes. One UPDATE per request on the
// session table is the classic write-amplification mistake: it produces a dead
// tuple per page view and makes the hottest table in the schema the one that
// needs the most vacuuming.
const touchInterval = time.Minute

// Middleware resolves the principal and enforces CSRF and RBAC.
type Middleware struct {
	repo    *Repository
	cookies CookieOptions
	logger  *slog.Logger

	mu        sync.Mutex
	lastTouch map[int64]time.Time
}

func NewMiddleware(repo *Repository, cookies CookieOptions, logger *slog.Logger) *Middleware {
	return &Middleware{
		repo:      repo,
		cookies:   cookies,
		logger:    logger,
		lastTouch: make(map[int64]time.Time),
	}
}

var errNoCredential = errors.New("auth: no credential presented")

// Authenticate resolves the access cookie into a principal and rejects the
// request when it cannot.
//
// It is fail-closed by construction: every path that does not end in
// WithPrincipal writes a 401 and returns. There is no branch that calls
// next.ServeHTTP with a bare context, which is what stops a resolution bug from
// degrading into "anonymous request treated as user 0".
func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, csrfHash, err := m.resolve(r)
		if err != nil {
			// A dead cookie is cleared on the way out so the browser stops
			// sending it; otherwise every subsequent request pays a failed
			// lookup for a token that will never resolve again.
			if !errors.Is(err, errNoCredential) {
				m.cookies.ClearSession(w)
			}
			httperr.Write(w, httperr.New(http.StatusUnauthorized, "unauthorized", "authentication required"))
			return
		}
		if err := m.verifyCSRF(r, p, csrfHash); err != nil {
			httperr.Write(w, err)
			return
		}
		m.maybeTouch(r.Context(), p.SessionID)
		next.ServeHTTP(w, r.WithContext(authctx.WithPrincipal(r.Context(), p)))
	})
}

// Optional resolves a principal when one is present but never rejects.
//
// Only /api/auth/me uses it, and only because that endpoint's contract is
// "always 200". A 401 there would recurse through the SPA's refresh
// interceptor on every cold boot: me → 401 → refresh → me → 401 …
func (m *Middleware) Optional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _, err := m.resolve(r)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(authctx.WithPrincipal(r.Context(), p)))
	})
}

// RequireAdmin gates the /api/admin surface.
//
// It answers 404, not 403, for a non-admin. A 403 confirms the route exists and
// that the caller merely lacks the role, which tells an attacker exactly which
// endpoints are worth escalating toward; 404 says nothing. This mirrors the
// row-level rule in CLAUDE.md §4.
func (m *Middleware) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := authctx.FromContext(r.Context())
		if !ok || !p.Role.IsAdmin() {
			httperr.Write(w, httperr.ErrNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Middleware) resolve(r *http.Request) (authctx.Principal, []byte, error) {
	raw := cookieValue(r, CookieAccess)
	if raw == "" {
		return authctx.Principal{}, nil, errNoCredential
	}
	res, err := m.repo.ResolveAccess(r.Context(), raw)
	if err != nil {
		return authctx.Principal{}, nil, err
	}
	return res.Principal, res.CSRFHash, nil
}

// verifyCSRF enforces the signed double-submit on unsafe verbs.
//
// The header is compared against session.csrf_token_hash — the value the
// server stored — and not against the fx_csrf cookie. Naive double-submit
// (header == cookie) is defeated by cookie injection from a sibling subdomain,
// because there the attacker controls BOTH sides of the comparison. Binding to
// the session row means forging a match requires producing a preimage of a
// stored sha256.
func (m *Middleware) verifyCSRF(r *http.Request, p authctx.Principal, csrfHash []byte) error {
	if isSafeMethod(r.Method) {
		return nil
	}
	// API tokens (PR4) carry no ambient credential, so there is nothing for a
	// cross-site request to ride on and nothing for CSRF to protect against.
	if p.Via != authctx.ViaSession {
		return nil
	}
	header := r.Header.Get(CSRFHeader)
	if header == "" || !secrets.Equal(secrets.Hash(header), csrfHash) {
		return httperr.New(http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token")
	}
	return nil
}

// maybeTouch updates last_seen_at at most once per touchInterval per session.
func (m *Middleware) maybeTouch(ctx context.Context, sessionID int64) {
	now := time.Now()
	m.mu.Lock()
	last, seen := m.lastTouch[sessionID]
	if seen && now.Sub(last) < touchInterval {
		m.mu.Unlock()
		return
	}
	m.lastTouch[sessionID] = now
	m.mu.Unlock()
	m.repo.TouchSession(ctx, sessionID)
}

// forgetTouch drops a session from the throttle map. Called on the logout
// paths that know a specific session id.
func (m *Middleware) forgetTouch(sessionID int64) {
	m.mu.Lock()
	delete(m.lastTouch, sessionID)
	m.mu.Unlock()
}

// SweepTouch prunes throttle entries not seen within olderThan, and returns how
// many it dropped.
//
// forgetTouch alone is NOT enough, and relying on it was a bug: it only fires
// on the two paths that revoke a single named session. Every bulk revocation —
// logout-everywhere, an admin disabling a user, a password change dropping the
// other devices — and every grace-window sibling leaves an entry behind
// forever. Pruning by age makes the map bounded by construction rather than by
// remembering to call forgetTouch from each new revocation path, which is
// exactly the discipline that failed here.
//
// Dropping a live session's entry is harmless: the next request simply pays one
// extra last_seen_at UPDATE and re-seeds it.
func (m *Middleware) SweepTouch(olderThan time.Duration) int {
	cutoff := time.Now().Add(-olderThan)
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, seen := range m.lastTouch {
		if seen.Before(cutoff) {
			delete(m.lastTouch, id)
			n++
		}
	}
	return n
}

func isSafeMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// NoStore marks every /api/auth response uncacheable.
//
// These responses carry session state and, on some paths, a CSRF token. A
// shared cache — or the browser's own back/forward cache — holding one and
// replaying it to a different user is the failure this prevents.
func NoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// VaryCookie tells caches that /api responses depend on the session.
func VaryCookie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Cookie")
		next.ServeHTTP(w, r)
	})
}
