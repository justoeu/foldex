package roleperm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/roleperm"
)

// An empty table must not be an instance nobody can repair. This is the whole
// reason the owner is resolved from the compiled matrix and never stored.
func TestResolve_EmptyStoreLeavesTheOwnerWhole(t *testing.T) {
	g := roleperm.Resolve(nil)

	for _, p := range authctx.AllPermissions {
		assert.True(t, g.Can(authctx.RoleOwner, p),
			"the owner must hold %q with nothing stored, or a truncated table "+
				"is an instance recoverable only by direct SQL", p)
	}
}

// A locked permission comes from the compiled matrix whatever the row says —
// in BOTH directions. Otherwise the lock is merely "the current screen does
// not offer it", which a hand-written INSERT walks straight past.
func TestResolve_LockedPermissionsIgnoreTheStore(t *testing.T) {
	// The store tries to hand an editor the meta-permission and to take
	// content.read away from a viewer.
	g := roleperm.Resolve(map[authctx.Role][]authctx.Permission{
		authctx.RoleEditor: {authctx.PermRolesAssign, authctx.PermPolicyWrite, authctx.PermInstanceTransfer},
		authctx.RoleViewer: {authctx.PermBackupExport},
	})

	assert.False(t, g.Can(authctx.RoleEditor, authctx.PermRolesAssign),
		"roles.assign must not be grantable: a role that can be given the power "+
			"to grant would grant itself everything else in one further step")
	assert.False(t, g.Can(authctx.RoleEditor, authctx.PermPolicyWrite))
	assert.False(t, g.Can(authctx.RoleEditor, authctx.PermInstanceTransfer))
	assert.True(t, g.Can(authctx.RoleViewer, authctx.PermContentRead),
		"content.read must not be removable: an account that cannot read its "+
			"own library is broken, not restricted")
}

// The editable half is exactly what the table holds. An absent editable
// permission is REVOKED, not defaulted — that is what the table is for.
func TestResolve_EditablePermissionsAreExactlyWhatIsStored(t *testing.T) {
	g := roleperm.Resolve(map[authctx.Role][]authctx.Permission{
		authctx.RoleEditor: {authctx.PermContentWrite},
	})

	assert.True(t, g.Can(authctx.RoleEditor, authctx.PermContentWrite))
	assert.False(t, g.Can(authctx.RoleEditor, authctx.PermBackupExport),
		"an editable permission absent from the store is revoked")
	assert.False(t, g.Can(authctx.RoleEditor, authctx.PermImportRun))
}

func TestResolve_UnknownRoleIsPowerless(t *testing.T) {
	g := roleperm.Resolve(map[authctx.Role][]authctx.Permission{
		authctx.Role("superuser"): {authctx.PermContentWrite, authctx.PermUsersWrite},
	})
	for _, p := range authctx.AllPermissions {
		assert.False(t, g.Can(authctx.Role("superuser"), p),
			"authorization must fail CLOSED for a role the matrix does not know")
	}
}

// Default is the historical behaviour, exactly. An instance that never opens
// the screen must not shift by a single entry.
func TestDefault_MatchesTheCompiledMatrix(t *testing.T) {
	g := roleperm.Default()
	compiled := authctx.DefaultGrants()

	for _, role := range authctx.AllRoles {
		for _, p := range authctx.AllPermissions {
			assert.Equal(t, compiled[role][p], g.Can(role, p),
				"role %q, permission %q", role, p)
		}
	}
}

func TestValidateWrite_RefusesTheOwnerRow(t *testing.T) {
	err := roleperm.ValidateWrite(roleperm.Default(), authctx.RoleOwner, authctx.RoleOwner,
		[]authctx.Permission{authctx.PermContentWrite})
	assert.ErrorIs(t, err, roleperm.ErrRoleNotEditable)
}

func TestValidateWrite_RefusesALockedPermission(t *testing.T) {
	for _, p := range []authctx.Permission{
		authctx.PermRolesAssign, authctx.PermPolicyWrite,
		authctx.PermInstanceTransfer, authctx.PermContentRead,
	} {
		err := roleperm.ValidateWrite(roleperm.Default(), authctx.RoleOwner, authctx.RoleEditor,
			[]authctx.Permission{p})
		assert.ErrorIs(t, err, roleperm.ErrPermissionLocked, "permission %q", p)
	}
}

