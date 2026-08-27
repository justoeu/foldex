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

type validateCase struct {
	name string
	job  string
	cfg  JobConfig
	ok   bool
}

// The floors are the security of ADR-44: a row may tune the agenda, never
// lower protection under the env baseline. Each refusal below is one way a
// row could have done exactly that.
func TestValidateJobConfig_FloorsHold(t *testing.T) {
	cases := []validateCase{
		{"mode is required", JobDump, JobConfig{Times: []string{"03:30"}, Weekdays: allDays()}, false},
		{"mode must be known", JobDump, JobConfig{Mode: "cron", Times: []string{"03:30"}, Weekdays: allDays()}, false},

		{"times mode carrying an interval", JobDrill, JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"sun"}, IntervalMin: 60}, false},
		{"interval mode carrying times", JobDrill, JobConfig{Mode: "interval", IntervalMin: 60, Times: []string{"03:30"}}, false},
		{"interval mode carrying weekdays", JobDrill, JobConfig{Mode: "interval", IntervalMin: 60, Weekdays: []string{"sun"}}, false},

		{"enabled on dump", JobDump, JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: allDays(), Enabled: boolPtr(true)}, false},
		{"enabled on drill", JobDrill, JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"sun"}, Enabled: boolPtr(false)}, false},
		{"enabled on mirror", JobMirror, JobConfig{Mode: "interval", IntervalMin: 60, Enabled: boolPtr(false)}, false},

		{"zero times is the floor", JobDrill, JobConfig{Mode: "times", Times: []string{}, Weekdays: []string{"sun"}}, false},
		{"one time", JobDrill, JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"sun"}}, true},
		{"six times", JobDrill, JobConfig{Mode: "times", Times: []string{"00:00", "04:00", "08:00", "12:00", "16:00", "20:00"}, Weekdays: []string{"sun"}}, true},
		{"seven times", JobDrill, JobConfig{Mode: "times", Times: []string{"00:00", "01:00", "02:00", "03:00", "04:00", "05:00", "06:00"}, Weekdays: []string{"sun"}}, false},
		{"repeated time", JobDrill, JobConfig{Mode: "times", Times: []string{"03:30", "03:30"}, Weekdays: []string{"sun"}}, false},
		{"unparseable time", JobDrill, JobConfig{Mode: "times", Times: []string{"25:99"}, Weekdays: []string{"sun"}}, false},
		{"a time carrying a weekday", JobDrill, JobConfig{Mode: "times", Times: []string{"03:30 sun"}, Weekdays: []string{"sun"}}, false},

		{"zero weekdays", JobDrill, JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{}}, false},
		{"absent weekdays", JobDrill, JobConfig{Mode: "times", Times: []string{"03:30"}}, false},
		{"invalid weekday", JobDrill, JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"someday"}}, false},
		{"repeated weekday", JobDrill, JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"sun", "sun"}}, false},

		// The dump is the instance's disaster floor: four days a week means
		// three consecutive days with no dump at all, and no other job's
		// agenda buys that back.
		{"dump on four weekdays", JobDump, JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"mon", "tue", "wed", "thu"}}, false},
		{"dump on five weekdays", JobDump, JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"mon", "tue", "wed", "thu", "fri"}}, true},
		{"drill on one weekday", JobDrill, JobConfig{Mode: "times", Times: []string{"01:00"}, Weekdays: []string{"sun"}}, true},
		{"mirror on one weekday", JobMirror, JobConfig{Mode: "times", Times: []string{"01:00"}, Weekdays: []string{"sun"}}, true},
		{"user_zip on one weekday", JobUserZip, JobConfig{Mode: "times", Times: []string{"01:00"}, Weekdays: []string{"sun"}}, true},

		{"user_zip disabled needs no agenda", JobUserZip, JobConfig{Mode: "times", Enabled: boolPtr(false)}, true},
		{"user_zip disabled carrying an agenda", JobUserZip, JobConfig{Mode: "times", Enabled: boolPtr(false), Times: []string{"02:30"}, Weekdays: allDays()}, false},
		{"user_zip enabled still needs an agenda", JobUserZip, JobConfig{Mode: "times", Enabled: boolPtr(true)}, false},

		{"legacy time refused", JobUserZip, JobConfig{Mode: "times", Time: "02:30", Weekdays: allDays()}, false},
		{"legacy weekday refused", JobDrill, JobConfig{Mode: "times", Times: []string{"01:00"}, Weekday: "sun"}, false},

		{"unknown job", "vacuum", JobConfig{Mode: "interval", IntervalMin: 60}, false},
	}
	for _, job := range []string{JobDump, JobDrill, JobMirror, JobUserZip} {
		cases = append(cases,
			validateCase{job + " interval under the floor", job, JobConfig{Mode: "interval", IntervalMin: MinIntervalMin - 1}, false},
			validateCase{job + " interval at the floor", job, JobConfig{Mode: "interval", IntervalMin: MinIntervalMin}, true},
			validateCase{job + " interval at the ceiling", job, JobConfig{Mode: "interval", IntervalMin: MaxIntervalMin}, true},
			validateCase{job + " interval over the ceiling", job, JobConfig{Mode: "interval", IntervalMin: MaxIntervalMin + 1}, false},
			validateCase{job + " interval zero cannot switch the job off", job, JobConfig{Mode: "interval"}, false},
		)
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

// The refusal is what the handler returns verbatim and what the UI renders
// without restating: it has to carry the real numbers (INV-169's reasoning).
func TestValidateJobConfig_RefusalsNameTheRealNumbers(t *testing.T) {
	err := ValidateJobConfig(JobDump, JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"mon", "tue"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "5")

	err = ValidateJobConfig(JobMirror, JobConfig{Mode: "interval", IntervalMin: 5})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "15")
	assert.Contains(t, err.Error(), "1440")

	err = ValidateJobConfig(JobDrill, JobConfig{Mode: "times", Times: []string{}, Weekdays: []string{"sun"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "6")
}

func mustAnchor(t *testing.T, raw string) Anchor {
	t.Helper()
	a, err := ParseAnchor(raw)
	require.NoError(t, err)
	return a
}

func TestTiming_NextPicksTheNearestTime(t *testing.T) {
	timing := Timing{Anchors: []Anchor{mustAnchor(t, "03:30"), mustAnchor(t, "15:30")}}
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)

	assert.Equal(t, time.Date(2026, 8, 26, 15, 30, 0, 0, time.UTC), timing.Next(now))
	assert.Equal(t, time.Date(2026, 8, 27, 3, 30, 0, 0, time.UTC),
		timing.Next(time.Date(2026, 8, 26, 16, 0, 0, 0, time.UTC)))
}

func TestTiming_NextDailyFiresTodayOrTomorrow(t *testing.T) {
	timing := Timing{Anchors: []Anchor{mustAnchor(t, "03:30")}}
	loc := time.FixedZone("test", -3*3600)

	before := time.Date(2026, 8, 25, 1, 0, 0, 0, loc)
	assert.Equal(t, time.Date(2026, 8, 25, 3, 30, 0, 0, loc), timing.Next(before))

	// Exactly at the time: strictly-after, so it rolls to tomorrow — firing
	// twice for one instant is how a scheduler double-runs a slot.
	at := time.Date(2026, 8, 25, 3, 30, 0, 0, loc)
	assert.Equal(t, time.Date(2026, 8, 26, 3, 30, 0, 0, loc), timing.Next(at))

	after := time.Date(2026, 8, 25, 22, 0, 0, 0, loc)
	assert.Equal(t, time.Date(2026, 8, 26, 3, 30, 0, 0, loc), timing.Next(after))
}

func TestTiming_NextRespectsTheWeekdaySet(t *testing.T) {
	// 2026-08-25 is a Tuesday.
	timing := Timing{
		Anchors:  []Anchor{mustAnchor(t, "03:30")},
		Weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday},
	}
	tuesday := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 8, 26, 3, 30, 0, 0, time.UTC), timing.Next(tuesday))

	// On a scheduled day but past the time: the next SCHEDULED day, not
	// tomorrow — a bare time no longer knows which days it fires on.
	wednesdayLate := time.Date(2026, 8, 26, 23, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 8, 28, 3, 30, 0, 0, time.UTC), timing.Next(wednesdayLate))

	// A single weekday wraps a full week.
	weekly := Timing{Anchors: []Anchor{mustAnchor(t, "04:30")}, Weekdays: []time.Weekday{time.Sunday}}
	assert.Equal(t, time.Date(2026, 8, 30, 4, 30, 0, 0, time.UTC), weekly.Next(tuesday))
	sundayLate := time.Date(2026, 8, 30, 23, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 9, 6, 4, 30, 0, 0, time.UTC), weekly.Next(sundayLate))

	// Several times on a restricted day set: the nearest of the product.
	twice := Timing{
		Anchors:  []Anchor{mustAnchor(t, "03:30"), mustAnchor(t, "15:30")},
		Weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday},
	}
	assert.Equal(t, time.Date(2026, 8, 26, 3, 30, 0, 0, time.UTC), twice.Next(tuesday))
}

