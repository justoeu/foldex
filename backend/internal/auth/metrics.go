package auth

import (
	"context"
	"fmt"
	"time"

	"foldex/internal/pkg/authctx"
	"foldex/internal/roleperm"
)

// InstanceMetrics is the administration screen's header: four numbers that
// answer "is this instance healthy?" without opening any of its sections.
//
// Every field is derived at read time. Nothing here is a counter that some
// write path has to remember to increment — a stale "active users" tile is
// worse than no tile, because it reads as fact.
type InstanceMetrics struct {
	ActiveUsers        int    `json:"active_users"`
	ActiveUsersAdded30 int    `json:"active_users_added_30d"`
	PendingInvites     int    `json:"pending_invites"`
	NextInviteExpiry   *int64 `json:"next_invite_expiry_hours"`
	RolesInUse         int    `json:"roles_in_use"`
	PermissionCount    int    `json:"permission_count"`
	// TwoFactorPercent is over ACTIVE accounts only. Counting pending and
	// disabled rows would let an instance improve its own score by inviting
	// people who never sign in, or by disabling the accounts without a second
	// factor — both of which move the number in the reassuring direction while
	// making the instance no safer.
	TwoFactorPercent int `json:"two_factor_percent"`
}

// RoleSummary is one row of the administration screen's RBAC matrix.
//
// Permissions come from the same matrix the middleware authorizes against, not
// from a copy maintained for display. A screen that described a grid the server
// does not enforce would be worse than showing nothing.
type RoleSummary struct {
	Role        authctx.Role         `json:"role"`
	Permissions []authctx.Permission `json:"permissions"`
	UserCount   int                  `json:"user_count"`
	// Editable says whether this role's grants may be configured at all. The
	// screen renders from this rather than re-deriving "is it the owner?",
	// for §5's usual reason: two copies of one policy drift, and the direction
	// nobody notices is the one that offers a save the server refuses.
	Editable bool `json:"editable"`
}

// Roles returns every role with its EFFECTIVE permissions and how many
// accounts hold it.
//
// `grants` is the resolved matrix, not the compiled one. Rendering the compiled
// matrix on a screen whose whole purpose is to show what the server enforces
// would make the screen a description of a rule the server stopped applying
// the moment anyone edited it.
func (r *Repository) Roles(ctx context.Context, grants roleperm.Grants) ([]RoleSummary, error) {
	counts := map[authctx.Role]int{}
	rows, err := r.pool.Query(ctx,
		`SELECT role, count(*) FROM app_user GROUP BY role`)
	if err != nil {
		return nil, fmt.Errorf("role counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var role authctx.Role
		var n int
		if err := rows.Scan(&role, &n); err != nil {
			return nil, fmt.Errorf("scan role count: %w", err)
		}
		counts[role] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("role counts: %w", err)
	}

	// Ranged over AllRoles, not over the counts map: a role nobody currently
	// holds still has to appear in the matrix, and map iteration would also
	// reshuffle the rows between two loads of the same screen.
	out := make([]RoleSummary, 0, len(authctx.AllRoles))
	for _, role := range authctx.AllRoles {
		out = append(out, RoleSummary{
			Role:        role,
			Permissions: grants.Permissions(role),
			UserCount:   counts[role],
			Editable:    authctx.IsRoleEditable(role),
		})
	}
	return out, nil
}

// Metrics computes the administration header in one round trip.
//
// One query with several scalar subqueries rather than four queries: the tiles
// render together, and four separate reads could observe four different
// instants — an invite counted as pending in one and expired in the next, on a
// screen that presents them as a single snapshot.
func (r *Repository) Metrics(ctx context.Context) (InstanceMetrics, error) {
	var m InstanceMetrics
	var twoFactor int
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM app_user WHERE status = 'active'),
			(SELECT count(*) FROM app_user
			  WHERE status = 'active' AND created_at >= now() - interval '30 days'),
			(SELECT count(*) FROM invite
			  WHERE accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()),
			(SELECT count(DISTINCT role) FROM app_user WHERE status = 'active'),
			(SELECT count(*) FROM app_user u
			  WHERE u.status = 'active'
			    AND EXISTS (SELECT 1 FROM totp_secret ts
			                 WHERE ts.user_id = u.id AND ts.confirmed_at IS NOT NULL))
	`).Scan(&m.ActiveUsers, &m.ActiveUsersAdded30, &m.PendingInvites,
		&m.RolesInUse, &twoFactor)
	if err != nil {
		return InstanceMetrics{}, fmt.Errorf("metrics: %w", err)
	}
	// Divided by ActiveUsers, which the same statement already counted: a second
	// identical subquery would be one more scan for a number we hold.
	if m.ActiveUsers > 0 {
		m.TwoFactorPercent = twoFactor * 100 / m.ActiveUsers
	}
	m.PermissionCount = len(authctx.AllPermissions)

	// Nullable and therefore read separately: a scalar subquery returning no
	// row would scan NULL into the int fields above and fail the whole call
	// just because nothing is currently pending.
	var expiresAt *time.Time
	if err := r.pool.QueryRow(ctx, `
		SELECT min(expires_at) FROM invite
		WHERE accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
	`).Scan(&expiresAt); err != nil {
		return InstanceMetrics{}, fmt.Errorf("metrics invite expiry: %w", err)
	}
	if expiresAt != nil {
		hours := int64(time.Until(*expiresAt).Hours())
		if hours < 0 {
			hours = 0
		}
		m.NextInviteExpiry = &hours
	}
	return m, nil
}
