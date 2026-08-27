//go:build integration

package backupagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

	require.NoError(t, store.Upsert(ctx, JobDump, JobConfig{
		Mode: "times", Times: []string{"06:00", "18:00"}, Weekdays: allDays()}, uid))
	rows, err = store.Load(ctx)
	require.NoError(t, err)
	require.Contains(t, rows, JobDump)
	assert.Equal(t, []string{"06:00", "18:00"}, rows[JobDump].Config.Times)
	assert.Equal(t, allDays(), rows[JobDump].Config.Weekdays)
	require.NotNil(t, rows[JobDump].UpdatedByEmail)
	assert.Equal(t, "owner@foldex.test", *rows[JobDump].UpdatedByEmail)

	// Upsert replaces, never accumulates.
	require.NoError(t, store.Upsert(ctx, JobDump, JobConfig{
		Mode: "times", Times: []string{"04:15"}, Weekdays: []string{"mon", "tue", "wed", "thu", "fri"}}, uid))
	rows, err = store.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"04:15"}, rows[JobDump].Config.Times)
	assert.Equal(t, []string{"mon", "tue", "wed", "thu", "fri"}, rows[JobDump].Config.Weekdays)

	// The floor holds at the store layer too — the handler is not the only
	// gate (a hand-written caller gets the same refusal).
	err = store.Upsert(ctx, JobDump, JobConfig{Mode: "times", Times: []string{}, Weekdays: allDays()}, uid)
	require.Error(t, err)
	err = store.Upsert(ctx, JobDump, JobConfig{
		Mode: "times", Times: []string{"04:15"}, Weekdays: []string{"mon", "wed", "fri"}}, uid)
	require.Error(t, err, "the dump's weekday floor is the instance's disaster floor, not a preference")

	require.NoError(t, store.Delete(ctx, JobDump))
	rows, err = store.Load(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "delete falls the job back to the env baseline")
	assert.NoError(t, store.Delete(ctx, JobDump), "deleting an absent row is idempotent")
}

// A row stored before the unified shape must still be honoured. Load
// normalizes it; without that the agent would fall back to the env baseline
// and never say why — the job would look configured and run on another
// agenda.
func TestScheduleStore_LoadNormalizesALegacyRow(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	store := NewScheduleStore(pool)

	for _, legacy := range []struct {
		job string
		doc string
	}{
		{JobDump, `{"times":["06:00","18:00"]}`},
		{JobDrill, `{"time":"01:00","weekday":"sun"}`},
		{JobMirror, `{"interval_min":360}`},
		{JobUserZip, `{"enabled":true,"time":"02:30"}`},
	} {
		_, err := pool.Exec(ctx,
			`INSERT INTO backup_schedule (job, config) VALUES ($1, $2::jsonb)`, legacy.job, legacy.doc)
		require.NoError(t, err)
	}

	rows, err := store.Load(ctx)
	require.NoError(t, err)
	for job, row := range rows {
		assert.NoError(t, ValidateJobConfig(job, row.Config),
			"a legacy row must load as a document the floors accept, not as one they refuse")
	}
	assert.Equal(t, allDays(), rows[JobDump].Config.Weekdays)
	assert.Equal(t, []string{"01:00"}, rows[JobDrill].Config.Times)
	assert.Equal(t, []string{"sun"}, rows[JobDrill].Config.Weekdays)
	assert.Equal(t, "interval", rows[JobMirror].Config.Mode)
	require.NotNil(t, rows[JobUserZip].Config.Enabled)
	assert.True(t, *rows[JobUserZip].Config.Enabled)
}

// The migration is what rewrites the rows on disk; Load's normalization is the
// belt to its braces. Re-running the statements is idempotent, so applying
// them again over freshly seeded legacy rows is a faithful exercise of what
// `migrate up` did.
func TestMigration_UnifiesLegacyScheduleRows(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	for _, legacy := range []struct {
		job string
		doc string
	}{
		{JobDump, `{"times":["06:00","18:00"]}`},
		{JobDrill, `{"time":"01:00","weekday":"SUN"}`},
		{JobMirror, `{"interval_min":360}`},
		{JobUserZip, `{"enabled":false}`},
	} {
		_, err := pool.Exec(ctx,
			`INSERT INTO backup_schedule (job, config) VALUES ($1, $2::jsonb)`, legacy.job, legacy.doc)
		require.NoError(t, err)
	}

	_, file, _, _ := runtime.Caller(0)
	sql, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..",
		"db", "migrations", "000043_backup_schedule_unified.up.sql"))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(sql))
	require.NoError(t, err)

	rows := map[string]JobConfig{}
	cur, err := pool.Query(ctx, `SELECT job, config FROM backup_schedule`)
	require.NoError(t, err)
	defer cur.Close()
	for cur.Next() {
		var job string
		var raw []byte
		require.NoError(t, cur.Scan(&job, &raw))
		var cfg JobConfig
		require.NoError(t, json.Unmarshal(raw, &cfg))
		rows[job] = cfg
	}
	require.NoError(t, cur.Err())

	require.Len(t, rows, 4)
	for job, cfg := range rows {
		assert.NoError(t, ValidateJobConfig(job, cfg), "job=%s", job)
	}
	assert.Equal(t, "times", rows[JobDump].Mode)
	assert.Equal(t, []string{"06:00", "18:00"}, rows[JobDump].Times)
	assert.Equal(t, allDays(), rows[JobDump].Weekdays)
	assert.Equal(t, []string{"01:00"}, rows[JobDrill].Times)
	assert.Equal(t, []string{"sun"}, rows[JobDrill].Weekdays)
	assert.Equal(t, "interval", rows[JobMirror].Mode)
	assert.Equal(t, 360, rows[JobMirror].IntervalMin)
	require.NotNil(t, rows[JobUserZip].Enabled)
	assert.False(t, *rows[JobUserZip].Enabled)
	assert.Empty(t, rows[JobUserZip].Times, "a disabled job carries no agenda")
	for job, cfg := range rows {
		assert.Empty(t, cfg.Time, "job=%s: the legacy keys are gone from the row", job)
		assert.Empty(t, cfg.Weekday, "job=%s", job)
	}
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
