//go:build integration

package settings_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/settings"
	"foldex/internal/testdb"
)

func TestRepository_MasterPassword_Lifecycle(t *testing.T) {
	pool := testdb.New(t)
	repo := settings.NewRepository(pool)
	ctx := context.Background()

	// Nothing configured initially.
	configured, err := repo.MasterPasswordConfigured(ctx)
	require.NoError(t, err)
	assert.False(t, configured)

	ok, present, err := repo.VerifyMaster(ctx, "anything")
	require.NoError(t, err)
	assert.False(t, present, "no master configured → present=false")
	assert.False(t, ok)

	// Set it.
	require.NoError(t, repo.SetMasterPassword(ctx, "super-secret-master", nil))
	configured, err = repo.MasterPasswordConfigured(ctx)
	require.NoError(t, err)
	assert.True(t, configured)

	// Verify right/wrong.
	ok, present, err = repo.VerifyMaster(ctx, "super-secret-master")
	require.NoError(t, err)
	assert.True(t, present)
	assert.True(t, ok)

	ok, present, err = repo.VerifyMaster(ctx, "wrong")
	require.NoError(t, err)
	assert.True(t, present)
	assert.False(t, ok)

	// Upsert (change) is idempotent on the key — still exactly one row, new value.
	require.NoError(t, repo.SetMasterPassword(ctx, "rotated-master-pass", nil))
	ok, _, err = repo.VerifyMaster(ctx, "rotated-master-pass")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, _, err = repo.VerifyMaster(ctx, "super-secret-master")
	require.NoError(t, err)
	assert.False(t, ok, "old master no longer valid after rotation")

	// Clear.
	require.NoError(t, repo.ClearMasterPassword(ctx))
	configured, err = repo.MasterPasswordConfigured(ctx)
	require.NoError(t, err)
	assert.False(t, configured)

	// Clearing again is a harmless no-op.
	require.NoError(t, repo.ClearMasterPassword(ctx))
}

func TestRepository_MasterHint_Tristate(t *testing.T) {
	pool := testdb.New(t)
	repo := settings.NewRepository(pool)
	ctx := context.Background()

	// Set password + hint.
	hint := "starts with s"
	require.NoError(t, repo.SetMasterPassword(ctx, "first-pass-1", &hint))
	got, err := repo.MasterPasswordHint(ctx)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "starts with s", *got)

	// Change password with nil hint → hint PRESERVED (not wiped).
	require.NoError(t, repo.SetMasterPassword(ctx, "second-pass-2", nil))
	got, err = repo.MasterPasswordHint(ctx)
	require.NoError(t, err)
	require.NotNil(t, got, "nil hint on change must keep the existing hint")
	assert.Equal(t, "starts with s", *got)

	// Explicit empty hint → cleared.
	empty := ""
	require.NoError(t, repo.SetMasterPassword(ctx, "third-pass-3", &empty))
	got, err = repo.MasterPasswordHint(ctx)
	require.NoError(t, err)
	assert.Nil(t, got, "empty hint must clear it")

	// Clearing the master removes any hint too.
	hint2 := "another"
	require.NoError(t, repo.SetMasterPassword(ctx, "fourth-pass-4", &hint2))
	require.NoError(t, repo.ClearMasterPassword(ctx))
	got, err = repo.MasterPasswordHint(ctx)
	require.NoError(t, err)
	assert.Nil(t, got)
}
