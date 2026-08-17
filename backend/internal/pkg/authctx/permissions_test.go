package authctx_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

// The matrix is the whole authorization contract, so it is asserted entry by
// entry rather than by ranging over the map under test — a test that derived
// its expectations from the same map would pass for any matrix, including one
// where a typo handed a viewer PermPolicyWrite.
func TestMatrix_IsExactlyTheDocumentedGrid(t *testing.T) {
	want := map[authctx.Role][]authctx.Permission{
		authctx.RoleOwner: {
			authctx.PermContentRead, authctx.PermContentWrite,
			authctx.PermBackupExport, authctx.PermBackupRestore, authctx.PermImportRun,
			authctx.PermUsersRead, authctx.PermUsersWrite, authctx.PermRolesAssign,
			authctx.PermInvitesRead, authctx.PermInvitesWrite,
			authctx.PermAuditRead, authctx.PermPolicyRead, authctx.PermPolicyWrite,
			authctx.PermInstanceTransfer,
		},
		authctx.RoleAdmin: {
			authctx.PermContentRead, authctx.PermContentWrite,
			authctx.PermBackupExport, authctx.PermBackupRestore, authctx.PermImportRun,
			authctx.PermUsersRead, authctx.PermUsersWrite, authctx.PermRolesAssign,
			authctx.PermInvitesRead, authctx.PermInvitesWrite,
			authctx.PermAuditRead, authctx.PermPolicyRead,
		},
		authctx.RoleEditor: {
			authctx.PermContentRead, authctx.PermContentWrite,
			authctx.PermBackupExport, authctx.PermBackupRestore, authctx.PermImportRun,
		},
		authctx.RoleViewer: {
			authctx.PermContentRead, authctx.PermBackupExport,
		},
	}
	for role, permissions := range want {
		assert.Equal(t, permissions, role.Permissions(), "role %q", role)
	}
}

// An admin who could rewrite the password policy or the OAuth allowlist could
// lower the instance's floor and then walk in through it, so the two
// owner-only entries are locked from the admin side as well.
func TestMatrix_AdminCannotWritePolicyOrTransferTheInstance(t *testing.T) {
	assert.False(t, authctx.RoleAdmin.Can(authctx.PermPolicyWrite))
	assert.False(t, authctx.RoleAdmin.Can(authctx.PermInstanceTransfer))
	assert.True(t, authctx.RoleAdmin.Can(authctx.PermPolicyRead),
		"an admin still has to be able to see the rules it manages people under")
}

// The viewer's whole reason to exist: same rows as an editor, no mutation.
func TestMatrix_ViewerReadsButNeverWrites(t *testing.T) {
	assert.True(t, authctx.RoleViewer.Can(authctx.PermContentRead))
	for _, p := range []authctx.Permission{
		authctx.PermContentWrite, authctx.PermBackupRestore, authctx.PermImportRun,
	} {
		assert.False(t, authctx.RoleViewer.Can(p), "viewer must not hold %q", p)
	}
}

// A role string that reached the process without passing the CHECK constraint —
// a hand-edited row, a future migration, a bug — must be powerless rather than
// unconstrained. The map lookup returns a nil set, so every answer is false.
func TestMatrix_UnknownRoleFailsClosed(t *testing.T) {
	for _, r := range []authctx.Role{"", "user", "root", "OWNER"} {
		assert.False(t, r.Valid(), "role %q must not validate", r)
		assert.Empty(t, r.Permissions(), "role %q must hold nothing", r)
		for _, p := range authctx.AllPermissions {
			assert.False(t, r.Can(p), "role %q must not hold %q", r, p)
		}
	}
}

// AllPermissions drives the administration screen's matrix COLUMNS, and
// Role.Permissions() iterates it — so a permission missing from the slice would
// silently vanish from both the screen and every role's reported set while the
// middleware kept enforcing it.
//
// The expected contents are spelled out literally rather than derived from
// AllPermissions itself. A test that ranged over the slice to check the slice
// cannot fail: Permissions() is a subset of it by construction, so the obvious
// "granted but unlisted" assertion is unreachable. Only a hardcoded list
// catches a permission that was added to the matrix and forgotten here.
func TestAllPermissions_IsExactlyTheDeclaredVocabulary(t *testing.T) {
	assert.Equal(t, []authctx.Permission{
		"content.read", "content.write",
		"backup.export", "backup.restore", "import.run",
		"users.read", "users.write", "roles.assign",
		"invites.read", "invites.write",
		"audit.read", "policy.read", "policy.write",
		"instance.transfer",
	}, authctx.AllPermissions)

	seen := map[authctx.Permission]bool{}
	for _, p := range authctx.AllPermissions {
		require.False(t, seen[p], "duplicate entry %q", p)
		seen[p] = true
	}
	assert.Equal(t, []authctx.Role{"owner", "admin", "editor", "viewer"}, authctx.AllRoles)
}
