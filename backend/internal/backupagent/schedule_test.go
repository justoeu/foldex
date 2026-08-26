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

// The floors are the security of ADR-44: a row may tune the agenda, never
// lower protection under the env baseline. Each refusal below is one way a
// row could have done exactly that.
func TestValidateJobConfig_FloorsHold(t *testing.T) {
	cases := []struct {
		name string
		job  string
		cfg  JobConfig
		ok   bool
	}{
		{"dump one time", JobDump, JobConfig{Times: []string{"03:30"}}, true},
		{"dump six times", JobDump, JobConfig{Times: []string{"00:00", "04:00", "08:00", "12:00", "16:00", "20:00"}}, true},
		{"dump zero times is the floor", JobDump, JobConfig{Times: []string{}}, false},
		{"dump seven times", JobDump, JobConfig{Times: []string{"00:00", "01:00", "02:00", "03:00", "04:00", "05:00", "06:00"}}, false},
		{"dump repeated time", JobDump, JobConfig{Times: []string{"03:30", "03:30"}}, false},
		{"dump weekly time refused", JobDump, JobConfig{Times: []string{"03:30 sun"}}, false},
		{"dump foreign field refused", JobDump, JobConfig{Times: []string{"03:30"}, IntervalMin: 60}, false},
		{"dump unparseable time", JobDump, JobConfig{Times: []string{"25:99"}}, false},

		{"drill weekly", JobDrill, JobConfig{Time: "01:00", Weekday: "sun"}, true},
		{"drill without weekday", JobDrill, JobConfig{Time: "01:00"}, false},
		{"drill without time cannot switch off", JobDrill, JobConfig{Weekday: "sun"}, false},
		{"drill bad weekday", JobDrill, JobConfig{Time: "01:00", Weekday: "someday"}, false},
		{"drill foreign field refused", JobDrill, JobConfig{Time: "01:00", Weekday: "sun", Enabled: boolPtr(false)}, false},

		{"mirror in bounds", JobMirror, JobConfig{IntervalMin: 360}, true},
		{"mirror at the floor", JobMirror, JobConfig{IntervalMin: MinMirrorIntervalMin}, true},
		{"mirror under the floor", JobMirror, JobConfig{IntervalMin: MinMirrorIntervalMin - 1}, false},
		{"mirror zero cannot switch off", JobMirror, JobConfig{IntervalMin: 0}, false},
		{"mirror over the ceiling", JobMirror, JobConfig{IntervalMin: MaxMirrorIntervalMin + 1}, false},

		{"user_zip enabled with time", JobUserZip, JobConfig{Enabled: boolPtr(true), Time: "02:30"}, true},
		{"user_zip disabled needs no time", JobUserZip, JobConfig{Enabled: boolPtr(false)}, true},
		{"user_zip enabled without time", JobUserZip, JobConfig{Enabled: boolPtr(true)}, false},
		{"user_zip without enabled", JobUserZip, JobConfig{Time: "02:30"}, false},
		{"user_zip weekly time refused", JobUserZip, JobConfig{Enabled: boolPtr(true), Time: "02:30 sun"}, false},

		{"unknown job", "vacuum", JobConfig{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateJobConfig(tc.job, tc.cfg)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func mustAnchor(t *testing.T, raw string) Anchor {
	t.Helper()
	a, err := ParseAnchor(raw)
	require.NoError(t, err)
	return a
}

func TestTiming_NextPicksTheNearestAnchor(t *testing.T) {
	timing := Timing{Anchors: []Anchor{mustAnchor(t, "03:30"), mustAnchor(t, "15:30")}}
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)

	next, owner := timing.Next(now)
	assert.Equal(t, time.Date(2026, 8, 26, 15, 30, 0, 0, time.UTC), next)
	assert.Equal(t, "15:30", owner.String())

	next, owner = timing.Next(time.Date(2026, 8, 26, 16, 0, 0, 0, time.UTC))
	assert.Equal(t, time.Date(2026, 8, 27, 3, 30, 0, 0, time.UTC), next)
	assert.Equal(t, "03:30", owner.String())
}

func TestTiming_MaxGapIsTheWidestWraparound(t *testing.T) {
	// 03:30 → 15:30 is 12h; 15:30 → 03:30 wraps in 12h — symmetric.
	even := Timing{Anchors: []Anchor{mustAnchor(t, "03:30"), mustAnchor(t, "15:30")}}
	assert.Equal(t, 12*time.Hour, even.MaxGap())

	// 00:00 → 01:00 is 1h; the wrap 01:00 → 00:00 is 23h and must win: the
	// catch-up decision compares against the longest LEGITIMATE silence.
	skewed := Timing{Anchors: []Anchor{mustAnchor(t, "00:00"), mustAnchor(t, "01:00")}}
	assert.Equal(t, 23*time.Hour, skewed.MaxGap())

	single := Timing{Anchors: []Anchor{mustAnchor(t, "01:00 sun")}}
	assert.Equal(t, 7*24*time.Hour, single.MaxGap())

	interval := Timing{Interval: 45 * time.Minute}
	assert.Equal(t, 45*time.Minute, interval.MaxGap())

	// A weekly anchor mixed into a multi-anchor timing must yield the week,
	// not minutes-since-midnight arithmetic: 14h here would make Due fire a
	// spurious catch-up after ~17.5h of legitimate silence on every boot.
	// Unreachable through validation today (dump refuses weekdays) — this
	// pins the defensive path so removing it is a red test, not a latency.
	mixed := Timing{Anchors: []Anchor{mustAnchor(t, "03:00"), mustAnchor(t, "13:00 sun")}}
	assert.Equal(t, 7*24*time.Hour, mixed.MaxGap())
}

func TestTiming_DueFollowsMaxGap(t *testing.T) {
	timing := Timing{Anchors: []Anchor{mustAnchor(t, "03:30"), mustAnchor(t, "15:30")}}
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)

	assert.True(t, timing.Due(now, time.Time{}), "never succeeded is always due")
	assert.False(t, timing.Due(now, now.Add(-13*time.Hour)),
		"13h behind a 12h gap is inside the 25%% grace")
	assert.True(t, timing.Due(now, now.Add(-16*time.Hour)),
		"16h behind a 12h gap is past gap+grace")

	disabled := Timing{}
	assert.False(t, disabled.Due(now, time.Time{}), "no schedule, no catch-up")

	interval := Timing{Interval: time.Hour}
	assert.False(t, interval.Due(now, time.Time{}),
		"interval jobs use intervalDue at boot, not the anchor path")
}

func TestTiming_PreviousSlotIsTheLatestPastAnchor(t *testing.T) {
	timing := Timing{Anchors: []Anchor{mustAnchor(t, "03:30"), mustAnchor(t, "15:30")}}
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 8, 26, 3, 30, 0, 0, time.UTC), timing.PreviousSlot(now))
}

