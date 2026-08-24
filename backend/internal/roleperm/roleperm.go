// Package roleperm resolves the configurable half of the RBAC matrix (ADR-42).
//
// It is a leaf: it imports authctx and nothing else of the application. The
// authorization gate imports this, never the reverse.
package roleperm

import (
	"fmt"
	"reflect"

	"foldex/internal/pkg/authctx"
)

// Grants is one immutable resolution of the matrix.
//
// Immutable because it is read on the authorization path of every request and
// replaced wholesale on a write. A map mutated in place would need a lock per
// permission check; swapping a pointer under one RWMutex costs a read lock and
// cannot be observed half-updated.
type Grants struct {
	held map[authctx.Role]map[authctx.Permission]bool
}

// Can reports whether the role holds the permission.
//
// A role absent from the map resolves to a nil set and answers false for
// everything, exactly as the compiled matrix does: authorization fails CLOSED
// and an unknown role is powerless rather than unconstrained.
func (g Grants) Can(r authctx.Role, p authctx.Permission) bool { return g.held[r][p] }

// Permissions returns the role's grants in AllPermissions order.
func (g Grants) Permissions(r authctx.Role) []authctx.Permission {
	out := make([]authctx.Permission, 0, len(authctx.AllPermissions))
	for _, p := range authctx.AllPermissions {
		if g.Can(r, p) {
			out = append(out, p)
		}
	}
	return out
}

// Resolve builds the effective matrix from the stored editable rows.
//
// Three rules, and each is what keeps some state of the table from being fatal:
//
//   - The OWNER never reads `stored`. Its grants are the compiled matrix, so no
//     write and no truncation of that table can produce an instance with nobody
//     able to repair it.
//   - A LOCKED permission is taken from the compiled matrix, whatever the row
//     says. That is what makes `roles.assign` genuinely un-grantable rather
//     than merely un-offered by the current screen, and what keeps
//     `content.read` from being removable by a hand-written INSERT.
//   - Everything else is exactly what the table holds. An editable permission
//     absent from the table is absent from the role — that is the whole point
//     of the table, and it is why the migration seeds it rather than treating
//     "no rows" as "defaults".
func Resolve(stored map[authctx.Role][]authctx.Permission) Grants {
	compiled := authctx.DefaultGrants()
	held := make(map[authctx.Role]map[authctx.Permission]bool, len(authctx.AllRoles))

	for _, role := range authctx.AllRoles {
		set := make(map[authctx.Permission]bool, len(authctx.AllPermissions))
		if !authctx.IsRoleEditable(role) {
			for p, ok := range compiled[role] {
				set[p] = ok
			}
			held[role] = set
			continue
		}
		for _, p := range stored[role] {
			if !authctx.IsPermissionLocked(p) {
				set[p] = true
			}
		}
		for _, p := range authctx.AllPermissions {
			if authctx.IsPermissionLocked(p) && compiled[role][p] {
				set[p] = true
			}
		}
		held[role] = set
	}
	return Grants{held: held}
}

// Default is the matrix with nothing configured — the compiled one.
func Default() Grants {
	stored := make(map[authctx.Role][]authctx.Permission, len(authctx.AllRoles))
	compiled := authctx.DefaultGrants()
	for _, role := range authctx.AllRoles {
		for _, p := range authctx.AllPermissions {
			if compiled[role][p] {
				stored[role] = append(stored[role], p)
			}
		}
	}
	return Resolve(stored)
}

// ErrLocked and friends are the refusals a write can earn. They are semantic:
// the handler owns status and message (CLAUDE.md §7).
var (
	ErrRoleNotEditable   = fmt.Errorf("role is not editable")
	ErrPermissionLocked  = fmt.Errorf("permission is not configurable")
	ErrUnknownPermission = fmt.Errorf("unknown permission")
	ErrEscalation        = fmt.Errorf("cannot grant a permission you do not hold")
)

// ValidateWrite checks a proposed grant set for one role against the caller.
//
// `want` is the full editable set the caller is asking for — absent means
// revoked. The rules, in the order a reader needs them:
//
//  1. The role must be editable at all (the owner is not).
//  2. Every named permission must exist. An unknown string silently dropped
//     would let a typo read as a successful save that changed nothing.
//  3. A locked permission may not be named in either direction.
//  4. The caller may not grant what the CALLER does not hold. This is the rule
//     that answers "an admin must not give itself owner-level powers", and it
//     is stated in terms of the caller rather than a list of owner-level
//     permissions on purpose: a permission unlocked later is covered by
//     construction, where a list would have to be remembered.
func ValidateWrite(current Grants, caller authctx.Role, target authctx.Role, want []authctx.Permission) error {
	// The caller must hold the meta-permission, checked HERE and not only at
	// the route. Every other rule below is inside the loop over `want`, so an
	// empty set skipped the caller entirely: Set(viewer, admin, nil) stripped
	// every admin to its locked floor and returned nil. Unreachable over HTTP
	// today — the route gates on roles.assign — but this function is documented
	// as the choke point a second entry point cannot get past, and it was not
	// one.
	if !current.Can(caller, authctx.PermRolesAssign) {
		return fmt.Errorf("%w: %q cannot assign permissions", ErrEscalation, caller)
	}
	if !authctx.IsRoleEditable(target) {
		return ErrRoleNotEditable
	}
	known := make(map[authctx.Permission]bool, len(authctx.AllPermissions))
	for _, p := range authctx.AllPermissions {
		known[p] = true
	}
	for _, p := range want {
		if !known[p] {
			return fmt.Errorf("%w: %q", ErrUnknownPermission, p)
		}
		if authctx.IsPermissionLocked(p) {
			return fmt.Errorf("%w: %q", ErrPermissionLocked, p)
		}
		if !current.Can(caller, p) {
			return fmt.Errorf("%w: %q", ErrEscalation, p)
		}
	}
	return nil
}

// Reader is the one question the authorization gate asks. Mirrors
// authgate.Grants; declared here too so this package stays a leaf.
type Reader interface {
	Can(authctx.Role, authctx.Permission) bool
}

// OrDefault substitutes the compiled matrix for a nil one.
//
// One place, because there were four — the router, policy, backup and folders
// each spelled `if grants == nil { grants = Default() }` — while the GATE
// documents a nil matrix as fail-CLOSED. Both readings are right where they
// sit: a constructor saying "nil means the compiled matrix" is a deliberate,
// visible substitution for a test, while a gate reached with nil anyway can
// only mean authorization was never wired, which is not a state to serve
// requests in. Four copies of the first make it easy to misread as the second.
func OrDefault(g Reader) Reader {
	if g == nil {
		return Default()
	}
	// A TYPED nil is not `== nil` once it is inside an interface: a caller
	// holding a nil *Repository and passing it here produces a non-nil Reader
	// whose Can dereferences nothing. That is not hypothetical — collapsing the
	// four `if grants == nil` copies into this function turned four correct
	// CONCRETE comparisons into interface ones, and the admin gate panicked on
	// the first request of any handler built with a nil store.
	//
	// Checked here rather than at each call site, which is the whole point of
	// the function: a rule that four callers have to remember is the rule that
	// was just removed.
	if v := reflect.ValueOf(g); v.Kind() == reflect.Ptr && v.IsNil() {
		return Default()
	}
	return g
}
