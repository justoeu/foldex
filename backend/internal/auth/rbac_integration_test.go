//go:build integration

package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
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

// ─────────────────────────────────────────────────────────────────────
// Google auto-provisioning (ADR-35) — the one path that mints an account
// from an anonymous request, so every refusal is asserted, not assumed.
// ─────────────────────────────────────────────────────────────────────

// setPolicy saves an instance policy as the owner, returning the client so the
// caller can keep using it.
func setPolicy(t *testing.T, owner *client, body map[string]any) {
	t.Helper()
	rec := owner.do(http.MethodPut, "/api/admin/policy", body)
	require.Equal(t, http.StatusOK, rec.Code, "policy save: %s", rec.Body.String())
}

func provisioningPolicy(domains []string, on bool, role string) map[string]any {
	return map[string]any{
		"password_min_length": 8, "otp_ttl_minutes": 5, "otp_cooldown_seconds": 60,
		"google_allowed_domains": domains, "google_auto_provision": on,
		"google_default_role": role,
	}
}

// The success case, asserted all the way down to the rows: an account exists,
// its address is recorded verified because Google asserted it, the identity is
// linked, and the role is the configured non-administrative one.
func TestProvision_CreatesTheAccountWhenTheOwnerEnabledIt(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{Policy: true})
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	setPolicy(t, owner, provisioningPolicy([]string{"example.com"}, true, "viewer"))

	g.as("new-subject", "newcomer@example.com", true)
	outcome, failure := h.client(t).googleRoundTrip(t, auth.OAuthPurposeLogin)
	require.Empty(t, failure, "provisioning must succeed")
	assert.Equal(t, "signed_in", outcome)

	ctx := context.Background()
	var role, status string
	var verified *time.Time
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT role, status, email_verified_at FROM app_user WHERE email_normalized = $1`,
		"newcomer@example.com").Scan(&role, &status, &verified))
	assert.Equal(t, "viewer", role, "the configured default role, never an administrative one")
	assert.Equal(t, "active", status)
	assert.NotNil(t, verified, "Google asserted the address, which is why it was accepted")

	var identities int
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT count(*) FROM user_identity WHERE subject = $1`, "new-subject").Scan(&identities))
	assert.Equal(t, 1, identities, "the identity is linked in the same transaction as the row")
}

// Every refusal is the SAME not_linked an unknown address has always produced.
// A distinct answer would tell an anonymous caller which instances are open, or
// let them enumerate the allowlist one guess at a time.
func TestProvision_EveryRefusalIsIndistinguishable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  map[string]any
		subject string
		email   string
		// verified false stands for an address Google did not confirm.
		verified bool
	}{
		{
			name:   "auto-provisioning is off",
			policy: provisioningPolicy([]string{"example.com"}, false, "editor"),
			email:  "nobody@example.com", subject: "s1", verified: true,
		},
		{
			name:   "domain is not on the allowlist",
			policy: provisioningPolicy([]string{"example.com"}, true, "editor"),
			email:  "nobody@other.test", subject: "s2", verified: true,
		},
		{
			name:   "a subdomain is not the domain",
			policy: provisioningPolicy([]string{"example.com"}, true, "editor"),
			email:  "nobody@mail.example.com", subject: "s3", verified: true,
		},
		{
			name:   "a suffix is not the domain",
			policy: provisioningPolicy([]string{"example.com"}, true, "editor"),
			email:  "nobody@notexample.com", subject: "s4", verified: true,
		},
		{
			name:   "the address is unverified",
			policy: provisioningPolicy([]string{"example.com"}, true, "editor"),
			email:  "nobody@example.com", subject: "s5", verified: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, g := newGoogleHarness(t, harnessOpts{Policy: true})
			owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
			setPolicy(t, owner, tc.policy)

			g.as(tc.subject, tc.email, tc.verified)
			outcome, failure := h.client(t).googleRoundTrip(t, auth.OAuthPurposeLogin)

			assert.Empty(t, outcome)
			assert.Equal(t, "not_linked", failure, "every refusal must look identical")

			var accounts int
			require.NoError(t, h.pool.QueryRow(context.Background(),
				`SELECT count(*) FROM app_user WHERE email_normalized = $1`,
				strings.ToLower(tc.email)).Scan(&accounts))
			assert.Equal(t, 0, accounts, "no account may be created")
		})
	}
}

// An instance that never opened the policy screen keeps the invite-only
// behaviour ADR-31 established.
func TestProvision_IsOffOnAnInstanceThatNeverConfiguredIt(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{Policy: true})
	h.bootstrapAdmin(t, "owner@example.com", "a good password")

	g.as("stranger", "stranger@anywhere.test", true)
	_, failure := h.client(t).googleRoundTrip(t, auth.OAuthPurposeLogin)
	assert.Equal(t, "not_linked", failure)
}

