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
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "super-secret-master", nil, nil))
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
	current := "super-secret-master"
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "rotated-master-pass", &current, nil))
	ok, _, err = repo.VerifyMaster(ctx, uid, "rotated-master-pass")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, _, err = repo.VerifyMaster(ctx, uid, "super-secret-master")
	require.NoError(t, err)
	assert.False(t, ok, "old master no longer valid after rotation")

	// Clear.
	_, err = repo.ClearMasterPassword(ctx, uid, "rotated-master-pass")
	require.NoError(t, err)
	configured, err = repo.MasterPasswordConfigured(ctx, uid)
	require.NoError(t, err)
	assert.False(t, configured)

	// Clearing again is a harmless no-op.
	_, err = repo.ClearMasterPassword(ctx, uid, "anything")
	require.NoError(t, err)
}

func TestRepository_MasterHint_Tristate(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := settings.NewRepository(pool)
	ctx := context.Background()

	// Set password + hint.
	hint := "starts with s"
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "first-pass-1", nil, &hint))
	got, err := repo.MasterPasswordHint(ctx, uid)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "starts with s", *got)

	// Change password with nil hint → hint PRESERVED (not wiped).
	pass1 := "first-pass-1"
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "second-pass-2", &pass1, nil))
	got, err = repo.MasterPasswordHint(ctx, uid)
	require.NoError(t, err)
	require.NotNil(t, got, "nil hint on change must keep the existing hint")
	assert.Equal(t, "starts with s", *got)

	// Explicit empty hint → cleared.
	empty := ""
	pass2 := "second-pass-2"
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "third-pass-3", &pass2, &empty))
	got, err = repo.MasterPasswordHint(ctx, uid)
	require.NoError(t, err)
	assert.Nil(t, got, "empty hint must clear it")

	// Clearing the master removes any hint too.
	hint2 := "another"
	pass3 := "third-pass-3"
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "fourth-pass-4", &pass3, &hint2))
	_, err = repo.ClearMasterPassword(ctx, uid, "fourth-pass-4")
	require.NoError(t, err)
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
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "original-pass", nil, &seedHint))

	blocker, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var locked int64
	require.NoError(t, blocker.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 FOR UPDATE`, int64(uid)).Scan(&locked))

	// Queued first: commits hint = target-pass.
	hint := "target-pass"
	original := "original-pass"
	resB := make(chan error, 1)
	go func() { resB <- repo.SetMasterPassword(ctx, uid, "other-pass", &original, &hint) }()
	testdb.WaitForBlockedSQLCount(t, pool, "FOR NO KEY UPDATE", 1)

	// Queued second: rotates the password TO target-pass with a nil hint
	// ("leave the existing hint untouched"), proving the password the
	// queued-ahead writer installed so the hint check is what refuses it.
	other := "other-pass"
	resA := make(chan error, 1)
	go func() { resA <- repo.SetMasterPassword(ctx, uid, "target-pass", &other, nil) }()
	testdb.WaitForBlockedSQLCount(t, pool, "FOR NO KEY UPDATE", 2)

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

// The first-set race: two concurrent rotations that both read "nothing
// configured" outside a lock would both land, the second silently replacing
// the master the first just installed — with no proof of either. The
// configured check and the current-password proof must run under the
// app_user row lock, so exactly one first-set wins and the loser is told a
// current password is now required.
func TestSetMasterPassword_ConcurrentFirstSetOnlyOneWins(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "first-set-race@test.local", "admin")
	repo := settings.NewRepository(pool)

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, pw := range []string{"master-from-tab-a", "master-from-tab-b"} {
		go func() {
			<-start
			results <- repo.SetMasterPassword(ctx, uid, pw, nil, nil)
		}()
	}
	close(start)

	var succeeded, refused int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case assert.ErrorIs(t, err, settings.ErrCurrentPasswordRequired):
			refused++
		}
	}
	assert.Equal(t, 1, succeeded, "exactly one unauthenticated first-set may land")
	assert.Equal(t, 1, refused, "the loser must be refused for want of the now-current password")
}

// A proof of the PREVIOUS master must not clear or rotate the current one:
// verification reads the hash under the same lock the write takes.
func TestMasterPassword_StaleCurrentIsRefused(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "stale-master@test.local", "admin")
	repo := settings.NewRepository(pool)

	require.NoError(t, repo.SetMasterPassword(ctx, uid, "master-h1-pass", nil, nil))
	h1 := "master-h1-pass"
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "master-h2-pass", &h1, nil))

	err := repo.SetMasterPassword(ctx, uid, "master-h3-pass", &h1, nil)
	assert.ErrorIs(t, err, settings.ErrWrongMasterPassword,
		"rotating with the previous master must fail after H1 -> H2")

	_, err = repo.ClearMasterPassword(ctx, uid, "master-h1-pass")
	assert.ErrorIs(t, err, settings.ErrWrongMasterPassword,
		"clearing with the previous master must fail after H1 -> H2")

	configured, err := repo.ClearMasterPassword(ctx, uid, "master-h2-pass")
	require.NoError(t, err)
	assert.True(t, configured)

	configured, err = repo.ClearMasterPassword(ctx, uid, "anything")
	require.NoError(t, err)
	assert.False(t, configured, "clearing an unconfigured master is an idempotent no-op")
}

func TestRepository_ClosedPoolErrors(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := settings.NewRepository(pool)
	ctx := context.Background()
	require.NoError(t, repo.SetMasterPassword(ctx, uid, "seeded-master", nil, nil))
	pool.Close()

	_, err := repo.MasterPasswordConfigured(ctx, uid)
	require.Error(t, err)

	_, err = repo.MasterPasswordHint(ctx, uid)
	require.Error(t, err)

	_, _, err = repo.VerifyMaster(ctx, uid, "x")
	require.Error(t, err)

	require.Error(t, repo.SetMasterPassword(ctx, uid, "another-one", nil, nil))
	_, err = repo.ClearMasterPassword(ctx, uid, "seeded-master")
	require.Error(t, err)
}
