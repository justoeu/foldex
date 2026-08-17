//go:build integration

package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/testdb"
)

// newHarnessWithPolicy stands up the stack with the owner-configurable rules
// mounted. Separate from newHarness so the tests written before ADR-35 keep
// exercising the compiled-in floors rather than a settings row.
func newHarnessWithPolicy(t *testing.T) *harness {
	t.Helper()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))
	return newHarnessWith(t, pool, harnessOpts{Policy: true})
}

// signIn logs a seeded account in and returns its client.
func signIn(t *testing.T, h *harness, email, password string) *client {
	t.Helper()
	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": email, "password": password,
	}).Code, "sign-in failed for %s", email)
	return c
}

// ─────────────────────────────────────────────────────────────────────
// The four roles
// ─────────────────────────────────────────────────────────────────────

// Whoever completes setup holds the instance. Before ADR-33 they became an
// ordinary admin, and there was no account that could not be demoted.
func TestRBAC_BootstrapCreatesTheOwner(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	body := decode(t, c.do(http.MethodGet, "/api/auth/me", nil))
	assert.Equal(t, "owner", body["user"].(map[string]any)["role"])
}

// The viewer's whole reason to exist: the same rows an editor sees, and no
// mutation. A 403 rather than a 404, because a viewer knows their own library
// exists and hiding it would be a lie.
func TestRBAC_ViewerReadsItsOwnLibraryButCannotWrite(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "owner@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "viewer@example.com", "a good password", "viewer")

	viewer := signIn(t, h, "viewer@example.com", "a good password")

	assert.Equal(t, http.StatusOK, viewer.do(http.MethodGet, "/api/links", nil).Code,
		"a viewer must still read its own library")

	rec := viewer.do(http.MethodPost, "/api/links", map[string]string{"url": "https://example.com"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "forbidden_role", errCode(t, rec))
}

// An editor is exactly what every pre-ADR-33 `user` became: unchanged reach
// over its own content.
func TestRBAC_EditorStillWrites(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "owner@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "editor@example.com", "a good password", "editor")

	editor := signIn(t, h, "editor@example.com", "a good password")

	assert.Equal(t, http.StatusCreated,
		editor.do(http.MethodPost, "/api/links", map[string]string{"url": "https://example.com"}).Code)
}

// The administration surface answers 404 — not 403 — to an account that is not
// an administrator, so a viewer or editor cannot learn it exists at all.
func TestRBAC_NonAdminGetsNotFoundFromAdmin(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "owner@example.com", "a good password")
	for _, role := range []string{"editor", "viewer"} {
		email := role + "@example.com"
		testdb.SeedUserWithPassword(t, h.pool, email, "a good password", role)
		c := signIn(t, h, email, "a good password")

		for _, path := range []string{"/api/admin/users", "/api/admin/metrics", "/api/admin/roles", "/api/admin/audit"} {
			rec := c.do(http.MethodGet, path, nil)
			assert.Equal(t, http.StatusNotFound, rec.Code, "%s on %s", role, path)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────
// The owner seat
// ─────────────────────────────────────────────────────────────────────

// Role and status are refused outright, and delete too: the owner is the one
// account that always has to be able to administer the instance.
func TestRBAC_OwnerCannotBeDemotedDisabledOrDeleted(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "owner@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "admin@example.com", "a good password", "admin")
	admin := signIn(t, h, "admin@example.com", "a good password")

	for _, patch := range []map[string]string{{"role": "editor"}, {"status": "disabled"}} {
		rec := admin.do(http.MethodPatch, "/api/admin/users/1", patch)
		assert.Equal(t, http.StatusConflict, rec.Code, "patch %v", patch)
		assert.Equal(t, "owner_immutable", errCode(t, rec))
	}

	rec := admin.do(http.MethodDelete, "/api/admin/users/1", nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "owner_immutable", errCode(t, rec))
}

// Ownership is not assignable through the ordinary role editor: it moves only
// through transfer, which demotes the outgoing owner in the same statement.
func TestRBAC_OwnerCannotBeHandedOutByRoleEdit(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	target := testdb.SeedUserWithPassword(t, h.pool, "editor@example.com", "a good password", "editor")

	rec := owner.do(http.MethodPatch, fmt.Sprintf("/api/admin/users/%d", int64(target)),
		map[string]string{"role": "owner"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_role", errCode(t, rec))
}

// Only the owner may transfer, and the seat lands whole: exactly one owner
// before and after.
func TestRBAC_TransferMovesTheSeatAndLeavesExactlyOneOwner(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	target := testdb.SeedUserWithPassword(t, h.pool, "next@example.com", "a good password", "admin")

	rec := owner.do(http.MethodPost,
		fmt.Sprintf("/api/admin/users/%d/transfer-ownership", int64(target)), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "owner", decode(t, rec)["role"])

	ctx := context.Background()
	var owners, admins int
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE role = 'owner'), count(*) FILTER (WHERE role = 'admin')
		 FROM app_user`).Scan(&owners, &admins))
	assert.Equal(t, 1, owners, "the instance must have exactly one owner after a transfer")
	assert.Equal(t, 1, admins, "the outgoing owner becomes an admin")

	// Both accounts lose every session: the outgoing owner's tokens carry a role
	// they no longer hold, and the incoming owner's one they have outgrown.
	var live int
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT count(*) FROM session WHERE revoked_at IS NULL`).Scan(&live))
	assert.Equal(t, 0, live)
}

// An admin passes the /api/admin gate but not this one — and gets 403, not the
// 404 the group answers, because they already know the surface exists.
func TestRBAC_AdminCannotTransferTheInstance(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "owner@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "admin@example.com", "a good password", "admin")
	target := testdb.SeedUserWithPassword(t, h.pool, "next@example.com", "a good password", "editor")
	admin := signIn(t, h, "admin@example.com", "a good password")

	rec := admin.do(http.MethodPost,
		fmt.Sprintf("/api/admin/users/%d/transfer-ownership", int64(target)), nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "forbidden_role", errCode(t, rec))
}

// Handing the seat to a disabled or pending account is a lockout no remaining
// role could undo, since only the owner may transfer.
func TestRBAC_TransferRefusesAnInactiveTarget(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	target := testdb.SeedUserWithPassword(t, h.pool, "off@example.com", "a good password", "editor")
	_, err := h.pool.Exec(context.Background(),
		`UPDATE app_user SET status = 'disabled' WHERE id = $1`, int64(target))
	require.NoError(t, err)

	rec := owner.do(http.MethodPost,
		fmt.Sprintf("/api/admin/users/%d/transfer-ownership", int64(target)), nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "target_not_active", errCode(t, rec))
}

// ─────────────────────────────────────────────────────────────────────
// Administration surfaces
// ─────────────────────────────────────────────────────────────────────

func TestRBAC_MetricsAndRolesDescribeTheInstance(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "editor@example.com", "a good password", "editor")

	metrics := decode(t, owner.do(http.MethodGet, "/api/admin/metrics", nil))
	assert.Equal(t, float64(2), metrics["active_users"])
	assert.Equal(t, float64(0), metrics["pending_invites"])
	assert.Equal(t, float64(14), metrics["permission_count"])

	roles := decode(t, owner.do(http.MethodGet, "/api/admin/roles", nil))
	list := roles["roles"].([]any)
	require.Len(t, list, 4, "every role appears, including ones nobody holds")
	first := list[0].(map[string]any)
	assert.Equal(t, "owner", first["role"], "the matrix is ordered, not map-shuffled")
	assert.Equal(t, float64(1), first["user_count"])
	// The permission vocabulary comes from the server so the screen cannot
	// describe a grid the server does not implement.
	assert.Len(t, roles["permissions"].([]any), 14)
}

// The trail is what an investigation reads, so the events that matter most are
// the ones nobody performs deliberately.
func TestRBAC_AuditRecordsSignInsAndRoleChanges(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	target := testdb.SeedUserWithPassword(t, h.pool, "editor@example.com", "a good password", "editor")

	// A failed sign-in, then a role change.
	h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "editor@example.com", "password": "the wrong password",
	})
	require.Equal(t, http.StatusOK, owner.do(http.MethodPatch,
		fmt.Sprintf("/api/admin/users/%d", int64(target)), map[string]string{"role": "admin"}).Code)

	entries := decode(t, owner.do(http.MethodGet, "/api/admin/audit", nil))["entries"].([]any)
	seen := map[string]bool{}
	for _, raw := range entries {
		seen[raw.(map[string]any)["action"].(string)] = true
	}
	assert.True(t, seen["login.failed"], "a failed sign-in must be recorded: %v", seen)
	assert.True(t, seen["user.role_changed"], "a role change must be recorded: %v", seen)

	// Filtering narrows to one action.
	only := decode(t, owner.do(http.MethodGet, "/api/admin/audit?action=login.failed", nil))["entries"].([]any)
	require.NotEmpty(t, only)
	for _, raw := range only {
		assert.Equal(t, "login.failed", raw.(map[string]any)["action"])
	}
}

