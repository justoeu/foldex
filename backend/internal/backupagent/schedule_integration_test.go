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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/testdb"
)

// migrationSQL reads one migration file so a test can apply it to the seeded
// table. Every statement in 000043 is guarded on the row not already carrying
// a mode, so re-running it over freshly seeded legacy rows is a faithful
// exercise of what `migrate up` did.
func migrationSQL(t *testing.T, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations", name))
	require.NoError(t, err)
	return string(raw)
}

// scheduleDocs reads every stored row back as a decoded document, keyed by
// job — the migrations rewrite raw jsonb, so the assertions compare documents
// rather than bytes whose key order Postgres decides.
func scheduleDocs(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string]map[string]any {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT job, config FROM backup_schedule`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]map[string]any{}
	for rows.Next() {
		var job string
		var raw []byte
		require.NoError(t, rows.Scan(&job, &raw))
		var doc map[string]any
		require.NoError(t, json.Unmarshal(raw, &doc))
		out[job] = doc
	}
	require.NoError(t, rows.Err())
	return out
}

func seedSchedule(ctx context.Context, t *testing.T, pool *pgxpool.Pool, job, doc string) {
	t.Helper()
	_, err := pool.Exec(ctx,
		`INSERT INTO backup_schedule (job, config) VALUES ($1, $2::jsonb)`, job, doc)
	require.NoError(t, err)
}

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
// One undecodable document must not take the OTHER three jobs down with it.
// Load's contract is per-row tolerance — a row that does not validate is
// returned anyway so EffectiveTiming refuses it and the caller logs the
// fallback — and a whole-read error breaks exactly that: the sync loop keeps
// yesterday's timings for every job and nothing says why. A hand-written
// {"interval_min": "nightly"} is all it takes.
func TestScheduleStore_LoadToleratesOneUndecodableRow(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	store := NewScheduleStore(pool)

	for _, r := range []struct{ job, doc string }{
		{JobDump, `{"mode":"times","times":["06:00"],"weekdays":["sun","mon","tue","wed","thu","fri","sat"]}`},
		{JobMirror, `{"interval_min": "nightly"}`},
	} {
		_, err := pool.Exec(ctx,
			`INSERT INTO backup_schedule (job, config) VALUES ($1, $2::jsonb)`, r.job, r.doc)
		require.NoError(t, err)
	}

	rows, err := store.Load(ctx)
	require.NoError(t, err, "one bad document must not fail the whole read")

	// The good row survives intact.
	require.Contains(t, rows, JobDump)
	assert.Equal(t, []string{"06:00"}, rows[JobDump].Config.Times)

	// The bad one is REPORTED, not hidden: it comes back carrying the reason,
	// with a config the floors refuse, so the job falls to the env baseline
	// and the caller has something to log.
	require.Contains(t, rows, JobMirror)
	assert.NotEmpty(t, rows[JobMirror].Malformed, "the row must carry why it could not be read")
	assert.Error(t, ValidateJobConfig(JobMirror, rows[JobMirror].Config))
}

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
		seedSchedule(ctx, t, pool, legacy.job, legacy.doc)
	}

	_, err := pool.Exec(ctx, migrationSQL(t, "000043_backup_schedule_unified.up.sql"))
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

// The migration's header claims it rewrites the rows as normalized does. That
// claim is only worth something if something checks it: the two drifted apart
// on exactly the shapes below, and each divergence was a job whose agenda
// changed without anyone asking.
//
// One case per iteration because job is the primary key, and several of these
// shapes belong to the same job.
func TestMigration_AgreesWithNormalizedOnEveryLegacyShape(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	sql := migrationSQL(t, "000043_backup_schedule_unified.up.sql")

	for _, tc := range []struct {
		name   string
		job    string
		legacy string
	}{
		// "enabled" left on a job that may not carry it produces a document
		// that HAS a mode — so normalized returns it untouched — and that the
		// floors then refuse forever, pinning the job to the env baseline
		// behind nothing louder than a Warn.
		{"enabled on the dump", JobDump, `{"enabled":true,"time":"02:30"}`},
		{"enabled false on the drill", JobDrill, `{"enabled":false,"time":"02:30"}`},
		{"enabled on the mirror", JobMirror, `{"enabled":true,"interval_min":360}`},

		// A disabled user_zip that keeps its agenda is refused by the new
		// validator, falls back to the env baseline and STARTS RUNNING AGAIN.
		{"user_zip off with a time", JobUserZip, `{"enabled":false,"time":"02:30"}`},
		{"user_zip off with times", JobUserZip, `{"enabled":false,"times":["02:30","14:30"]}`},
		{"user_zip off with an interval", JobUserZip, `{"enabled":false,"interval_min":360}`},
		{"user_zip off with nothing else", JobUserZip, `{"enabled":false}`},

		// The reverse: "enabled" must survive whichever branch rewrites the
		// row, or a user_zip the owner switched ON comes back off (and one
		// switched off through the times shape comes back on).
		{"user_zip on with times", JobUserZip, `{"enabled":true,"times":["02:30"]}`},
		{"user_zip on with a time", JobUserZip, `{"enabled":true,"time":"02:30"}`},
		{"user_zip on with an interval", JobUserZip, `{"enabled":true,"interval_min":360}`},
		{"user_zip on with no agenda", JobUserZip, `{"enabled":true}`},

		{"the dump's own shape", JobDump, `{"times":["06:00","18:00"]}`},
		{"the drill's own shape", JobDrill, `{"time":"01:00","weekday":"SUN"}`},
		{"the mirror's own shape", JobMirror, `{"interval_min":360}`},
		{"a hand-written bare time", JobDump, `{"time":"04:15"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `DELETE FROM backup_schedule`)
			require.NoError(t, err)
			seedSchedule(ctx, t, pool, tc.job, tc.legacy)

			// Twice: every statement is guarded on the row not already
			// carrying a mode, and an operator who re-runs `migrate up` must
			// not get a second, different rewrite.
			_, err = pool.Exec(ctx, sql)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, sql)
			require.NoError(t, err)

			var raw []byte
			require.NoError(t, pool.QueryRow(ctx,
				`SELECT config FROM backup_schedule WHERE job = $1`, tc.job).Scan(&raw))
			var migrated JobConfig
			require.NoError(t, json.Unmarshal(raw, &migrated))

			var legacy JobConfig
			require.NoError(t, json.Unmarshal([]byte(tc.legacy), &legacy))
			assert.Equal(t, legacy.normalized(tc.job), migrated,
				"the row on disk and the row Load would hand the agent must be the same document")
		})
	}
}