func TestEffectiveTiming_RowWinsAndInvalidRowFallsBack(t *testing.T) {
	cfg := Config{DumpAt: mustAnchor(t, "03:30")}

	env := EffectiveTiming(JobDump, cfg, nil)
	assert.Equal(t, "env", env.Source)
	assert.Equal(t, "03:30", env.String())

	row := &JobConfig{Times: []string{"06:00", "18:00"}}
	db := EffectiveTiming(JobDump, cfg, row)
	assert.Equal(t, "db", db.Source)
	assert.Equal(t, "06:00, 18:00", db.String())

	// An invalid row degrades to the baseline, never to a dead job.
	bad := &JobConfig{Times: []string{}}
	assert.Equal(t, "env", EffectiveTiming(JobDump, cfg, bad).Source)
}

func TestEffectiveTiming_MirrorRowCannotSwitchTheMirrorOn(t *testing.T) {
	off := Config{MirrorIntervalMin: 0}
	row := &JobConfig{IntervalMin: 60}
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
	row := &JobConfig{Enabled: boolPtr(false)}
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
		JobDrill: {Job: JobDrill, Config: JobConfig{Time: "01:00", Weekday: "sun"}},
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
	assert.Equal(t, "01:00 sun", timings[JobDrill].String())
}

func TestAgent_StateReportsEveryJobEvenWhenUnregistered(t *testing.T) {
	agent, err := New(Config{AllowPlaintext: true}, nil, newRecorderStore(), nil, testLogger())
	require.NoError(t, err)
	state := agent.agentState()

	report, ok := state.Jobs[JobUserZip]
	require.True(t, ok, "an absent job would render as unknown instead of \"unavailable, and here is why\"")
	assert.False(t, report.Capable)
	assert.Equal(t, "no_source_credentials", report.Reason)

	// The mirror too: absent from the report, the UI offered editors for a
	// row no process would ever read — a schedule configured and ignored
	// forever, the exact dishonesty the heartbeat exists to prevent.
	mirror, ok := state.Jobs[JobMirror]
	require.True(t, ok)
	assert.False(t, mirror.Capable)
	assert.Equal(t, "mirror_off", mirror.Reason)
}
