//go:build integration

package backupstatus_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backupstatus"
	"foldex/internal/testdb"
)

const artifactKey = "backups/dump/2026/09/09/foldex.dump.age"

/*
 * The ceiling from INV-187, and the reason it is counted in SQL.
 *
 * Reading the count in Go and then inserting leaves a window: two requests that
 * interleave between the read and the write both see two-used and both proceed,
 * which is the fourth download the ceiling exists to refuse. Folding the count
 * into one INSERT ... SELECT ... WHERE (count) < n does not close it either —
 * under READ COMMITTED each statement takes its own snapshot. The count and the
 * insert therefore share one transaction behind pg_advisory_xact_lock, which
 * TestReserveDownload_ConcurrentRequestsCannotOverrunTheCeiling proves.
 */
func TestReserveDownload_ThreePerAdministratorAndNoMore(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	repo := backupstatus.NewRepository(pool)

	uid := testdb.SeedUser(t, pool, "admin@example.com", "admin")
	var runID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at, artifact_key)
		VALUES ('dump', 'succeeded', now(), now(), now(), $1) RETURNING id`, artifactKey).Scan(&runID))

	for i := 1; i <= backupstatus.MaxDownloadsPerAdmin; i++ {
		id, err := repo.ReserveDownload(ctx, runID, uid, artifactKey)
		require.NoError(t, err, "download %d must be allowed", i)
		assert.NotZero(t, id)

		budget, err := repo.DownloadBudgetFor(ctx, runID, uid, artifactKey)
		require.NoError(t, err)
		assert.Equal(t, i, budget.Used)
		assert.Equal(t, backupstatus.MaxDownloadsPerAdmin-i, budget.Available)
	}

	_, err := repo.ReserveDownload(ctx, runID, uid, artifactKey)
	assert.ErrorIs(t, err, backupstatus.ErrDownloadBudgetSpent)

	budget, err := repo.DownloadBudgetFor(ctx, runID, uid, artifactKey)
	require.NoError(t, err)
	assert.Equal(t, 0, budget.Available, "available must floor at zero, never go negative")
}

// Per ADMINISTRATOR, not per instance: a shared pool would let the first admin
// to hit a network hiccup spend everyone else's budget.
func TestReserveDownload_EachAdministratorHasTheirOwnBudget(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	repo := backupstatus.NewRepository(pool)

	a := testdb.SeedUser(t, pool, "a@example.com", "admin")
	b := testdb.SeedUser(t, pool, "b@example.com", "admin")
	var runID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at, artifact_key)
		VALUES ('dump', 'succeeded', now(), now(), now(), $1) RETURNING id`, artifactKey).Scan(&runID))

	for i := 0; i < backupstatus.MaxDownloadsPerAdmin; i++ {
		_, err := repo.ReserveDownload(ctx, runID, a, artifactKey)
		require.NoError(t, err)
	}
	_, err := repo.ReserveDownload(ctx, runID, a, artifactKey)
	require.ErrorIs(t, err, backupstatus.ErrDownloadBudgetSpent)

	budget, err := repo.DownloadBudgetFor(ctx, runID, b, artifactKey)
	require.NoError(t, err)
	assert.Equal(t, backupstatus.MaxDownloadsPerAdmin, budget.Available,
		"one administrator exhausting their budget must not spend another's")
	_, err = repo.ReserveDownload(ctx, runID, b, artifactKey)
	assert.NoError(t, err)
}

/*
 * Two artifacts are two budgets. The key is part of the tally because a
 * user_zip run points at N objects, so "which artifact was downloaded" is not
 * derivable from the run id — and a per-run tally would let one object's
 * downloads lock out every other object of the same run.
 */