func TestTiming_NextRecomputesAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	timing := Timing{Anchors: []Anchor{mustAnchor(t, "02:30")}}

	// Spring forward 2026-03-08: 02:30 does not exist. time.Date normalizes it
	// into the following hour, so the time still fires exactly once — the
	// documented "may shift on the transition day" behaviour, not a skip.
	now := time.Date(2026, 3, 8, 1, 0, 0, 0, loc)
	next := timing.Next(now)
	assert.True(t, next.After(now))
	assert.Equal(t, 8, next.Day())
}

func TestTiming_MaxGapIsTheWidestGapOfTheWeekGrid(t *testing.T) {
	// 03:30 → 15:30 is 12h; 15:30 → 03:30 wraps in 12h — symmetric.
	even := Timing{Anchors: []Anchor{mustAnchor(t, "03:30"), mustAnchor(t, "15:30")}}
	assert.Equal(t, 12*time.Hour, even.MaxGap())

	// 00:00 → 01:00 is 1h; the wrap 01:00 → 00:00 is 23h and must win: the
	// catch-up decision compares against the longest LEGITIMATE silence.
	skewed := Timing{Anchors: []Anchor{mustAnchor(t, "00:00"), mustAnchor(t, "01:00")}}
	assert.Equal(t, 23*time.Hour, skewed.MaxGap())

	// One time on one weekday is seven days, and it falls out of the grid —
	// no special case for it.
	weekly := Timing{Anchors: []Anchor{mustAnchor(t, "01:00")}, Weekdays: []time.Weekday{time.Sunday}}
	assert.Equal(t, 7*24*time.Hour, weekly.MaxGap())

	// Twice a day, three days a week: the widest silence is fri 15:30 → mon
	// 03:30 (60h), which no single day's arithmetic could show.
	restricted := Timing{
		Anchors:  []Anchor{mustAnchor(t, "03:30"), mustAnchor(t, "15:30")},
		Weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday},
	}
	assert.Equal(t, 60*time.Hour, restricted.MaxGap())

	interval := Timing{Interval: 45 * time.Minute}
	assert.Equal(t, 45*time.Minute, interval.MaxGap())

	assert.Equal(t, time.Duration(0), Timing{}.MaxGap())
}

