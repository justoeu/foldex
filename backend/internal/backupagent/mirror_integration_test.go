//go:build integration

package backupagent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backup"
	"foldex/internal/testdb"
)

func TestMirrorRun_DefersToARealRestoreInFlight(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	source := newRecorderStore()
	source.uploads["screens/1.png"] = []byte("one")
	source.listing = []ObjectInfo{{Key: "screens/1.png", Size: 3, LastModified: time.Now()}}
	dest := newRecorderStore()

	job, err := NewMirrorJob(Config{AllowPlaintext: true}, pool, NewRunStore(pool), source, dest, testLogger())
	require.NoError(t, err)
	job.restoreProbeEvery = 50 * time.Millisecond
	job.restoreProbeDeadline = 300 * time.Millisecond

	// A per-user restore holds its advisory lock on a pinned connection —
	// the bucket is mid-write and the mirror must not ship that state.
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	var got bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`,
		backup.RestoreAdvisoryLockKey).Scan(&got))
	require.True(t, got)

	_, _, reason, err := job.Run(ctx)
	require.Error(t, err)
	assert.Equal(t, ReasonRestoreInFlight, reason)
	assert.Empty(t, dest.uploads, "nothing may be mirrored while the restore holds the bucket")

	// Lock released: the same job proceeds and copies.
	_, err = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, backup.RestoreAdvisoryLockKey)
	require.NoError(t, err)
	artifact, _, reason, err := job.Run(ctx)
	require.NoError(t, err)
	assert.Empty(t, reason)
	assert.Equal(t, []byte("one"), dest.uploads[mirrorKeyPrefix+"screens/1.png"])
	require.NotNil(t, artifact.Mirror)
	assert.EqualValues(t, 1, artifact.Mirror.ObjectsCopied)
}

func TestSucceed_MirrorStatsLandInTheirColumnsAndDumpLeavesThemNull(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	s := NewRunStore(pool)

	mirrorID, err := s.Begin(ctx, JobMirror, time.Now())
	require.NoError(t, err)
	require.NoError(t, s.Succeed(ctx, mirrorID, &Artifact{
		Mirror: &MirrorStats{ObjectsScanned: 12, ObjectsCopied: 3, BytesCopied: 4096},
	}, nil))

	var scanned, copied, bytesCopied int64
	var key *string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT objects_scanned, objects_copied, bytes_copied, artifact_key
		FROM backup_run WHERE id = $1`, mirrorID).Scan(&scanned, &copied, &bytesCopied, &key))
	assert.EqualValues(t, 12, scanned)
	assert.EqualValues(t, 3, copied)
	assert.EqualValues(t, 4096, bytesCopied)
	assert.Nil(t, key, "the mirror ships a delta, never one artifact object")

	// The dump's shape is untouched: artifact columns filled, counters NULL.
	dumpID, err := s.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)
	require.NoError(t, s.Succeed(ctx, dumpID, &Artifact{Key: "backups/dump/x", Bytes: 7, SHA256: "aa"}, nil))
	var dumpKey string
	var nullScanned *int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT artifact_key, objects_scanned FROM backup_run WHERE id = $1`, dumpID).
		Scan(&dumpKey, &nullScanned))
	assert.Equal(t, "backups/dump/x", dumpKey)
	assert.Nil(t, nullScanned)
}

func TestIntervalLoop_TickerFiresAndRecordsRuns(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	agent, err := New(lifecycleConfig(), pool, newRecorderStore(), nil, testLogger())
	require.NoError(t, err)
	agent.skewWarning = nil
	executed := make(chan struct{}, 8)
	agent.jobs = []jobSpec{{name: JobMirror, interval: 80 * time.Millisecond,
		run: func(context.Context, int64) (*Artifact, map[string]any, string, error) {
			// Non-blocking: if the assertion below has already passed its
			// count, a full buffer must not park this send inside spec.run
			// and wedge Stop()'s wg.Wait on a saturated machine.
			select {
			case executed <- struct{}{}:
			default:
			}
			return &Artifact{Mirror: &MirrorStats{ObjectsScanned: 1}}, nil, "", nil
		}}}
	// A fresh success keeps boot catch-up (and its minutes of jitter) out of
	// the way: what is under test is the ticker branch of the scheduler.
	_, err = pool.Exec(ctx, `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at)
		VALUES ('mirror', 'succeeded', now(), now(), now())`)
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	agent.Start(runCtx)
	defer agent.Stop()

	for range 2 {
		select {
		case <-executed:
		case <-time.After(10 * time.Second):
			t.Fatal("the interval schedule must keep firing the job")
		}
	}
	require.Eventually(t, func() bool {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM backup_run
			WHERE job = 'mirror' AND status = 'succeeded' AND objects_scanned = 1`).Scan(&n); err != nil {
			return false
		}
		return n >= 1
	}, 10*time.Second, 100*time.Millisecond,
		"an interval-fired run must land in backup_run with its mirror counters")
}

func TestConsecutiveFailures_RestoreInFlightDoesNotCount(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	s := NewRunStore(pool)

	for _, reason := range []string{ReasonRestoreInFlight, ReasonMirrorCopyFailed} {
		id, err := s.Begin(ctx, JobMirror, time.Now())
		require.NoError(t, err)
		require.NoError(t, s.Fail(ctx, id, reason))
	}
	n, err := s.ConsecutiveFailures(ctx, JobMirror)
	require.NoError(t, err)
	assert.Equal(t, 1, n,
		"a long per-user restore says nothing about whether the mirror WORKS — paging on it trains the operator to ignore the alert")
}

func TestMirror_ARequestedRowExecutesTheRealJobThroughTheRegistry(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	source := newRecorderStore()
	source.uploads["screenshots/9.png"] = []byte("payload")
	source.listing = []ObjectInfo{{Key: "screenshots/9.png", Size: 7, LastModified: time.Now()}}
	dest := newRecorderStore()

	cfg := lifecycleConfig()
	cfg.AllowPlaintext = true
	cfg.MirrorIntervalMin = 60
	agent, err := New(cfg, pool, dest, source, testLogger())
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	agent.Start(runCtx)
	defer agent.Stop()

	// The operator's button is an INSERT; the poll loop must run the REAL
	// mirror through the registry wrapper — a wrapper that dropped the call
	// would leave this row failed or hanging and the destination empty.
	_, err = pool.Exec(ctx, `INSERT INTO backup_run (job, status, scheduled_for) VALUES ('mirror','requested', now())`)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM backup_run WHERE job='mirror' ORDER BY id DESC LIMIT 1`).Scan(&status); err != nil {
			return false
		}
		return status == "succeeded"
	}, 15*time.Second, 200*time.Millisecond)
	assert.Contains(t, dest.uploads, mirrorKeyPrefix+"screenshots/9.png")
}
