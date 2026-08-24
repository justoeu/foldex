//go:build integration

package roleperm_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/roleperm"
	"foldex/internal/testdb"
)

// Mandatory: Shared holds one Postgres per test BINARY in a package-level
// sync.Once, and nothing inside a test is late enough to stop it. Without this
// the container outlives the run (CLAUDE.md §2), and internal/security fails
// the build for a package that forgets.
func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

func setup(t *testing.T) *roleperm.Repository {
	t.Helper()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))
	repo := roleperm.NewRepository(pool)
	require.NoError(t, repo.Load(context.Background()))
	return repo
}

// The migration seeds exactly the compiled matrix, so an instance that never
// opens the screen must not shift by a single entry.
func TestLoad_SeededMatrixMatchesTheCompiledOne(t *testing.T) {
	repo := setup(t)
	compiled := authctx.DefaultGrants()

	for _, role := range authctx.AllRoles {
		for _, p := range authctx.AllPermissions {
			assert.Equal(t, compiled[role][p], repo.Can(role, p),
				"role %q, permission %q", role, p)
		}
	}
}

func TestSet_RevokesAndTheSnapshotFollows(t *testing.T) {
	ctx := context.Background()
	repo := setup(t)
	require.True(t, repo.Can(authctx.RoleEditor, authctx.PermImportRun))

	require.NoError(t, repo.Set(ctx, authctx.RoleOwner, authctx.RoleEditor,
		[]authctx.Permission{authctx.PermContentWrite}))

	assert.False(t, repo.Can(authctx.RoleEditor, authctx.PermImportRun),
		"the snapshot must follow the write, or the gate keeps enforcing the old matrix")
	assert.True(t, repo.Can(authctx.RoleEditor, authctx.PermContentWrite))
	// The locked floor survives a write that did not mention it.
	assert.True(t, repo.Can(authctx.RoleEditor, authctx.PermContentRead))
}

// A fresh process must resolve to what the previous one wrote — the whole
// point of storing it rather than keeping it in memory.
func TestSet_SurvivesAReload(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	first := roleperm.NewRepository(pool)
	require.NoError(t, first.Load(ctx))
	require.NoError(t, first.Set(ctx, authctx.RoleOwner, authctx.RoleViewer, nil))

	second := roleperm.NewRepository(pool)
	require.NoError(t, second.Load(ctx))
	assert.False(t, second.Can(authctx.RoleViewer, authctx.PermBackupExport))
	assert.True(t, second.Can(authctx.RoleViewer, authctx.PermContentRead))
}

// Delete-then-insert is one transaction. A refused write must leave the
// previous set intact rather than the half that had been deleted.
func TestSet_ARefusedWriteChangesNothing(t *testing.T) {
	ctx := context.Background()
	repo := setup(t)

	err := repo.Set(ctx, authctx.RoleOwner, authctx.RoleEditor,
		[]authctx.Permission{authctx.PermContentWrite, authctx.PermRolesAssign})
	require.ErrorIs(t, err, roleperm.ErrPermissionLocked)

	assert.True(t, repo.Can(authctx.RoleEditor, authctx.PermImportRun),
		"a refusal must not have deleted the role's existing grants")
	assert.False(t, repo.Can(authctx.RoleEditor, authctx.PermRolesAssign))
}

// No write to this table can produce an instance nobody is able to repair.
func TestSet_CannotStripTheOwner(t *testing.T) {
	ctx := context.Background()
	repo := setup(t)

	err := repo.Set(ctx, authctx.RoleOwner, authctx.RoleOwner, nil)
	require.ErrorIs(t, err, roleperm.ErrRoleNotEditable)

	for _, p := range authctx.AllPermissions {
		assert.True(t, repo.Can(authctx.RoleOwner, p), "permission %q", p)
	}
}

// The rule the feature was asked for: an administrator must not be able to
// give itself, or anyone, a power its own role does not hold.
func TestSet_AdminCannotGrantOwnerLevelPowers(t *testing.T) {
	ctx := context.Background()
	repo := setup(t)

	for _, p := range []authctx.Permission{authctx.PermPolicyWrite, authctx.PermInstanceTransfer} {
		err := repo.Set(ctx, authctx.RoleAdmin, authctx.RoleAdmin,
			[]authctx.Permission{authctx.PermUsersRead, p})
		require.Error(t, err, "permission %q", p)
		assert.False(t, repo.Can(authctx.RoleAdmin, p))
	}
}

