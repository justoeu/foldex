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

func TestScheduleStore_RoundTripAndFallback(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	store := NewScheduleStore(pool)

	rows, err := store.Load(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "a fresh instance has no rows — every job on its env baseline")

	var uid int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO app_user (email, email_normalized, name, role, status, password_hash)
		VALUES ('owner@foldex.test', 'owner@foldex.test', 'Owner', 'owner', 'active',
			'$2a$10$test.hash.not.a.real.credential.abcdefghijk')
		RETURNING id`).Scan(&uid))

	require.NoError(t, store.Upsert(ctx, JobDump, JobConfig{Times: []string{"06:00", "18:00"}}, uid))
	rows, err = store.Load(ctx)
	require.NoError(t, err)
	require.Contains(t, rows, JobDump)
	assert.Equal(t, []string{"06:00", "18:00"}, rows[JobDump].Config.Times)
	require.NotNil(t, rows[JobDump].UpdatedByEmail)
	assert.Equal(t, "owner@foldex.test", *rows[JobDump].UpdatedByEmail)

	// Upsert replaces, never accumulates.
	require.NoError(t, store.Upsert(ctx, JobDump, JobConfig{Times: []string{"04:15"}}, uid))
	rows, err = store.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"04:15"}, rows[JobDump].Config.Times)

	// The floor holds at the store layer too — the handler is not the only
	// gate (a hand-written caller gets the same refusal).
	err = store.Upsert(ctx, JobDump, JobConfig{Times: []string{}}, uid)
	require.Error(t, err)

	require.NoError(t, store.Delete(ctx, JobDump))
	rows, err = store.Load(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "delete falls the job back to the env baseline")
	assert.NoError(t, store.Delete(ctx, JobDump), "deleting an absent row is idempotent")
}

func TestScheduleStore_HeartbeatRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	store := NewScheduleStore(pool)

	_, seen, err := store.AgentSeen(ctx)
	require.NoError(t, err)
	assert.False(t, seen, "no heartbeat ever written must read as \"never seen\", not a zero time")

	state := AgentState{
		SeenAt:  time.Now().UTC().Truncate(time.Millisecond),
		Version: "2.17.0",
		Jobs: map[string]JobReport{
			JobDump:  {Capable: true, Source: "db", Schedule: "04:15"},
			JobDrill: {Capable: false, Reason: "no_identity", Source: "env", Schedule: "disabled"},
		},
	}
	require.NoError(t, store.Heartbeat(ctx, state))
	got, seen, err := store.AgentSeen(ctx)
	require.NoError(t, err)
	require.True(t, seen)
	assert.Equal(t, "2.17.0", got.Version)
	assert.Equal(t, state.Jobs, got.Jobs)

	// The row is a singleton: a second heartbeat replaces, never appends.
	state.Version = "2.17.1"
	require.NoError(t, store.Heartbeat(ctx, state))
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM backup_agent_state`).Scan(&n))
	assert.Equal(t, 1, n)
}

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
	require.NoError(t, store.Upsert(ctx, JobDump, JobConfig{Times: []string{"06:00", "18:00"}}, 0))
	require.Eventually(t, func() bool {
		return agent.timing(JobDump).Source == "db"
	}, 15*time.Second, 100*time.Millisecond, "the sync loop must adopt the row without a restart")
	assert.Equal(t, "06:00, 18:00", agent.timing(JobDump).String())

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
