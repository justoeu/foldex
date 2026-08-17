// Package authctx carries the authenticated principal across the request.
//
// It is a leaf: it imports only the standard library, so every repository and
// handler package can depend on it without any risk of an import cycle — the
// same role internal/pkg/httperr already plays for errors.
//
// The contract is deliberately narrow. The context transports the principal;
// repositories never read it. Every repository method takes a UserID parameter
// explicitly, so forgetting to scope a query is a compile error rather than a
// cross-tenant leak. See docs/SDD-AUTH-RBAC.md §8.1 for why this was chosen
// over Postgres RLS.
package authctx

import "context"

// UserID identifies the owner of a row.
//
// It is a distinct type rather than a bare int64 on purpose: it makes
// Get(ctx, linkID, userID) with the arguments swapped a compile error. Convert
// with int64(uid) at the pgx boundary — we do not rely on pgx's reflection
// fallback for named integer types.
type UserID int64

// Role is the RBAC level of a principal. The set is closed by a CHECK
// constraint on app_user.role, and what each role may do lives in the matrix in
// permissions.go — see ADR-33.
type Role string

const (
	// RoleOwner runs the instance. Exactly one row holds it, enforced by a
	// partial unique index rather than by handler discipline, and it moves only
	// by transfer.
	RoleOwner Role = "owner"
	// RoleAdmin manages people and reads the audit trail, but does not set the
	// policies it manages them under.
	RoleAdmin Role = "admin"
	// RoleEditor is an ordinary account: full CRUD over its own library. This is
	// what migration 000032 turned every pre-existing 'user' into.
	RoleEditor Role = "editor"
	// RoleViewer holds its own library read-only.
	RoleViewer Role = "viewer"
)

// IsAdmin reports whether the role may reach /api/admin/* at all.
//
// It stays a role test rather than a permission test because it gates the whole
// route group before any handler-specific permission does: the group answers 404
// to everyone else, so the finer check inside would never run. The per-endpoint
// authority is still Can — this only decides whether the surface exists.
func (r Role) IsAdmin() bool { return r == RoleOwner || r == RoleAdmin }

// Via records which credential authenticated the request. API tokens skip CSRF
// (they carry no ambient credential) and are rejected on the auth, admin and
// backup routes.
const (
	ViaSession  = "session"
	ViaAPIToken = "api_token"
)

// Principal is the authenticated identity behind a request.
//
// SessionID and TokenID are mutually exclusive, and the zero value of each is
// load-bearing: an endpoint that revokes "every session except SessionID" would
// revoke ALL of them if handed a token principal. That is why every such
// endpoint sits behind Middleware.RejectAPIToken rather than checking Via
// itself — one guard at the mount point instead of a rule each handler must
// remember.
type Principal struct {
	UserID    UserID
	Role      Role
	SessionID int64 // 0 when Via is ViaAPIToken
	TokenID   int64 // 0 when Via is ViaSession
	Via       string
}

type contextKey struct{}

// WithPrincipal returns a context carrying p. Called by the auth middleware.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

// FromContext returns the principal, if the request was authenticated.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok
}

// MustUser returns the authenticated user id, panicking when there is none.
//
// It panics rather than returning a zero UserID because a zero owner would
// silently match no rows — or, worse, be written into one. Every caller sits
// behind the Authenticate middleware, so a panic here means a routing mistake,
// and it should surface on the first request in development rather than as a
// data bug in production. The Recoverer middleware turns it into a 500.
func MustUser(ctx context.Context) UserID {
	p, ok := FromContext(ctx)
	if !ok {
		panic("authctx: no principal in context — route is mounted outside the Authenticate middleware")
	}
	return p.UserID
}

// User returns the authenticated user id and whether one was present.
func User(ctx context.Context) (UserID, bool) {
	p, ok := FromContext(ctx)
	if !ok {
		return 0, false
	}
	return p.UserID, true
}