// Deleting an account must not erase the record of what it did — "an account
// that no longer exists promoted this user" is exactly the entry an
// investigation needs.
func TestRBAC_AuditOutlivesTheAccountItDescribes(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	target := testdb.SeedUserWithPassword(t, h.pool, "gone@example.com", "a good password", "editor")

	require.Equal(t, http.StatusNoContent, owner.do(http.MethodDelete,
		fmt.Sprintf("/api/admin/users/%d", int64(target)), nil).Code)

	entries := decode(t, owner.do(http.MethodGet, "/api/admin/audit?action=user.deleted", nil))["entries"].([]any)
	require.NotEmpty(t, entries, "the deletion itself must be recorded")
	assert.Equal(t, "gone@example.com", entries[0].(map[string]any)["target_email"],
		"the e-mail is denormalized, so the entry stays readable after the row is gone")
}

// An admin manages people under the rules; only the owner writes the rules.
func TestRBAC_PolicyIsReadableByAdminsAndWritableOnlyByTheOwner(t *testing.T) {
	h := newHarnessWithPolicy(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "admin@example.com", "a good password", "admin")
	admin := signIn(t, h, "admin@example.com", "a good password")

	assert.Equal(t, http.StatusOK, admin.do(http.MethodGet, "/api/admin/policy", nil).Code)

	body := map[string]any{
		"password_min_length": 12, "otp_ttl_minutes": 5, "otp_cooldown_seconds": 60,
		"google_allowed_domains": []string{}, "google_auto_provision": false,
		"google_default_role": "editor",
	}
	rec := admin.do(http.MethodPut, "/api/admin/policy", body)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "forbidden_role", errCode(t, rec))

	require.Equal(t, http.StatusOK, owner.do(http.MethodPut, "/api/admin/policy", body).Code)
}

