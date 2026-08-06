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
	lastTouch map[touchKey]time.Time
}

// touchKey namespaces the throttle map by credential kind.
//
// Session ids and API token ids are both dense BIGSERIALs from different
// sequences, so a map keyed on the bare integer would have session 7 and token
// 7 suppressing each other's writes — a bug that shows up as a stale
// "last used" column and nothing else.
type touchKey struct {
	via string
	id  int64
}

func NewMiddleware(repo *Repository, cookies CookieOptions, logger *slog.Logger) *Middleware {
	return &Middleware{
		repo:      repo,
		cookies:   cookies,
		logger:    logger,
		lastTouch: make(map[touchKey]time.Time),
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
		m.touch(r.Context(), p)
		next.ServeHTTP(w, r.WithContext(authctx.WithPrincipal(r.Context(), p)))
	})
}

// touch records activity against whichever credential authenticated the
// request, throttled the same way for both.
//
// Split by Via rather than by "is SessionID zero", because zero is a
// valid-looking id and the branch should read as a decision, not as a
// coincidence. Token traffic is scripted and can be far heavier than a human's,
// so it needs the throttle at least as much as a session does.
func (m *Middleware) touch(ctx context.Context, p authctx.Principal) {
	if p.Via == authctx.ViaAPIToken {
		if m.shouldTouch(touchKey{authctx.ViaAPIToken, p.TokenID}) {
			m.repo.TouchAPIToken(ctx, p.TokenID)
		}
		return
	}
	m.maybeTouch(ctx, p.SessionID)
}

// Optional resolves a principal when one is present but never rejects for the
// LACK of one.
//
// /api/auth/me needs it because that endpoint's contract is "always 200" — a
// 401 there would recurse through the SPA's refresh interceptor on every cold
// boot: me → 401 → refresh → me → 401 … The 2FA enrollment routes need it
// because they serve two callers: a signed-in user adding a factor, and an
// admin mid-login who has only a pre-auth challenge.
//
// It DOES reject on a failed CSRF check. Not rejecting would make "optional
// authentication" a way to mount an unsafe verb outside CSRF protection
// entirely: the browser attaches the session cookie to a cross-site POST
// regardless, so a resolved principal on an unsafe method needs the header
// exactly as much here as under Authenticate. Safe methods are unaffected,
// which is why /me is untouched by this.
func (m *Middleware) Optional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, csrfHash, err := m.resolve(r)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		if cerr := m.verifyCSRF(r, p, csrfHash); cerr != nil {
			httperr.Write(w, cerr)
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

// RejectAPIToken refuses a bearer credential on surfaces it must never reach.
//
// API tokens exist for the browser extension and for scripts: they are
// long-lived, stored in plain configuration, and their whole point is that no
// human is present. Letting one change a password, mint an invite, promote an
// account or download a full backup would make a token pasted into a config
// file equivalent to the account itself. The scope column says `content`; this
// middleware is what makes that word mean something.
//
// 404, not 403, on the admin surface — mounted alongside RequireAdmin, which
// answers the same way for the same reason.
func (m *Middleware) RejectAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := authctx.FromContext(r.Context()); ok && p.Via == authctx.ViaAPIToken {
			httperr.Write(w, httperr.New(http.StatusForbidden, "token_scope",
				"an API token cannot be used on this endpoint"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// resolve turns whichever credential the request carries into a principal.
//
// The session cookie wins when both are present. A browser that has an
// Authorization header AND a live session is almost always a developer testing
// with curl-copied headers, and silently preferring the token would give them a
// principal with no CSRF protection and no admin routes — a confusing failure
// far from its cause.
func (m *Middleware) resolve(r *http.Request) (authctx.Principal, []byte, error) {
	if raw := cookieValue(r, CookieAccess); raw != "" {
		res, err := m.repo.ResolveAccess(r.Context(), raw)
		if err != nil {
			return authctx.Principal{}, nil, err
		}
		return res.Principal, res.CSRFHash, nil
	}
	if bearer, ok := bearerToken(r); ok {
		p, err := m.repo.ResolveAPIToken(r.Context(), bearer)
		if err != nil {
			return authctx.Principal{}, nil, err
		}
		return p, nil, nil
	}
	return authctx.Principal{}, nil, errNoCredential
}

// bearerToken extracts an `Authorization: Bearer …` value.
//
// The scheme is matched case-insensitively (RFC 7235 says it is), and anything
// that is not our own prefix is left for ResolveAPIToken to reject — this
// function's job is parsing, not policy.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	rest, found := strings.CutPrefix(h, "Bearer ")
	if !found {
		if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
			return "", false
		}
		rest = h[7:]
	}
	rest = strings.TrimSpace(rest)
	return rest, rest != ""
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
	if m.shouldTouch(touchKey{authctx.ViaSession, sessionID}) {
		m.repo.TouchSession(ctx, sessionID)
	}
}

// shouldTouch reports whether enough time has passed to write again, recording
// the decision. It returns true at most once per touchInterval per key.
func (m *Middleware) shouldTouch(k touchKey) bool {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if last, seen := m.lastTouch[k]; seen && now.Sub(last) < touchInterval {
		return false
	}
	m.lastTouch[k] = now
	return true
}

// forgetTouch drops a session from the throttle map. Called on the logout
// paths that know a specific session id.
func (m *Middleware) forgetTouch(sessionID int64) {
	m.mu.Lock()
	delete(m.lastTouch, touchKey{authctx.ViaSession, sessionID})
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
	for k, seen := range m.lastTouch {
		if seen.Before(cutoff) {
			delete(m.lastTouch, k)
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
