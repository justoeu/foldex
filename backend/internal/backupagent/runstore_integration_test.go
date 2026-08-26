//go:build integration

package backupagent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backup"
	"foldex/internal/testdb"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

func TestSchemaVersion_GateReadsWhatGolangMigrateRecords(t *testing.T) {
	// testdb applies the .sql files directly, so schema_migrations — a
	// golang-migrate artifact — does not exist here. The gate's contract is
	// with THAT table, so the test builds it the way migrate does.
	ctx := context.Background()
	pool := testdb.Shared(t)
	_, err := pool.Exec(ctx, `CREATE TABLE schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE schema_migrations`) })
	s := NewRunStore(pool)

	_, err = pool.Exec(ctx, `INSERT INTO schema_migrations VALUES ($1, false)`, RequiredSchemaVersion)
	require.NoError(t, err)
	v, err := s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, RequiredSchemaVersion, v)

	_, err = pool.Exec(ctx, `UPDATE schema_migrations SET dirty = true`)
	require.NoError(t, err)
	_, err = s.SchemaVersion(ctx)
	assert.ErrorContains(t, err, "dirty",
		"a half-applied migration must stop the agent, not race it")
}

func TestMigration000040_IsActuallyAppliedToTheTestSchema(t *testing.T) {
	// The real assertion the old gate test wanted: the backup_run table this
	// package was written against exists with its load-bearing index.
	ctx := context.Background()
	pool := testdb.Shared(t)
	var count int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE tablename = 'backup_run' AND indexname = 'backup_run_one_running_idx'`).
		Scan(&count))
	assert.Equal(t, 1, count, "the partial unique index is the persistence-level mutual exclusion — its absence turns dual agents into silent double-runs")
}

func TestBegin_TheRunningSlotIsExclusivePerJob(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	s := NewRunStore(pool)

	id, err := s.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)

	// A second agent (its own claim identity) hits the partial unique index.
	other := NewRunStore(pool)
	_, err = other.Begin(ctx, JobDump, time.Now())
	assert.ErrorIs(t, err, ErrAlreadyRunning)

	// A different job is not blocked: the slot is per job, not global.
	_, err = other.Begin(ctx, JobMirror, time.Now())
	require.NoError(t, err)

	// Finishing frees the slot.
	require.NoError(t, s.Succeed(ctx, id, &Artifact{Key: "k", Bytes: 1, SHA256: "aa"}, nil))
	_, err = other.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)
}

func TestClaimRequested_CASPromotesExactlyOnce(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	s := NewRunStore(pool)

	_, ok, err := s.ClaimRequested(ctx, JobDump)
	require.NoError(t, err)
	assert.False(t, ok, "nothing requested, nothing claimed")

	// The backend's side of the contract is a bare INSERT — reproduced here
	// verbatim, claim_token NULL until an agent owns it.
	_, err = pool.Exec(ctx, `INSERT INTO backup_run (job, status, scheduled_for) VALUES ('dump','requested', now())`)
	require.NoError(t, err)

	id, ok, err := s.ClaimRequested(ctx, JobDump)
	require.NoError(t, err)
	require.True(t, ok)

	// The same request cannot be claimed twice.
	_, ok, err = s.ClaimRequested(ctx, JobDump)
	require.NoError(t, err)
	assert.False(t, ok)

	// And while it runs, a second requested row stays queued: promoting it
	// would violate the running slot.
	_, err = pool.Exec(ctx, `INSERT INTO backup_run (job, status, scheduled_for) VALUES ('dump','requested', now())`)
	require.NoError(t, err)
	_, ok, err = s.ClaimRequested(ctx, JobDump)
	require.NoError(t, err)
	assert.False(t, ok, "the partial unique index must hold against claim promotion too")

	require.NoError(t, s.Fail(ctx, id, ReasonDumpFailed))
	_, ok, err = s.ClaimRequested(ctx, JobDump)
	require.NoError(t, err)
	assert.True(t, ok, "with the slot free the queued request is claimable")
}

func TestOutcomes_FeedLastSuccessAndConsecutiveFailures(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	s := NewRunStore(pool)

	last, err := s.LastSuccess(ctx, JobDump)
	require.NoError(t, err)
	assert.True(t, last.IsZero(), "never succeeded reads as the zero time — that is what makes catch-up fire")

	id1, err := s.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)
	require.NoError(t, s.Fail(ctx, id1, ReasonDumpFailed))
	id2, err := s.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)
	require.NoError(t, s.Fail(ctx, id2, ReasonUploadFailed))

	n, err := s.ConsecutiveFailures(ctx, JobDump)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	id3, err := s.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)
	require.NoError(t, s.Succeed(ctx, id3, &Artifact{Key: "backups/dump/x", Bytes: 42, SHA256: "ff"}, map[string]any{"encrypted": true}))

	last, err = s.LastSuccess(ctx, JobDump)
	require.NoError(t, err)
	assert.False(t, last.IsZero())
	n, err = s.ConsecutiveFailures(ctx, JobDump)
	require.NoError(t, err)
	assert.Zero(t, n, "a success resets the streak the alert threshold counts")

	// The artifact facts survive into the row the admin surface will read.
	var key, sha string
	var size int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT artifact_key, artifact_bytes, artifact_sha256 FROM backup_run WHERE id = $1`, id3).
		Scan(&key, &size, &sha))
	assert.Equal(t, "backups/dump/x", key)
	assert.EqualValues(t, 42, size)
	assert.Equal(t, "ff", sha)
}

func TestExpireStale_FreesTheSlotADeadAgentHeld(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	dead := NewRunStore(pool)

	_, err := dead.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)
	// Age the row past the TTL — the dead agent never finished it.
	_, err = pool.Exec(ctx, `UPDATE backup_run SET started_at = now() - interval '5 hours' WHERE status = 'running'`)
	require.NoError(t, err)

	successor := NewRunStore(pool)
	_, err = successor.Begin(ctx, JobDump, time.Now())
	require.ErrorIs(t, err, ErrAlreadyRunning, "before the janitor, the corpse still holds the slot")

	n, err := successor.ExpireStale(ctx, 4*time.Hour)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	var reason string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT last_error FROM backup_run WHERE status = 'failed'`).Scan(&reason))
	assert.Equal(t, ReasonStaleClaim, reason)

	_, err = successor.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err, "the successor cleans up after the dead and takes the slot")
}

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
	spec := jobSpec{name: JobDump, run: func(runCtx context.Context) (*Artifact, map[string]any, string, error) {
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