func TestTiming_DueFollowsMaxGap(t *testing.T) {
	timing := Timing{Anchors: []Anchor{mustAnchor(t, "03:30"), mustAnchor(t, "15:30")}}
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)

	assert.True(t, timing.Due(now, time.Time{}), "never succeeded is always due")
	assert.False(t, timing.Due(now, now.Add(-13*time.Hour)),
		"13h behind a 12h gap is inside the 25% grace")
	assert.True(t, timing.Due(now, now.Add(-16*time.Hour)),
		"16h behind a 12h gap is past gap+grace")

	daily := Timing{Anchors: []Anchor{mustAnchor(t, "03:30")}}
	assert.False(t, daily.Due(now, now.Add(-25*time.Hour)),
		"one gap plus a restart minutes later must not double-run: the 25% grace absorbs it")
	assert.False(t, daily.Due(now, now.Add(-29*time.Hour)),
		"the grace is gap/4 = 6h: 29h is still inside it — a tighter divisor would double-run restarts")
	assert.True(t, daily.Due(now, now.Add(-31*time.Hour)), "past gap+grace is due")

	disabled := Timing{}
	assert.False(t, disabled.Due(now, time.Time{}), "no schedule, no catch-up")
}

// Due used to bail on interval timings and hand the decision to a separate
// free function. One job, one catch-up rule.
func TestTiming_DueCoversIntervalTimings(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	timing := Timing{Interval: 6 * time.Hour}

	assert.True(t, timing.Due(now, time.Time{}),
		"never succeeded means due — a mirror that never ran must not look healthy")
	assert.False(t, timing.Due(now, now.Add(-2*time.Hour)), "a fresh success is not due")
	assert.False(t, timing.Due(now, now.Add(-7*time.Hour)),
		"one interval plus a restart inside the 25% grace must not double-run")
	assert.True(t, timing.Due(now, now.Add(-8*time.Hour)),
		"past interval+grace (7h30) is due")
	assert.False(t, Timing{Interval: 0}.Due(now, time.Time{}),
		"interval 0 means the job is off — never due, even having never run")
}