// The rule that answers "an admin must not give itself owner-level powers",
// stated in terms of the CALLER so a permission unlocked later is covered by
// construction rather than by remembering to extend a list.
func TestValidateWrite_CallerCannotGrantWhatItDoesNotHold(t *testing.T) {
	// A matrix where the admin has lost import.run, so it is editable-but-not-held.
	current := roleperm.Resolve(map[authctx.Role][]authctx.Permission{
		authctx.RoleAdmin:  {authctx.PermUsersRead, authctx.PermUsersWrite},
		authctx.RoleEditor: {authctx.PermContentWrite},
	})

	err := roleperm.ValidateWrite(current, authctx.RoleAdmin, authctx.RoleEditor,
		[]authctx.Permission{authctx.PermImportRun})
	assert.ErrorIs(t, err, roleperm.ErrEscalation)

	// The owner holds it, so the same write is fine from the owner.
	require.NoError(t, roleperm.ValidateWrite(roleperm.Default(), authctx.RoleOwner,
		authctx.RoleEditor, []authctx.Permission{authctx.PermImportRun}))
}

// A typo silently dropped would read as a save that changed nothing.
func TestValidateWrite_RefusesAnUnknownPermission(t *testing.T) {
	err := roleperm.ValidateWrite(roleperm.Default(), authctx.RoleOwner, authctx.RoleEditor,
		[]authctx.Permission{authctx.Permission("content.writ")})
	assert.ErrorIs(t, err, roleperm.ErrUnknownPermission)
}

func TestValidateWrite_AcceptsAnOrdinaryEdit(t *testing.T) {
	require.NoError(t, roleperm.ValidateWrite(roleperm.Default(), authctx.RoleOwner,
		authctx.RoleViewer, []authctx.Permission{authctx.PermBackupExport, authctx.PermContentWrite}))
}

// Revoking everything editable is legal, and the locked floor is what keeps
// the resulting role usable rather than inert.
func TestResolve_RevokingEverythingLeavesTheLockedFloor(t *testing.T) {
	g := roleperm.Resolve(map[authctx.Role][]authctx.Permission{authctx.RoleViewer: {}})
	assert.True(t, g.Can(authctx.RoleViewer, authctx.PermContentRead))
	assert.False(t, g.Can(authctx.RoleViewer, authctx.PermBackupExport))
}

func TestPermissions_AreReturnedInDisplayOrder(t *testing.T) {
	got := roleperm.Default().Permissions(authctx.RoleViewer)
	assert.Equal(t, []authctx.Permission{authctx.PermContentRead, authctx.PermBackupExport}, got)
}

// A TYPED nil is not `== nil` once it is inside an interface, and every caller
// of OrDefault holds a concrete *Repository.
//
// This is the exact defect that collapsing four `if grants == nil` checks into
// one function introduced: four correct CONCRETE comparisons became interface
// ones, and the admin gate panicked on the first request of any handler built
// with a nil store — which is every test that does not care about configured
// permissions, and any deployment where the store failed to wire.
func TestOrDefault_SurvivesATypedNil(t *testing.T) {
	var typedNil *roleperm.Repository
	got := roleperm.OrDefault(typedNil)

	require.NotNil(t, got)
	// It must not merely be non-nil — it must ANSWER, which a typed nil does
	// not: it panics. And it must answer with the compiled matrix.
	assert.True(t, got.Can(authctx.RoleOwner, authctx.PermInstanceTransfer))
	assert.True(t, got.Can(authctx.RoleEditor, authctx.PermContentWrite))
	assert.False(t, got.Can(authctx.RoleViewer, authctx.PermContentWrite))
}

func TestOrDefault_PassesARealMatrixThrough(t *testing.T) {
	// Stripped: if OrDefault substituted the default here, a configured
	// instance would silently serve the compiled matrix instead.
	stripped := roleperm.Resolve(map[authctx.Role][]authctx.Permission{})
	got := roleperm.OrDefault(stripped)

	assert.False(t, got.Can(authctx.RoleEditor, authctx.PermContentWrite),
		"a real matrix must pass through untouched")
}

func TestOrDefault_SubstitutesAnUntypedNil(t *testing.T) {
	assert.True(t, roleperm.OrDefault(nil).Can(authctx.RoleEditor, authctx.PermContentWrite))
}