func TestReserveDownload_TheBudgetIsPerArtifactNotPerRun(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	repo := backupstatus.NewRepository(pool)

	uid := testdb.SeedUser(t, pool, "admin@example.com", "admin")
	var runID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at)
		VALUES ('user_zip', 'succeeded', now(), now(), now()) RETURNING id`).Scan(&runID))

	for i := 0; i < backupstatus.MaxDownloadsPerAdmin; i++ {
		_, err := repo.ReserveDownload(ctx, runID, uid, "backups/users/7.zip.age")
		require.NoError(t, err)
	}
	_, err := repo.ReserveDownload(ctx, runID, uid, "backups/users/7.zip.age")
	require.ErrorIs(t, err, backupstatus.ErrDownloadBudgetSpent)

	_, err = repo.ReserveDownload(ctx, runID, uid, "backups/users/9.zip.age")
	assert.NoError(t, err, "a different object of the same run has its own budget")
}

// A failed stream does NOT refund. The budget is about bytes that left the
// instance, and a download that died at 99% moved the data.
func TestCompleteDownload_RecordsTheOutcomeWithoutRefundingTheReservation(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	repo := backupstatus.NewRepository(pool)

	uid := testdb.SeedUser(t, pool, "admin@example.com", "admin")
	var runID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at, artifact_key)
		VALUES ('dump', 'succeeded', now(), now(), now(), $1) RETURNING id`, artifactKey).Scan(&runID))

	id, err := repo.ReserveDownload(ctx, runID, uid, artifactKey)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteDownload(ctx, id, 4096))

	var completed bool
	var sent *int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT completed, bytes_sent FROM backup_download WHERE id = $1`, id).Scan(&completed, &sent))
	assert.True(t, completed)
	require.NotNil(t, sent)
	assert.EqualValues(t, 4096, *sent)

	budget, err := repo.DownloadBudgetFor(ctx, runID, uid, artifactKey)
	require.NoError(t, err)
	assert.Equal(t, 1, budget.Used, "completing a download must not release its reservation")
}

/*
 * The refund, and the line it must not cross.
 *
 * ReleaseDownload exists for the reservation that produced no bytes — an agent
 * that never answered. Its WHERE clause is what keeps it from becoming a
 * general refund: once CompleteDownload has stamped bytes_sent, the row is a
 * record of data that left the instance and the DELETE must find nothing.
 */
func TestReleaseDownload_GivesBackOnlyAReservationThatMovedNothing(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	repo := backupstatus.NewRepository(pool)

	uid := testdb.SeedUser(t, pool, "admin@example.com", "admin")
	var runID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at, artifact_key)
		VALUES ('dump', 'succeeded', now(), now(), now(), $1) RETURNING id`, artifactKey).Scan(&runID))

	untouched, err := repo.ReserveDownload(ctx, runID, uid, artifactKey)
	require.NoError(t, err)
	require.NoError(t, repo.ReleaseDownload(ctx, untouched))

	budget, err := repo.DownloadBudgetFor(ctx, runID, uid, artifactKey)
	require.NoError(t, err)
	assert.Equal(t, 0, budget.Used, "a reservation that served nothing must cost nothing")

	streamed, err := repo.ReserveDownload(ctx, runID, uid, artifactKey)
	require.NoError(t, err)
	// Zero bytes is still a stream that STARTED: the client got the 200 and the
	// headers, and only bytes_sent being NULL means the bridge never answered.
	require.NoError(t, repo.CompleteDownload(ctx, streamed, 0))
	require.NoError(t, repo.ReleaseDownload(ctx, streamed))

	budget, err = repo.DownloadBudgetFor(ctx, runID, uid, artifactKey)
	require.NoError(t, err)
	assert.Equal(t, 1, budget.Used, "a completed download must not be refundable")
}

// Retention deletes backup_run rows; the budget rows must not outlive the
// artifact they rationed — there is nothing left to spend them on.
func TestBackupDownload_DiesWithTheRunItRationed(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	repo := backupstatus.NewRepository(pool)

	uid := testdb.SeedUser(t, pool, "admin@example.com", "admin")
	var runID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at, artifact_key)
		VALUES ('dump', 'succeeded', now(), now(), now(), $1) RETURNING id`, artifactKey).Scan(&runID))
	_, err := repo.ReserveDownload(ctx, runID, uid, artifactKey)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `DELETE FROM backup_run WHERE id = $1`, runID)
	require.NoError(t, err)

	var left int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM backup_download WHERE run_id = $1`, runID).Scan(&left))
	assert.Zero(t, left)
}

func TestArtifactKeyForRun_TellsAnAbsentKeyFromAnAbsentRun(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	repo := backupstatus.NewRepository(pool)

	var mirrorID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at)
		VALUES ('mirror', 'succeeded', now(), now(), now()) RETURNING id`).Scan(&mirrorID))

	// A mirror run legitimately shipped no single artifact — that is "" and a
	// 409 upstream, NOT a missing run and a 404.
	key, err := repo.ArtifactKeyForRun(ctx, mirrorID)
	require.NoError(t, err)
	assert.Empty(t, key)

	_, err = repo.ArtifactKeyForRun(ctx, mirrorID+9999)
	assert.ErrorIs(t, err, backupstatus.ErrRunNotFound)
}

/*
 * The one thing a sequential test cannot tell apart from a broken implementation.
 *
 * The first cut folded the count into the INSERT
 * (`INSERT ... SELECT ... WHERE (SELECT count) < n`) and its comment claimed
 * that was atomic. It is not: under READ COMMITTED each statement takes its own
 * snapshot, so parallel requests all read "two used" — none sees the others'
 * uncommitted rows — and all insert. Every sequential test above passed against
 * that. This one fails.
 */
func TestReserveDownload_ConcurrentRequestsCannotOverrunTheCeiling(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	repo := backupstatus.NewRepository(pool)

	uid := testdb.SeedUser(t, pool, "admin@example.com", "admin")
	var runID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at, artifact_key)
		VALUES ('dump', 'succeeded', now(), now(), now(), $1) RETURNING id`, artifactKey).Scan(&runID))

	const racers = 12
	var granted atomic.Int64
	var refused atomic.Int64
	// One barrier so every goroutine attempts inside the same window; staggered
	// starts would serialize themselves and prove nothing.
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			switch _, err := repo.ReserveDownload(ctx, runID, uid, artifactKey); {
			case err == nil:
				granted.Add(1)
			case errors.Is(err, backupstatus.ErrDownloadBudgetSpent):
				refused.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, backupstatus.MaxDownloadsPerAdmin, granted.Load(),
		"exactly the ceiling may be granted, however many requests race for it")
	assert.EqualValues(t, racers-backupstatus.MaxDownloadsPerAdmin, refused.Load())

	// And the table agrees — a grant that did not insert would be worse than a
	// refusal that did.
	var rows int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM backup_download WHERE run_id = $1 AND user_id = $2`,
		runID, int64(uid)).Scan(&rows))
	assert.Equal(t, backupstatus.MaxDownloadsPerAdmin, rows)
}
