//go:build integration

package backupagent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/testdb"
)

// The live-reload contract: an owner's row reaches the running agent's timers
// within one sync tick, with no restart — and deleting it falls back to env.
func TestAgent_SyncPicksUpScheduleRowsLive(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	cfg := lifecycleConfig()
	cfg.DumpAt = mustAnchor(t, "03:30")
	agent, err := New(cfg, pool, newRecorderStore(), nil, testLogger())
	require.NoError(t, err)
	agent.skewWarning = nil

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	agent.Start(runCtx)
	defer agent.Stop()

	assert.Equal(t, "env", agent.timing(JobDump).Source)
	assert.Equal(t, "03:30", agent.timing(JobDump).String())

	store := NewScheduleStore(pool)
	require.NoError(t, store.Upsert(ctx, JobDump, JobConfig{
		Mode: "times", Times: []string{"06:00", "18:00"},
		Weekdays: []string{"mon", "tue", "wed", "thu", "fri"}}, 0))
	require.Eventually(t, func() bool {
		return agent.timing(JobDump).Source == "db"
	}, 15*time.Second, 100*time.Millisecond, "the sync loop must adopt the row without a restart")
	assert.Equal(t, "06:00, 18:00 · mon, tue, wed, thu, fri", agent.timing(JobDump).String())

	require.NoError(t, store.Delete(ctx, JobDump))
	require.Eventually(t, func() bool {
		return agent.timing(JobDump).Source == "env"
	}, 15*time.Second, 100*time.Millisecond, "deleting the row must fall the agenda back to env")

	// The heartbeat is written by the same loop — the band's "agent last
	// seen" and the agenda's source come from one place.
	state, seen, err := store.AgentSeen(ctx)
	require.NoError(t, err)
	require.True(t, seen)
	assert.True(t, state.Jobs[JobDump].Capable)
	assert.Equal(t, "env", state.Jobs[JobDump].Source)
}

// The heartbeat is where the admin form learns the ENV agenda. Without the
// baseline document there the form can only open blank or on the row, and
// "env is the first option, the row is the override" has no first option.
func TestAgent_HeartbeatCarriesTheEnvBaseline(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	cfg := lifecycleConfig()
	cfg.DumpAt = mustAnchor(t, "03:30")
	agent, err := New(cfg, pool, newRecorderStore(), nil, testLogger())
	require.NoError(t, err)
	agent.skewWarning = nil

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	agent.Start(runCtx)
	defer agent.Stop()

	state, seen, err := NewScheduleStore(pool).AgentSeen(ctx)
	require.NoError(t, err)
	require.True(t, seen)

	baseline := state.Jobs[JobDump].Baseline
	assert.Equal(t, "times", baseline.Mode)
	assert.Equal(t, []string{"03:30"}, baseline.Times)
	assert.Equal(t, allDays(), baseline.Weekdays)
	assert.NoError(t, ValidateJobConfig(JobDump, baseline),
		"the form opens on this document and saves it back — it has to pass the floors")
}

// The operator cannot verify a destination they cannot see, and the endpoint
// is the field that most often points somewhere other than intended — here it
// named the SAME host as the origin, which is a mirror that survives nothing.
func TestAgent_HeartbeatNamesTheExternalDestination(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	cfg := lifecycleConfig()
	cfg.S3Endpoint = "s3.example.test:9000"
	cfg.S3Bucket = "foldex-backups"
	cfg.S3AccessKey = "AKIAEXAMPLEKEY"
	cfg.S3SecretKey = "s3cr3t-do-not-publish"
	agent, err := New(cfg, pool, newRecorderStore(), nil, testLogger())
	require.NoError(t, err)
	agent.skewWarning = nil

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	agent.Start(runCtx)
	defer agent.Stop()

	state, seen, err := NewScheduleStore(pool).AgentSeen(ctx)
	require.NoError(t, err)
	require.True(t, seen)

	for job, prefix := range map[string]string{
		JobDump:    "backups/dump/",
		JobDrill:   "backups/dump/",
		JobMirror:  "backups/rustfs/",
		JobUserZip: "backups/users/",
	} {
		dest := state.Jobs[job].Destination
		require.NotNil(t, dest, "job %s writes to or reads from the external bucket", job)
		assert.Equal(t, "s3.example.test:9000", dest.Endpoint, "job %s", job)
		assert.Equal(t, "foldex-backups", dest.Bucket, "job %s", job)
		assert.Equal(t, prefix, dest.Prefix, "job %s", job)
	}
}