// An empty table is the state the "tabela vazia = ninguém pode nada" worry
// names. It must not be fatal: the owner is compiled, and every role keeps its
// locked floor.
func TestLoad_AnEmptyTableIsNotAnUnrecoverableInstance(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	_, err := pool.Exec(ctx, `DELETE FROM role_permission`)
	require.NoError(t, err)

	repo := roleperm.NewRepository(pool)
	require.NoError(t, repo.Load(ctx))

	for _, p := range authctx.AllPermissions {
		assert.True(t, repo.Can(authctx.RoleOwner, p),
			"the owner must survive an empty table: %q", p)
	}
	assert.True(t, repo.Can(authctx.RoleAdmin, authctx.PermRolesAssign),
		"the admin must keep the locked meta-permission, or nobody can restore the matrix")
	assert.True(t, repo.Can(authctx.RoleViewer, authctx.PermContentRead))
	assert.False(t, repo.Can(authctx.RoleEditor, authctx.PermContentWrite))
}

// A row hand-written past the API — the case that makes the lock a real
// guarantee rather than a screen that declines to offer it.
func TestLoad_ARowThatGrantsALockedPermissionIsIgnored(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	_, err := pool.Exec(ctx,
		`INSERT INTO role_permission (role, permission) VALUES ('editor', 'roles.assign')`)
	require.NoError(t, err)

	repo := roleperm.NewRepository(pool)
	require.NoError(t, repo.Load(ctx))
	assert.False(t, repo.Can(authctx.RoleEditor, authctx.PermRolesAssign),
		"resolution takes locked entries from the compiled matrix, whatever the row says")
}

// The database refuses the owner outright, so even the SQL path cannot store
// a grant for it.
func TestSchema_RefusesAnOwnerRow(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	_, err := pool.Exec(ctx,
		`INSERT INTO role_permission (role, permission) VALUES ('owner', 'content.read')`)
	assert.Error(t, err, "the CHECK constraint must refuse an owner row")
}

// A caller without roles.assign cannot write the matrix, and the check has to
// live in ValidateWrite rather than only at the route.
//
// Every other rule is inside the loop over `want`, so an EMPTY set skipped the
// caller entirely: Set(viewer, admin, nil) stripped every admin to its locked
// floor and returned nil. The route gates on roles.assign, so this was not
// reachable over HTTP — but the function is documented as the choke point a
// second entry point cannot get past, and it was not one.
func TestSet_ACallerWithoutRolesAssignIsRefused(t *testing.T) {
	ctx := context.Background()
	repo := setup(t)

	for _, caller := range []authctx.Role{authctx.RoleEditor, authctx.RoleViewer} {
		err := repo.Set(ctx, caller, authctx.RoleAdmin, nil)
		require.ErrorIs(t, err, roleperm.ErrEscalation, "caller %q", caller)
	}
	assert.True(t, repo.Can(authctx.RoleAdmin, authctx.PermUsersWrite),
		"the refused write must not have stripped the role")
}

