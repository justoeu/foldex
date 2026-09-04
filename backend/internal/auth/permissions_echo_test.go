package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

type staticGrants map[authctx.Role]map[authctx.Permission]bool

func (g staticGrants) Can(role authctx.Role, p authctx.Permission) bool {
	return g[role][p]
}

func TestPermissionsFor_NilGrantsUsesTheCompiledRole(t *testing.T) {
	h := &Handler{}
	viewer := h.permissionsFor(authctx.RoleViewer)
	assert.Contains(t, viewer, authctx.PermContentRead)
	assert.NotContains(t, viewer, authctx.PermContentWrite)

	editor := h.permissionsFor(authctx.RoleEditor)
	assert.Contains(t, editor, authctx.PermContentRead)
	assert.Contains(t, editor, authctx.PermContentWrite)
}

func TestPermissionsFor_LiveGrantsOverrideTheCompiledRole(t *testing.T) {
	h := &Handler{grants: staticGrants{
		authctx.RoleViewer: {
			authctx.PermContentRead:  true,
			authctx.PermContentWrite: true,
		},
	}}
	perms := h.permissionsFor(authctx.RoleViewer)
	require.Contains(t, perms, authctx.PermContentWrite)
	assert.NotContains(t, perms, authctx.PermBackupExport)
}

func TestAuthenticatedPayload_AlwaysEmitsPermissions(t *testing.T) {
	h := &Handler{}
	payload := h.authenticatedPayload(User{Role: authctx.RoleViewer}, "csrf")
	require.NotNil(t, payload.Permissions)
	assert.Contains(t, payload.Permissions, authctx.PermContentRead)
	assert.NotContains(t, payload.Permissions, authctx.PermContentWrite)
}