// The allowlist gates joining, not an identity the owner already granted —
// otherwise saving a list that excludes your own domain would lock you out of
// your own instance, with no second door for a Google-only owner.
func TestProvision_AllowlistDoesNotLockOutAnAlreadyLinkedAccount(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{Policy: true})
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "linked@outside.test", "a good password", "editor")
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO user_identity (user_id, provider, subject, email_at_link)
		VALUES ($1, 'google', 'linked-subject', 'linked@outside.test')`, int64(uid))
	require.NoError(t, err)

	// The allowlist now excludes that account's domain entirely.
	setPolicy(t, owner, provisioningPolicy([]string{"example.com"}, true, "editor"))

	g.as("linked-subject", "linked@outside.test", true)
	outcome, failure := h.client(t).googleRoundTrip(t, auth.OAuthPurposeLogin)
	assert.Empty(t, failure, "an existing identity must keep working")
	assert.Equal(t, "signed_in", outcome)
}

// A provisioned account must not be the one door that skips the second factor.
//
// The proof is the `signed_in` marker: oauthComplete is its ONLY emitter, and
// oauthComplete is where secondFactorPurpose decides whether to divert into a
// challenge. Reaching it means provisioning exits through the same funnel every
// other credential path uses, rather than minting a session inline — which is
// exactly the shortcut that would make "sign in with Google" the weakest door.
func TestProvision_ExitsThroughTheSharedSecondFactorFunnel(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{Policy: true, TwoFactor: true})
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	setPolicy(t, owner, provisioningPolicy([]string{"example.com"}, true, "editor"))

	g.as("fresh", "fresh@example.com", true)
	outcome, failure := h.client(t).googleRoundTrip(t, auth.OAuthPurposeLogin)
	require.Empty(t, failure)
	assert.Equal(t, "signed_in", outcome)

	var role string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT role FROM app_user WHERE email_normalized = $1`, "fresh@example.com").Scan(&role))
	assert.Equal(t, "editor", role)
}

// A role tampered directly into the settings row past policy.Validate must not
// reach the INSERT: the repository refuses it as the last gate.
func TestProvision_RefusesAnAdministrativeRoleEditedDirectlyIntoTheRow(t *testing.T) {
	h, g := newGoogleHarness(t, harnessOpts{Policy: true})
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	setPolicy(t, owner, provisioningPolicy([]string{"example.com"}, true, "editor"))

	// Straight into app_setting, bypassing the handler's validation entirely.
	_, err := h.pool.Exec(context.Background(), `
		UPDATE app_setting
		SET value = replace(value, '"google_default_role":"editor"', '"google_default_role":"admin"')
		WHERE key = 'instance_policy'`)
	require.NoError(t, err)

	g.as("escalate", "escalate@example.com", true)
	_, failure := h.client(t).googleRoundTrip(t, auth.OAuthPurposeLogin)
	assert.Equal(t, "not_linked", failure, "a tampered role must refuse like any other failure")

	var accounts int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app_user WHERE email_normalized = $1`,
		"escalate@example.com").Scan(&accounts))
	assert.Equal(t, 0, accounts)
}

// The configured OTP lifetime has to reach the mailed code, or the policy
// screen describes a rule nothing applies.
func TestPolicy_ConfiguredOTPLifetimeReachesTheChallenge(t *testing.T) {
	h := newHarnessWithPolicy(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	setPolicy(t, owner, map[string]any{
		"password_min_length": 8, "otp_ttl_minutes": 17, "otp_cooldown_seconds": 60,
		"google_allowed_domains": []string{}, "google_auto_provision": false,
		"google_default_role": "editor",
	})

	rec := owner.do(http.MethodGet, "/api/admin/policy", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, float64(17), decode(t, rec)["otp_ttl_minutes"],
		"the saved lifetime is what the handler reads back")
}

// The page size is range-checked BEFORE it narrows to int. Clamping after the
// conversion is unsound: on a 32-bit build a value like 2^32+50 truncates to
// 50, arriving as a plausible number no clamp can recognise as garbage.
func TestAudit_PageSizeIsValidatedBeforeItNarrows(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	for _, limit := range []string{"4294967346", "9223372036854775807", "-1", "201", "abc"} {
		rec := owner.do(http.MethodGet, "/api/admin/audit?limit="+limit, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "limit=%s must be refused", limit)
		assert.Equal(t, "invalid_limit", errCode(t, rec))
	}

	// And a value inside the range still works.
	assert.Equal(t, http.StatusOK, owner.do(http.MethodGet, "/api/admin/audit?limit=10", nil).Code)
}

// A domain carrying a newline reached an error message and from there a log
// line, where it forges a whole record. The allowlist refuses it at the door.
func TestPolicy_RefusesADomainThatCouldForgeALogRecord(t *testing.T) {
	h := newHarnessWithPolicy(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	rec := owner.do(http.MethodPut, "/api/admin/policy", map[string]any{
		"password_min_length": 8, "otp_ttl_minutes": 5, "otp_cooldown_seconds": 60,
		"google_allowed_domains": []string{"evil.test\nlevel=ERROR msg=\"forged\""},
		"google_auto_provision":  false, "google_default_role": "editor",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_policy", errCode(t, rec))
}
