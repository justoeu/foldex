//go:build integration

package backupagent

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backup"
	"foldex/internal/testdb"
)

func TestAdvisoryLocks_CrossProcessCoordination(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	release, ok, err := acquireJobLock(ctx, pool)
	require.NoError(t, err)
	require.True(t, ok)

	_, ok2, err := acquireJobLock(ctx, pool)
	require.NoError(t, err)
	assert.False(t, ok2, "the job lock is exclusive across agent instances")
	release()

	release3, ok3, err := acquireJobLock(ctx, pool)
	require.NoError(t, err)
	assert.True(t, ok3, "released means acquirable — a leaked lock would starve every future slot")
	release3()

	// The restore probe: busy only while a per-user restore holds ITS key.
	busy, err := restoreInFlight(ctx, pool)
	require.NoError(t, err)
	assert.False(t, busy)

	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	var got bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, backup.RestoreAdvisoryLockKey).Scan(&got))
	require.True(t, got)
	busy, err = restoreInFlight(ctx, pool)
	require.NoError(t, err)
	assert.True(t, busy, "a restore in flight must defer bucket-reading jobs (INV-104)")
	_, err = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, backup.RestoreAdvisoryLockKey)
	require.NoError(t, err)

	// Probing must not LEAK the restore key: after a probe, a real restore can
	// still take it. (The probe acquires and releases on one pinned conn.)
	busy, err = restoreInFlight(ctx, pool)
	require.NoError(t, err)
	assert.False(t, busy)
}

func TestExecute_RecordsOutcomeEvenWhenTheJobContextDies(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	a := &Agent{
		cfg:     Config{StaleRunMin: 240},
		pool:    pool,
		runs:    NewRunStore(pool),
		metrics: NewMetrics(),
		logger:  slog.New(slog.DiscardHandler),
	}
	jobCtx, cancel := context.WithCancel(ctx)
	spec := jobSpec{name: JobDump, run: func(runCtx context.Context, _ int64) (*Artifact, map[string]any, string, error) {
		cancel() // shutdown arrives mid-job
		<-runCtx.Done()
		return nil, nil, ReasonDumpFailed, errors.New("interrupted")
	}}
	a.execute(jobCtx, spec, time.Now(), 0)

	var status, reason string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status, last_error FROM backup_run ORDER BY id DESC LIMIT 1`).Scan(&status, &reason))
	assert.Equal(t, "failed", status)
	assert.Equal(t, ReasonShutdown, reason,
		"a cancelled run must land as failed(shutdown) on a fresh context — an unrecorded outcome is a stale running row")
}

func TestCheckSchema_RefusesAnOlderDatabase(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	_, err := pool.Exec(ctx, `CREATE TABLE schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE schema_migrations`) })
	a := &Agent{runs: NewRunStore(pool)}

	_, err = pool.Exec(ctx, `INSERT INTO schema_migrations VALUES ($1, false)`, RequiredSchemaVersion-1)
	require.NoError(t, err)
	err = a.CheckSchema(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migrations first",
		"the gate must fail with an instruction, not a missing-table error mid-job")

	_, err = pool.Exec(ctx, `UPDATE schema_migrations SET version = $1`, RequiredSchemaVersion)
	require.NoError(t, err)
	assert.NoError(t, a.CheckSchema(ctx))
}

func TestExecute_LockBusyFailsTheClaimedRequestAndSkipsTheSlot(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	// A sibling agent holds the job lock for the duration of the test.
	release, ok, err := acquireJobLock(ctx, pool)
	require.NoError(t, err)
	require.True(t, ok)
	defer release()

	a := &Agent{
		cfg:     Config{StaleRunMin: 240},
		pool:    pool,
		runs:    NewRunStore(pool),
		metrics: NewMetrics(),
		logger:  slog.New(slog.DiscardHandler),
	}
	ran := false
	spec := jobSpec{name: JobDump, run: func(context.Context, int64) (*Artifact, map[string]any, string, error) {
		ran = true
		return nil, nil, "", nil
	}}

	// Scheduled slot: skipped silently — the sibling IS running the job.
	a.execute(ctx, spec, time.Now(), 0)
	assert.False(t, ran)
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM backup_run`).Scan(&count))
	assert.Zero(t, count, "a skipped scheduled slot leaves no row: the sibling's record is the slot's record")

	// A claimed manual request cannot vanish silently: it must land as
	// failed(lock_busy) so the operator who clicked sees an outcome.
	_, err = pool.Exec(ctx, `INSERT INTO backup_run (job, status, scheduled_for) VALUES ('dump','requested', now())`)
	require.NoError(t, err)
	id, ok, err := a.runs.ClaimRequested(ctx, JobDump)
	require.NoError(t, err)
	require.True(t, ok)
	a.execute(ctx, spec, time.Now(), id)
	assert.False(t, ran)
	var status, reason string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status, last_error FROM backup_run WHERE id = $1`, id).Scan(&status, &reason))
	assert.Equal(t, "failed", status)
	assert.Equal(t, ReasonLockBusy, reason)
}

func TestExecute_ClaimedRequestRunsOnItsOwnRowNotANewOne(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	a := &Agent{
		cfg:     Config{StaleRunMin: 240},
		pool:    pool,
		runs:    NewRunStore(pool),
		metrics: NewMetrics(),
		logger:  slog.New(slog.DiscardHandler),
	}
	_, err := pool.Exec(ctx, `INSERT INTO backup_run (job, status, scheduled_for) VALUES ('dump','requested', now())`)
	require.NoError(t, err)
	id, ok, err := a.runs.ClaimRequested(ctx, JobDump)
	require.NoError(t, err)
	require.True(t, ok)

	spec := jobSpec{name: JobDump, run: func(context.Context, int64) (*Artifact, map[string]any, string, error) {
		return &Artifact{Key: "k", Bytes: 1, SHA256: "aa"}, nil, "", nil
	}}
	a.execute(ctx, spec, time.Now(), id)

	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM backup_run`).Scan(&count))
	assert.Equal(t, 1, count, "the claimed row IS the run — a second row would double-book the audit trail")
	var status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM backup_run WHERE id = $1`, id).Scan(&status))
	assert.Equal(t, "succeeded", status)
}
