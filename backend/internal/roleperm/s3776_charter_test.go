package roleperm

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/pkg/authctx"
)

func TestS3776Charter_Resolve(t *testing.T) {
	t.Parallel()

	t.Run("empty store leaves the owner whole", func(t *testing.T) {
		t.Parallel()
		g := Resolve(nil)
		for _, p := range authctx.AllPermissions {
			assert.True(t, g.Can(authctx.RoleOwner, p), p)
		}
	})

	t.Run("locked permissions ignore the store", func(t *testing.T) {
		t.Parallel()
		g := Resolve(map[authctx.Role][]authctx.Permission{
			authctx.RoleEditor: {authctx.PermRolesAssign, authctx.PermPolicyWrite},
			authctx.RoleViewer: {authctx.PermBackupExport},
		})
		assert.False(t, g.Can(authctx.RoleEditor, authctx.PermRolesAssign))
		assert.False(t, g.Can(authctx.RoleEditor, authctx.PermPolicyWrite))
		assert.True(t, g.Can(authctx.RoleViewer, authctx.PermContentRead))
	})

	t.Run("editable permissions are exactly what is stored", func(t *testing.T) {
		t.Parallel()
		g := Resolve(map[authctx.Role][]authctx.Permission{
			authctx.RoleEditor: {authctx.PermContentWrite},
		})
		assert.True(t, g.Can(authctx.RoleEditor, authctx.PermContentWrite))
		assert.False(t, g.Can(authctx.RoleEditor, authctx.PermBackupExport))
	})

	t.Run("unknown role is powerless", func(t *testing.T) {
		t.Parallel()
		g := Resolve(map[authctx.Role][]authctx.Permission{
			authctx.Role("superuser"): {authctx.PermContentWrite},
		})
		for _, p := range authctx.AllPermissions {
			assert.False(t, g.Can(authctx.Role("superuser"), p), p)
		}
	})
}
