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

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := settings.NewRepository(pool)
	ctx := context.Background()

	// Nothing configured initially.
	configured, err := repo.MasterPasswordConfigured(ctx, uid)
	require.NoError(t, err)
	assert.False(t, configured)

	ok, present, err := repo.VerifyMaster(ctx, uid, "anything")
	require.NoError(t, err)
	assert.False(t, present, "no master configured → present=false")
	assert.False(t, ok)

	// Set it.
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "super-secret-master", nil))
	configured, err = repo.MasterPasswordConfigured(ctx, uid)
	require.NoError(t, err)
	assert.True(t, configured)

	// Verify right/wrong.
	ok, present, err = repo.VerifyMaster(ctx, uid, "super-secret-master")
	require.NoError(t, err)
	assert.True(t, present)
	assert.True(t, ok)

	ok, present, err = repo.VerifyMaster(ctx, uid, "wrong")
	require.NoError(t, err)
	assert.True(t, present)
	assert.False(t, ok)

	// Upsert (change) is idempotent on the key — still exactly one row, new value.
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "rotated-master-pass", nil))
	ok, _, err = repo.VerifyMaster(ctx, uid, "rotated-master-pass")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, _, err = repo.VerifyMaster(ctx, uid, "super-secret-master")
	require.NoError(t, err)
	assert.False(t, ok, "old master no longer valid after rotation")

	// Clear.
	require.NoError(t, repo.ClearMasterPassword(ctx, uid))
	configured, err = repo.MasterPasswordConfigured(ctx, uid)
	require.NoError(t, err)
	assert.False(t, configured)

	// Clearing again is a harmless no-op.
	require.NoError(t, repo.ClearMasterPassword(ctx, uid))
}

func TestRepository_MasterHint_Tristate(t *testing.T) {
	pool := testdb.New(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := settings.NewRepository(pool)
	ctx := context.Background()

	// Set password + hint.
	hint := "starts with s"
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "first-pass-1", &hint))
	got, err := repo.MasterPasswordHint(ctx, uid)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "starts with s", *got)

	// Change password with nil hint → hint PRESERVED (not wiped).
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "second-pass-2", nil))
	got, err = repo.MasterPasswordHint(ctx, uid)
	require.NoError(t, err)
	require.NotNil(t, got, "nil hint on change must keep the existing hint")
	assert.Equal(t, "starts with s", *got)

	// Explicit empty hint → cleared.
	empty := ""
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "third-pass-3", &empty))
	got, err = repo.MasterPasswordHint(ctx, uid)
	require.NoError(t, err)
	assert.Nil(t, got, "empty hint must clear it")

	// Clearing the master removes any hint too.
	hint2 := "another"
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "fourth-pass-4", &hint2))
	require.NoError(t, repo.ClearMasterPassword(ctx, uid))
	got, err = repo.MasterPasswordHint(ctx, uid)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestRepository_ClosedPoolErrors(t *testing.T) {
	pool := testdb.New(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := settings.NewRepository(pool)
	ctx := context.Background()
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "seeded-master", nil))
	pool.Close()

	_, err := repo.MasterPasswordConfigured(ctx, uid)
	require.Error(t, err)

	_, err = repo.MasterPasswordHint(ctx, uid)
	require.Error(t, err)

	_, _, err = repo.VerifyMaster(ctx, uid, "x")
	require.Error(t, err)

	require.Error(t, repo.SetMasterPassword(ctx, uid, "another-one", nil))
	require.Error(t, repo.ClearMasterPassword(ctx, uid))
}