// The down migration had no test at all, and one of its comments was wrong
// once already. It is LOSSY by construction — the legacy vocabulary cannot
// say what the unified one can — so what is asserted here is that each loss
// is the DOCUMENTED one.
func TestMigration_DownCollapsesToTheLegacyVocabulary(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	sql := migrationSQL(t, "000043_backup_schedule_unified.down.sql")

	// The dump DROPS its weekday set: the legacy dump shape had none and
	// meant "every day" — the one lossy direction that RAISES frequency.
	seedSchedule(ctx, t, pool, JobDump,
		`{"mode":"times","times":["06:00","18:00"],"weekdays":["mon","tue","wed","thu","fri"]}`)
	// The drill collapses to its FIRST time and FIRST weekday.
	seedSchedule(ctx, t, pool, JobDrill,
		`{"mode":"times","times":["01:00","13:00"],"weekdays":["wed","sun"]}`)
	seedSchedule(ctx, t, pool, JobMirror, `{"mode":"interval","interval_min":360}`)
	// A disabled user_zip carries no time, and jsonb_strip_nulls is what
	// keeps the absent one from landing as an explicit null the old reader
	// would have parsed as an empty anchor.
	seedSchedule(ctx, t, pool, JobUserZip, `{"mode":"times","enabled":false}`)

	_, err := pool.Exec(ctx, sql)
	require.NoError(t, err)

	docs := scheduleDocs(ctx, t, pool)
	require.Len(t, docs, 4)
	assert.Equal(t, map[string]any{"times": []any{"06:00", "18:00"}}, docs[JobDump])
	assert.Equal(t, map[string]any{"time": "01:00", "weekday": "wed"}, docs[JobDrill])
	assert.Equal(t, map[string]any{"interval_min": float64(360)}, docs[JobMirror])
	assert.Equal(t, map[string]any{"enabled": false}, docs[JobUserZip],
		"exactly {\"enabled\":false} — a null \"time\" beside it would be a shape the old reader never wrote")

	// An enabled user_zip keeps its first time, and "enabled" defaults to
	// true for a row that never carried the key.
	require.NoError(t, testdb.Reset(ctx, pool))
	seedSchedule(ctx, t, pool, JobUserZip,
		`{"mode":"times","times":["02:30","14:30"],"weekdays":["sun","mon","tue","wed","thu","fri","sat"]}`)
	// Whatever the legacy vocabulary cannot say AT ALL: an interval on a job
	// that never had one, or wall times on the mirror. The row is deleted and
	// the job returns to its env baseline — which is what the old code did
	// with a row it refused, it just never said so.
	seedSchedule(ctx, t, pool, JobDump, `{"mode":"interval","interval_min":60}`)
	seedSchedule(ctx, t, pool, JobDrill, `{"mode":"interval","interval_min":60}`)
	seedSchedule(ctx, t, pool, JobMirror, `{"mode":"times","times":["03:00"],"weekdays":["mon"]}`)

	_, err = pool.Exec(ctx, sql)
	require.NoError(t, err)

	docs = scheduleDocs(ctx, t, pool)
	assert.Equal(t, map[string]any{"enabled": true, "time": "02:30"}, docs[JobUserZip])
	assert.NotContains(t, docs, JobDump, "an interval on the dump has no legacy form to fall back to")
	assert.NotContains(t, docs, JobDrill)
	assert.NotContains(t, docs, JobMirror, "wall times on the mirror have no legacy form either")

	// Nothing the current vocabulary wrote survives the revert: a leftover
	// "mode" would be read by the OLD backend as a row with no schedule.
	var withMode int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM backup_schedule WHERE config ? 'mode'`).Scan(&withMode))
	assert.Zero(t, withMode)

	// The two guards on the drill's and the mirror's collapse, neither of
	// which any case above reaches. Both send the row to the DELETE instead
	// of writing a legacy document, and that is the honest outcome — the job
	// returns to its env baseline, which is what the old code did with a row
	// it refused.
	require.NoError(t, testdb.Reset(ctx, pool))
	// A "times" drill with NO weekdays. The collapse is guarded on
	// weekdays ->> 0 IS NOT NULL because the legacy drill shape is
	// {"time","weekday"} and the weekday is not optional in it: without the
	// guard this row would collapse to {"time":"01:00","weekday":null}, which
	// the old reader parses as an anchor with no weekday — a DAILY drill
	// where the operator had asked for none.
	seedSchedule(ctx, t, pool, JobDrill, `{"mode":"times","times":["01:00"]}`)
	// A hand-written NON-NUMERIC interval on the mirror — the value that
	// actually trips the type guard. A quoted "360" would cast fine, because
	// ->> unquotes; only something like this makes ::int abort the whole
	// revert and take every other row's collapse with it.
	seedSchedule(ctx, t, pool, JobMirror, `{"mode":"interval","interval_min":"nightly"}`)
	// The bystander: it proves the two skips fail SOFT — one row the revert
	// cannot state must not cost the rows it can.
	seedSchedule(ctx, t, pool, JobDump,
		`{"mode":"times","times":["03:30"],"weekdays":["mon","tue","wed","thu","fri"]}`)

	_, err = pool.Exec(ctx, sql)
	require.NoError(t, err, "one row the revert cannot state must not abort the revert of the others")

	docs = scheduleDocs(ctx, t, pool)
	assert.NotContains(t, docs, JobDrill,
		"a times drill with no weekdays has no legacy form — the weekday is not optional in the old shape")
	assert.NotContains(t, docs, JobMirror,
		"a string interval_min is skipped by the type guard and falls to the DELETE")
	assert.Equal(t, map[string]any{"times": []any{"03:30"}}, docs[JobDump],
		"the rows around them still collapse")
}

// The up migration's type guard, the mirror image of the down one above, and
// the one nothing exercised. A migration runs as ONE transaction: if ::int
// aborts on a single hand-written row, every other job's rewrite goes with it
// and `migrate up` leaves a database at version 42 against a repo that
// expects 43.
//
// Both shapes below are skipped, and they are skipped for different reasons
// worth keeping straight. "nightly" is what would actually abort. "360" would
// NOT have — ->> unquotes, so it casts to 360 cleanly — and the guard skips
// it anyway, because the predicate asks whether the document is the shape the
// schema declares rather than whether the cast happens to work.
//
// What a skipped row then costs is pinned by the unit test
// TestJobConfig_AHandWrittenStringIntervalDoesNotDecode: it does not decode
// into JobConfig, so Load fails for the whole read. That is why such a row has
// to be found and fixed by hand — not a reason to unguard the cast.
func TestMigration_UpSkipsANonNumericInterval(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	for _, raw := range []string{`{"interval_min":"nightly"}`, `{"interval_min":"360"}`} {
		t.Run(raw, func(t *testing.T) {
			_, err := pool.Exec(ctx, `DELETE FROM backup_schedule`)
			require.NoError(t, err)
			seedSchedule(ctx, t, pool, JobMirror, raw)
			// The bystander: the skip must fail SOFT — one row the migration
			// cannot translate must not cost the rows it can.
			seedSchedule(ctx, t, pool, JobDump, `{"times":["06:00","18:00"]}`)

			_, err = pool.Exec(ctx, migrationSQL(t, "000043_backup_schedule_unified.up.sql"))
			require.NoError(t, err, "one hand-written row must not abort the migration of the others")

			docs := scheduleDocs(ctx, t, pool)
			var original map[string]any
			require.NoError(t, json.Unmarshal([]byte(raw), &original))
			assert.Equal(t, original, docs[JobMirror],
				"the row is left exactly as it was — untranslatable, never half-translated")
			assert.Equal(t, "times", docs[JobDump]["mode"], "the rows around it still migrated")
			assert.Equal(t, []any{"06:00", "18:00"}, docs[JobDump]["times"])
		})
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