// The floors are what make the screen unable to lower the instance's security
// from inside the instance.
func TestRBAC_PolicyRefusesValuesBelowTheFloor(t *testing.T) {
	h := newHarnessWithPolicy(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	rec := owner.do(http.MethodPut, "/api/admin/policy", map[string]any{
		"password_min_length": 4, "otp_ttl_minutes": 5, "otp_cooldown_seconds": 60,
		"google_allowed_domains": []string{}, "google_auto_provision": false,
		"google_default_role": "editor",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_policy", errCode(t, rec))
}

// An open allowlist plus auto-provisioning would let anyone holding any Google
// account create themselves a tenant on the instance.
func TestRBAC_PolicyRefusesAutoProvisionWithoutAnAllowlist(t *testing.T) {
	h := newHarnessWithPolicy(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	rec := owner.do(http.MethodPut, "/api/admin/policy", map[string]any{
		"password_min_length": 8, "otp_ttl_minutes": 5, "otp_cooldown_seconds": 60,
		"google_allowed_domains": []string{}, "google_auto_provision": true,
		"google_default_role": "editor",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_policy", errCode(t, rec))
}

// The configured minimum has to reach the code path that validates a password,
// or the screen is describing a rule nothing applies.
func TestRBAC_ConfiguredPasswordFloorIsEnforced(t *testing.T) {
	h := newHarnessWithPolicy(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	require.Equal(t, http.StatusOK, owner.do(http.MethodPut, "/api/admin/policy", map[string]any{
		"password_min_length": 20, "otp_ttl_minutes": 5, "otp_cooldown_seconds": 60,
		"google_allowed_domains": []string{}, "google_auto_provision": false,
		"google_default_role": "editor",
	}).Code)

	// 12 characters: fine under the compiled-in floor of 8, refused under 20.
	rec := owner.do(http.MethodPost, "/api/auth/password/change", map[string]string{
		"current_password": "a good password", "new_password": "short enough",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "password_too_short", errCode(t, rec))
}