// The heartbeat is read by the admin screen, so anything it carries is on a
// screen. INV-171 keeps the S3 credentials inside this process; publishing the
// destination must not become the hole that walks them out.
func TestAgent_HeartbeatCarriesNoCredential(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	cfg := lifecycleConfig()
	cfg.S3Endpoint = "s3.example.test:9000"
	cfg.S3Bucket = "foldex-backups"
	cfg.S3AccessKey = "AKIAEXAMPLEKEY"
	cfg.S3SecretKey = "s3cr3t-do-not-publish"
	cfg.RustFSAccessKey = "rustfs-access-key"
	cfg.RustFSSecretKey = "rustfs-secret-key"
	agent, err := New(cfg, pool, newRecorderStore(), nil, testLogger())
	require.NoError(t, err)
	agent.skewWarning = nil

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	agent.Start(runCtx)
	defer agent.Stop()

	var raw string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT capabilities::text FROM backup_agent_state WHERE id = 1`).Scan(&raw))

	for _, secret := range []string{
		cfg.S3AccessKey, cfg.S3SecretKey, cfg.RustFSAccessKey, cfg.RustFSSecretKey,
	} {
		assert.NotContains(t, raw, secret,
			"the heartbeat is rendered on the admin screen; a credential in it is a credential on a screen")
	}
}

// The disabled→enabled swap is the interleaving that used to strand a loop:
// with changeCh read after timing, a swap between the two reads closed a
// channel nobody held and a schedule-less job slept on the new channel until
// the NEXT edit. The loop now takes the channel first, so enabling a parked
// job wakes it.
func TestScheduleLoop_WakesAJobEnabledAfterStart(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	agent, err := New(lifecycleConfig(), pool, newRecorderStore(), nil, testLogger())
	require.NoError(t, err)
	agent.skewWarning = nil
	// Zero jitter: if forceTiming below wins the race into bootCatchUp, the
	// catch-up path must fire promptly instead of sleeping production
	// minutes inside a 10s test — the exact flake CI caught.
	agent.catchUpJitter = func() time.Duration { return 0 }
	executed := make(chan struct{}, 4)
	agent.jobs = []jobSpec{{name: JobDump, run: func(context.Context, int64) (*Artifact, map[string]any, string, error) {
		select {
		case executed <- struct{}{}:
		default:
		}
		return &Artifact{Key: "woken", Bytes: 1, SHA256: "aa"}, nil, "", nil
	}}}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	agent.Start(runCtx)
	defer agent.Stop()

	require.False(t, agent.timing(JobDump).Enabled(), "precondition: the job starts with no schedule")
	agent.forceTiming(JobDump, Timing{Interval: 60 * time.Millisecond, Source: "env"})

	select {
	case <-executed:
	case <-time.After(10 * time.Second):
		t.Fatal("a schedule arriving after Start must wake the parked loop — not wait for a second edit")
	}
}

// The catch-up path itself, through the jitter seam that made it testable: a
// job with a schedule and no success on record runs promptly at boot.
func TestBootCatchUp_RunsANeverSucceededJob(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	agent, err := New(lifecycleConfig(), pool, newRecorderStore(), nil, testLogger())
	require.NoError(t, err)
	agent.skewWarning = nil
	agent.catchUpJitter = func() time.Duration { return 0 }
	executed := make(chan struct{}, 4)
	agent.jobs = []jobSpec{{name: JobDump, run: func(context.Context, int64) (*Artifact, map[string]any, string, error) {
		select {
		case executed <- struct{}{}:
		default:
		}
		return &Artifact{Key: "catchup", Bytes: 1, SHA256: "aa"}, nil, "", nil
	}}}
	agent.forceTiming(JobDump, Timing{Interval: time.Hour, Source: "env"})

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	agent.Start(runCtx)
	defer agent.Stop()

	select {
	case <-executed:
	case <-time.After(10 * time.Second):
		t.Fatal("a never-succeeded job with a schedule must catch up at boot")
	}
	require.Eventually(t, func() bool {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM backup_run WHERE job='dump' AND status='succeeded'`).Scan(&n); err != nil {
			return false
		}
		return n >= 1
	}, 10*time.Second, 100*time.Millisecond, "the catch-up run must record its outcome")
}
