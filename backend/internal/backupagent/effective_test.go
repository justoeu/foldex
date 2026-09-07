package backupagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolPtr(b bool) *bool { return &b }

func allDays() []string { return []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} }

func mustAnchor(t *testing.T, raw string) Anchor {
	t.Helper()
	a, err := ParseAnchor(raw)
	require.NoError(t, err)
	return a
}

func TestEffectiveTiming_RowWinsAndInvalidRowFallsBack(t *testing.T) {
	cfg := Config{DumpAt: mustAnchor(t, "03:30")}

	env := EffectiveTiming(JobDump, cfg, nil)
	assert.Equal(t, "env", env.Source)
	assert.Equal(t, "03:30", env.String())

	row := &JobConfig{Mode: "times", Times: []string{"06:00", "18:00"}, Weekdays: allDays()}
	db := EffectiveTiming(JobDump, cfg, row)
	assert.Equal(t, "db", db.Source)
	assert.Equal(t, "06:00, 18:00", db.String())

	// An invalid row degrades to the baseline, never to a dead job.
	bad := &JobConfig{Mode: "times", Times: []string{}}
	assert.Equal(t, "env", EffectiveTiming(JobDump, cfg, bad).Source)
}

func TestEnvTiming_WeeklyAnchorBecomesAWeekdaySet(t *testing.T) {
	cfg := Config{DrillAt: mustAnchor(t, "01:00 sun")}
	timing := envTiming(JobDrill, cfg)
	assert.Equal(t, []time.Weekday{time.Sunday}, timing.Weekdays)
	assert.Equal(t, "01:00 · sun", timing.String(),
		"the weekday belongs to the timing now — leaving it on the anchor too would say \"sun\" twice")
}

func TestEffectiveTiming_MirrorRowCannotSwitchTheMirrorOn(t *testing.T) {
	off := Config{MirrorIntervalMin: 0}
	row := &JobConfig{Mode: "interval", IntervalMin: 60}
	timing := EffectiveTiming(JobMirror, off, row)
	assert.False(t, timing.Enabled(),
		"with the mirror off in env there is no source client in the process — a row cannot conjure one (INV-173)")

	on := Config{MirrorIntervalMin: 360}
	timing = EffectiveTiming(JobMirror, on, row)
	assert.Equal(t, time.Hour, timing.Interval)
	assert.Equal(t, "db", timing.Source)
}

func TestEffectiveTiming_UserZipRowMayDisable(t *testing.T) {
	cfg := Config{UserZipAt: mustAnchor(t, "02:30")}
	row := &JobConfig{Mode: "times", Enabled: boolPtr(false)}
	timing := EffectiveTiming(JobUserZip, cfg, row)
	assert.Equal(t, "db", timing.Source)
	assert.False(t, timing.Enabled(),
		"user_zip is a product convenience — the one job a row may switch off")
}

func TestAgent_CapabilityGatesScheduleRows(t *testing.T) {
	// No identity: a drill row must be ignored, not honoured into a job that
	// cannot decrypt what it restores.
	agent, err := New(Config{AllowPlaintext: true}, nil, newRecorderStore(), nil, testLogger())
	require.NoError(t, err)
	rows := map[string]ScheduleRow{
		JobDrill: {Job: JobDrill, Config: JobConfig{Mode: "times", Times: []string{"01:00"}, Weekdays: []string{"sun"}}},
	}
	timings := agent.computeTimings(rows)
	assert.False(t, timings[JobDrill].Enabled())

	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	idFile := filepath.Join(t.TempDir(), "identity.txt")
	require.NoError(t, os.WriteFile(idFile, []byte(identity.String()+"\n"), 0o600))
	withIdentity, err := New(Config{AllowPlaintext: true, AgeIdentityFile: idFile}, nil, newRecorderStore(), nil, testLogger())
	require.NoError(t, err)
	timings = withIdentity.computeTimings(rows)
	assert.True(t, timings[JobDrill].Enabled())
	assert.Equal(t, "01:00 · sun", timings[JobDrill].String())
}

func TestAgent_StateReportsEveryJobEvenWhenUnregistered(t *testing.T) {
	agent, err := New(Config{AllowPlaintext: true}, nil, newRecorderStore(), nil, testLogger())
	require.NoError(t, err)
	state := agent.agentState()

	report, ok := state.Jobs[JobUserZip]
	require.True(t, ok, "an absent job would render as unknown instead of \"unavailable, and here is why\"")
	assert.False(t, report.Capable)
	assert.Equal(t, "no_source_credentials", report.Reason)
	assert.Equal(t, JobConfig{}, report.Baseline,
		"a job this process cannot run has no baseline to pre-fill a form with")

	// The mirror too: absent from the report, the UI offered editors for a
	// row no process would ever read — a schedule configured and ignored
	// forever, the exact dishonesty the heartbeat exists to prevent.
	mirror, ok := state.Jobs[JobMirror]
	require.True(t, ok)
	assert.False(t, mirror.Capable)
	assert.Equal(t, "mirror_off", mirror.Reason)
}

// The heartbeat carries the env agenda as a DOCUMENT, not only as the display
// string: without it the admin form cannot open on the baseline, and "env is
// the first option, the row is the override" is unreachable from the UI.
func TestAgent_StateCarriesTheEnvBaseline(t *testing.T) {
	cfg := Config{AllowPlaintext: true, DumpAt: mustAnchor(t, "03:30")}
	agent, err := New(cfg, nil, newRecorderStore(), nil, testLogger())
	require.NoError(t, err)

	baseline := agent.agentState().Jobs[JobDump].Baseline
	assert.Equal(t, "times", baseline.Mode)
	assert.Equal(t, []string{"03:30"}, baseline.Times)
	assert.Equal(t, allDays(), baseline.Weekdays)
	require.NoError(t, ValidateJobConfig(JobDump, baseline),
		"the baseline the form opens on must be a document the form can save back")
}