// Set takes a per-role lock, so a second writer WAITS instead of racing.
//
// DELETE-then-INSERT under READ COMMITTED loses a revocation: the second
// transaction snapshots before the first commits, so rows the first inserted
// are invisible to its DELETE and survive it — an owner sending [] concurrently
// with an admin sending ["content.write"] leaves the role holding
// content.write, the merge of two intents that sending the FULL set exists to
// make impossible.
//
// This is asserted DETERMINISTICALLY, by holding the lock from outside and
// requiring Set to block on it. The obvious version — two goroutines racing —
// was written first and was a FALSE GREEN: removing the lock from Set left it
// passing, because the losing interleaving almost never happens in one run. A
// guard that only sometimes observes what it claims is worse than none.
func TestSet_TakesAPerRoleLockSoASecondWriterWaits(t *testing.T) {
	ctx := context.Background()
	repo := setup(t)
	pool := testdb.Shared(t)

	// Hold the same lock this role's write will ask for.
	holder, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(ctx) }()
	_, err = holder.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('foldex.role_permission'), hashtext($1))`,
		string(authctx.RoleEditor))
	require.NoError(t, err)

	blocked, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	err = repo.Set(blocked, authctx.RoleOwner, authctx.RoleEditor,
		[]authctx.Permission{authctx.PermContentWrite})
	require.Error(t, err, "Set must WAIT for the role's lock, not write straight through")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	// It blocked BEFORE the DELETE, not at commit. Without this the assertion
	// above also passes when a slow pool.Begin under load produces the same
	// deadline — and a write that deleted first and then timed out would leave
	// the role stripped.
	assert.True(t, repo.Can(authctx.RoleEditor, authctx.PermImportRun),
		"the blocked write must not have deleted anything")

	// A DIFFERENT role is not blocked by it: the lock is per role, not global,
	// so two owners editing two roles do not serialize on each other.
	other, cancelOther := context.WithTimeout(ctx, 5*time.Second)
	defer cancelOther()
	assert.NoError(t, repo.Set(other, authctx.RoleOwner, authctx.RoleViewer,
		[]authctx.Permission{authctx.PermBackupExport}))

	// And once the holder lets go, the original write goes through.
	require.NoError(t, holder.Rollback(ctx))
	require.NoError(t, repo.Set(ctx, authctx.RoleOwner, authctx.RoleEditor,
		[]authctx.Permission{authctx.PermContentWrite}))
	assert.True(t, repo.Can(authctx.RoleEditor, authctx.PermContentWrite))
}

// A revocation written by ANOTHER process reaches this one on its own.
//
// Without the ticker there is no periodic Load at all: a second replica never
// sees the change, and on the process that did write, a failed post-write
// refresh leaves the snapshot pre-revocation forever — failing OPEN.
func TestStartReloading_PicksUpAWriteThisProcessDidNotMake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	// Two repositories over one database: the second stands in for another
	// replica, and only the first performs the write.
	writer := roleperm.NewRepository(pool)
	require.NoError(t, writer.Load(ctx))
	reader := roleperm.NewRepository(pool)
	require.NoError(t, reader.Load(ctx))
	require.True(t, reader.Can(authctx.RoleEditor, authctx.PermImportRun))

	_ = reader.StartReloading(ctx, 50*time.Millisecond, slog.New(slog.DiscardHandler))
	require.NoError(t, writer.Set(ctx, authctx.RoleOwner, authctx.RoleEditor,
		[]authctx.Permission{authctx.PermContentWrite}))

	require.Eventually(t, func() bool {
		return !reader.Can(authctx.RoleEditor, authctx.PermImportRun)
	}, 5*time.Second, 25*time.Millisecond,
		"the replica that did not write must still stop honouring the revoked permission")

	// A SECOND write, after the first was already picked up. Without it the
	// test passes on a loop that ticks exactly ONCE — `for i := 0; i < 1; i++`
	// survived the whole file, because the first write always lands before the
	// single tick and the stop test then goes vacuous, its goroutine exiting
	// on its own regardless of the context.
	require.True(t, reader.Can(authctx.RoleEditor, authctx.PermContentWrite))
	require.NoError(t, writer.Set(ctx, authctx.RoleOwner, authctx.RoleEditor, nil))
	require.Eventually(t, func() bool {
		return !reader.Can(authctx.RoleEditor, authctx.PermContentWrite)
	}, 5*time.Second, 25*time.Millisecond,
		"reloading must be PERIODIC, not a single tick")
}

// The goroutine must not outlive its context, or every test that starts one
// leaks a ticker querying a pool the harness is about to close.
func TestStartReloading_StopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	repo := roleperm.NewRepository(pool)
	require.NoError(t, repo.Load(ctx))
	done := repo.StartReloading(ctx, 20*time.Millisecond, slog.New(slog.DiscardHandler))
	cancel()

	// Observed, not inferred: a goroutine COUNT also moves with the pool and
	// the driver, so it reports a leak that is not there and misses one that
	// is.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the reload goroutine outlived its context")
	}
}

// The MIGRATION's own seed matches the compiled matrix.
//
// Deliberately on a fresh database with NO Reset: `testdb.Reset` reseeds the
// table, and it derives its rows from authctx — so every other test here would
// stay green while migration 000039 said something else entirely. A permission
// added to the compiled matrix without a migration is exactly the drift this
// catches, and it surfaces otherwise as a 403 on a fresh install and nowhere
// on a developer's machine.
func TestMigration_SeedsTheEditableHalfOfTheCompiledMatrix(t *testing.T) {
	ctx := context.Background()
	// Its own container: this must observe the post-migration state, which the
	// shared database has already had Reset over.
	pool := testdb.New(t)

	repo := roleperm.NewRepository(pool)
	require.NoError(t, repo.Load(ctx))

	compiled := authctx.DefaultGrants()
	for _, role := range authctx.AllRoles {
		for _, p := range authctx.AllPermissions {
			assert.Equal(t, compiled[role][p], repo.Can(role, p),
				"migration 000039 disagrees with the compiled matrix at role %q, permission %q",
				role, p)
		}
	}

	// And it stores only the EDITABLE half: a locked row would be a second
	// source of truth for the part resolution always takes from the code.
	rows, err := pool.Query(ctx, `SELECT permission FROM role_permission`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var p string
		require.NoError(t, rows.Scan(&p))
		assert.False(t, authctx.IsPermissionLocked(authctx.Permission(p)),
			"the migration seeds locked permission %q; resolution ignores it, so the "+
				"row can only ever disagree with the code", p)
	}
	require.NoError(t, rows.Err())
}