func TestTiming_PreviousSlotIsTheLatestPastFiring(t *testing.T) {
	timing := Timing{Anchors: []Anchor{mustAnchor(t, "03:30"), mustAnchor(t, "15:30")}}
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 8, 26, 3, 30, 0, 0, time.UTC), timing.PreviousSlot(now))

	daily := Timing{Anchors: []Anchor{mustAnchor(t, "03:30")}}
	// Restart before the time: the missed slot is YESTERDAY's — recording
	// today's would claim a slot that has not happened yet.
	early := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 8, 24, 3, 30, 0, 0, time.UTC), daily.PreviousSlot(early))
	// Exactly ON the time: that instant IS the slot.
	at := time.Date(2026, 8, 25, 3, 30, 0, 0, time.UTC)
	assert.Equal(t, at, daily.PreviousSlot(at))

	weekly := Timing{Anchors: []Anchor{mustAnchor(t, "04:30")}, Weekdays: []time.Weekday{time.Sunday}}
	// 2026-08-25 is a Tuesday; the previous Sunday is the 23rd.
	assert.Equal(t, time.Date(2026, 8, 23, 4, 30, 0, 0, time.UTC),
		weekly.PreviousSlot(time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)))
	sundayAt := time.Date(2026, 8, 23, 4, 30, 0, 0, time.UTC)
	assert.Equal(t, sundayAt, weekly.PreviousSlot(sundayAt))
	assert.Equal(t, sundayAt, weekly.PreviousSlot(sundayAt.Add(time.Minute)))
}

func TestTiming_StringRendersTimesAndDays(t *testing.T) {
	assert.Equal(t, "03:30, 15:30 · mon, wed, fri", Timing{
		Anchors:  []Anchor{mustAnchor(t, "03:30"), mustAnchor(t, "15:30")},
		Weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday},
	}.String())
	assert.Equal(t, "01:00 · sun", Timing{
		Anchors:  []Anchor{mustAnchor(t, "01:00")},
		Weekdays: []time.Weekday{time.Sunday},
	}.String())
	assert.Equal(t, "03:30", Timing{Anchors: []Anchor{mustAnchor(t, "03:30")}}.String(),
		"every day is the default: naming all seven would be noise on every log line")
	assert.Equal(t, "03:30", Timing{
		Anchors:  []Anchor{mustAnchor(t, "03:30")},
		Weekdays: []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday},
	}.String())
	assert.Equal(t, "every 360m", Timing{Interval: 6 * time.Hour}.String())
	assert.Equal(t, "disabled", Timing{}.String())
}

