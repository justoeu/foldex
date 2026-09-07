//go:build integration

package settings_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/settings"
	"foldex/internal/testdb"
)

func waitForBlockedSQLCount(t *testing.T, pool *pgxpool.Pool, fragment string, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var blocked int
		err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query LIKE '%' || $1 || '%'`, fragment).Scan(&blocked)
		return err == nil && blocked >= want
	}, 3*time.Second, 10*time.Millisecond,
		"%d queries did not block on the expected row lock: %s", want, fragment)
}

func TestRepository_MasterPassword_Lifecycle(t *testing.T) {
	pool := testdb.Shared(t)

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
	pool := testdb.Shared(t)

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

// INV-066/067 under self-race: the hint-equality pre-check must observe the
// same committed state the write lands on. With the read on the pool before
// the row lock, tab 2 can commit hint == password between tab 1's read and
// its write, persisting the exact equality the check exists to refuse.
func TestSetMasterPassword_HintEqualityRunsUnderTheRowLock(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "hint-race@test.local", "admin")
	repo := settings.NewRepository(pool)

	seedHint := "old hint"
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "original-pass", &seedHint))

	blocker, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var locked int64
	require.NoError(t, blocker.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 FOR UPDATE`, int64(uid)).Scan(&locked))

	// Queued first: commits hint = target-pass.
	hint := "target-pass"
	resB := make(chan error, 1)
	go func() { resB <- repo.SetMasterPassword(ctx, uid, "other-pass", &hint) }()
	waitForBlockedSQLCount(t, pool, "FOR NO KEY UPDATE", 1)

	// Queued second: rotates the password TO target-pass with a nil hint
	// ("leave the existing hint untouched").
	resA := make(chan error, 1)
	go func() { resA <- repo.SetMasterPassword(ctx, uid, "target-pass", nil) }()
	waitForBlockedSQLCount(t, pool, "FOR NO KEY UPDATE", 2)

	require.NoError(t, blocker.Commit(ctx))
	require.NoError(t, <-resB)
	assert.ErrorIs(t, <-resA, settings.ErrHintMatchesPassword,
		"the check must see the hint the queued-ahead writer committed, not a pre-lock snapshot")

	stored, err := repo.MasterPasswordHint(ctx, uid)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "target-pass", *stored)
	ok, present, err := repo.VerifyMaster(ctx, uid, "other-pass")
	require.NoError(t, err)
	assert.True(t, present)
	assert.True(t, ok, "the refused write must not have rotated the password")
}

func TestRepository_ClosedPoolErrors(t *testing.T) {
	pool := testdb.Shared(t)

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