// ToConfig is what lets the admin form open pre-filled with the env agenda:
// the baseline has to come back as a document the form can edit and the
// validator accepts.
func TestTiming_ToConfigRoundTrips(t *testing.T) {
	restricted := Timing{
		Anchors:  []Anchor{mustAnchor(t, "03:30"), mustAnchor(t, "15:30")},
		Weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday},
	}
	cfg := restricted.ToConfig()
	assert.Equal(t, "times", cfg.Mode)
	assert.Equal(t, []string{"03:30", "15:30"}, cfg.Times)
	assert.Equal(t, []string{"mon", "wed", "fri"}, cfg.Weekdays)
	require.NoError(t, ValidateJobConfig(JobDrill, cfg))
	back := timingFromConfig(cfg)
	assert.Equal(t, restricted.Anchors, back.Anchors)
	assert.Equal(t, restricted.Weekdays, back.Weekdays)

	// A weekday-less timing emits all seven explicitly: the form shows a
	// concrete set, never an empty one the owner reads as "no days".
	daily := Timing{Anchors: []Anchor{mustAnchor(t, "03:30")}}
	cfg = daily.ToConfig()
	assert.Equal(t, allDays(), cfg.Weekdays)
	require.NoError(t, ValidateJobConfig(JobDump, cfg))
	assert.Equal(t, "03:30", timingFromConfig(cfg).String())

	interval := Timing{Interval: 6 * time.Hour}
	cfg = interval.ToConfig()
	assert.Equal(t, "interval", cfg.Mode)
	assert.Equal(t, 360, cfg.IntervalMin)
	require.NoError(t, ValidateJobConfig(JobMirror, cfg))
	assert.Equal(t, 6*time.Hour, timingFromConfig(cfg).Interval)

	assert.Equal(t, JobConfig{}, Timing{Source: "env"}.ToConfig(),
		"a job with no env agenda has no baseline document to pre-fill from")
}

// A row written before the unified shape — by an older backend, or by hand in
// SQL — must be honoured, not silently degraded to the env baseline.
func TestNormalize_LegacyRowBecomesTheUnifiedShape(t *testing.T) {
	dump := JobConfig{Times: []string{"06:00", "18:00"}}.normalized(JobDump)
	assert.Equal(t, "times", dump.Mode)
	assert.Equal(t, []string{"06:00", "18:00"}, dump.Times)
	assert.Equal(t, allDays(), dump.Weekdays)
	assert.NoError(t, ValidateJobConfig(JobDump, dump))

	drill := JobConfig{Time: "01:00", Weekday: "sun"}.normalized(JobDrill)
	assert.Equal(t, "times", drill.Mode)
	assert.Equal(t, []string{"01:00"}, drill.Times)
	assert.Equal(t, []string{"sun"}, drill.Weekdays)
	assert.NoError(t, ValidateJobConfig(JobDrill, drill))

	mirror := JobConfig{IntervalMin: 360}.normalized(JobMirror)
	assert.Equal(t, "interval", mirror.Mode)
	assert.Equal(t, 360, mirror.IntervalMin)
	assert.NoError(t, ValidateJobConfig(JobMirror, mirror))

	userZip := JobConfig{Enabled: boolPtr(true), Time: "02:30"}.normalized(JobUserZip)
	assert.Equal(t, "times", userZip.Mode)
	assert.Equal(t, []string{"02:30"}, userZip.Times)
	assert.Equal(t, allDays(), userZip.Weekdays)
	require.NotNil(t, userZip.Enabled)
	assert.True(t, *userZip.Enabled)
	assert.NoError(t, ValidateJobConfig(JobUserZip, userZip))

	off := JobConfig{Enabled: boolPtr(false)}.normalized(JobUserZip)
	assert.Equal(t, "times", off.Mode)
	require.NotNil(t, off.Enabled)
	assert.False(t, *off.Enabled)
	assert.NoError(t, ValidateJobConfig(JobUserZip, off))

	already := JobConfig{Mode: "interval", IntervalMin: 720}
	assert.Equal(t, already, already.normalized(JobMirror),
		"a document that already carries a mode is not guessed at again")
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
