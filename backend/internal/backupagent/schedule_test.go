package backupagent

import (
	"encoding/json"
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
	// msg is the distinguishing part of the expected refusal, required on
	// every negative case. A bare assert.Error passes when the row is refused
	// for the WRONG reason, and these messages are a contract: the handler
	// returns them verbatim and the UI renders them without restating.
	msg string
}

// The floors are the security of ADR-44: a row may tune the agenda, never
// lower protection under the env baseline. Each refusal below is one way a
// row could have done exactly that.
func TestValidateJobConfig_FloorsHold(t *testing.T) {
	cases := []validateCase{
		{name: "mode is required", job: JobDump, cfg: JobConfig{Times: []string{"03:30"}, Weekdays: allDays()}, msg: `needs "mode"`},
		{name: "mode must be known", job: JobDump, cfg: JobConfig{Mode: "cron", Times: []string{"03:30"}, Weekdays: allDays()}, msg: `needs "mode"`},

		{name: "times mode carrying an interval", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"sun"}, IntervalMin: 60}, msg: `mode "times" does not carry "interval_min"`},
		{name: "interval mode carrying times", job: JobDrill, cfg: JobConfig{Mode: "interval", IntervalMin: 60, Times: []string{"03:30"}}, msg: `mode "interval" does not carry "times" or "weekdays"`},
		{name: "interval mode carrying weekdays", job: JobDrill, cfg: JobConfig{Mode: "interval", IntervalMin: 60, Weekdays: []string{"sun"}}, msg: `mode "interval" does not carry "times" or "weekdays"`},

		// An explicit empty array is ABSENT, not present. Both gates that ask
		// "does this document carry an agenda?" — the disabled one and the
		// interval one — have to answer it the same way, and they once did
		// not: one counted length, the other nil-ness, so {"times":[]} was
		// accepted by one gate and refused by the other. [] states no times;
		// there is nothing in it to honour, and whether a client omits the
		// key or serializes an empty list is a choice its JSON encoder makes
		// (Go's own omitempty drops it, JSON.stringify keeps it), not a
		// difference in what the operator asked for.
		{name: "interval mode carrying an explicit empty times", job: JobDrill, cfg: JobConfig{Mode: "interval", IntervalMin: 60, Times: []string{}}, ok: true},
		{name: "interval mode carrying an explicit empty weekdays", job: JobDrill, cfg: JobConfig{Mode: "interval", IntervalMin: 60, Weekdays: []string{}}, ok: true},
		{name: "user_zip disabled carrying explicit empty arrays", job: JobUserZip, cfg: JobConfig{Mode: "times", Enabled: boolPtr(false), Times: []string{}, Weekdays: []string{}}, ok: true},

		{name: "enabled on dump", job: JobDump, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: allDays(), Enabled: boolPtr(true)}, msg: "dump cannot be switched off"},
		{name: "enabled on drill", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"sun"}, Enabled: boolPtr(false)}, msg: "drill cannot be switched off"},
		{name: "enabled on mirror", job: JobMirror, cfg: JobConfig{Mode: "interval", IntervalMin: 60, Enabled: boolPtr(false)}, msg: "mirror cannot be switched off"},

		{name: "zero times is the floor", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{}, Weekdays: []string{"sun"}}, msg: "needs between 1 and 6 wall times"},
		{name: "one time", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"sun"}}, ok: true},
		{name: "six times", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"00:00", "04:00", "08:00", "12:00", "16:00", "20:00"}, Weekdays: []string{"sun"}}, ok: true},
		{name: "seven times", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"00:00", "01:00", "02:00", "03:00", "04:00", "05:00", "06:00"}, Weekdays: []string{"sun"}}, msg: "needs between 1 and 6 wall times"},
		{name: "repeated time", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"03:30", "03:30"}, Weekdays: []string{"sun"}}, msg: `drill time "03:30" repeats`},
		{name: "unparseable time", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"25:99"}, Weekdays: []string{"sun"}}, msg: `is not a valid 24h wall time`},
		{name: "a time carrying a weekday", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"03:30 sun"}, Weekdays: []string{"sun"}}, msg: `the weekday belongs in "weekdays"`},

		{name: "zero weekdays", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{}}, msg: "an agenda that fires on no day"},
		{name: "absent weekdays", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}}, msg: "an agenda that fires on no day"},
		{name: "invalid weekday", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"someday"}}, msg: `weekday "someday" is not one of sun..sat`},
		{name: "repeated weekday", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"sun", "sun"}}, msg: `drill weekday "sun" repeats`},

		// The dump is the instance's disaster floor: four days a week means
		// three consecutive days with no dump at all, and no other job's
		// agenda buys that back.
		{name: "dump on four weekdays", job: JobDump, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"mon", "tue", "wed", "thu"}}, msg: "dump needs at least 5 weekdays, got 4"},
		{name: "dump on five weekdays", job: JobDump, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"mon", "tue", "wed", "thu", "fri"}}, ok: true},
		{name: "drill on one weekday", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"01:00"}, Weekdays: []string{"sun"}}, ok: true},
		{name: "mirror on one weekday", job: JobMirror, cfg: JobConfig{Mode: "times", Times: []string{"01:00"}, Weekdays: []string{"sun"}}, ok: true},
		{name: "user_zip on one weekday", job: JobUserZip, cfg: JobConfig{Mode: "times", Times: []string{"01:00"}, Weekdays: []string{"sun"}}, ok: true},

		// The document the admin form PUTs with the switch ON — the one live
		// payload of the only job that carries "enabled" at all. Every other
		// user_zip case here is a refusal or a switched-off row, so without
		// these two the accepting path of the enabled shape was only ever
		// exercised incidentally, inside TestNormalize.
		{name: "user_zip enabled with a full times agenda", job: JobUserZip, cfg: JobConfig{Mode: "times", Times: []string{"02:30", "14:30"}, Weekdays: allDays(), Enabled: boolPtr(true)}, ok: true},
		{name: "user_zip enabled on an interval", job: JobUserZip, cfg: JobConfig{Mode: "interval", IntervalMin: 360, Enabled: boolPtr(true)}, ok: true},

		{name: "user_zip disabled needs no agenda", job: JobUserZip, cfg: JobConfig{Mode: "times", Enabled: boolPtr(false)}, ok: true},
		{name: "user_zip disabled carrying an agenda", job: JobUserZip, cfg: JobConfig{Mode: "times", Enabled: boolPtr(false), Times: []string{"02:30"}, Weekdays: allDays()}, msg: "a disabled user_zip carries no agenda"},
		{name: "user_zip enabled still needs an agenda", job: JobUserZip, cfg: JobConfig{Mode: "times", Enabled: boolPtr(true)}, msg: "needs between 1 and 6 wall times"},

		{name: "legacy time refused", job: JobUserZip, cfg: JobConfig{Mode: "times", Time: "02:30", Weekdays: allDays()}, msg: "are the previous schedule vocabulary and are read-only"},
		{name: "legacy weekday refused", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"01:00"}, Weekday: "sun"}, msg: "are the previous schedule vocabulary and are read-only"},

		{name: "unknown job", job: "vacuum", cfg: JobConfig{Mode: "interval", IntervalMin: 60}, msg: `unknown job "vacuum"`},
	}
	for _, job := range []string{JobDump, JobDrill, JobMirror, JobUserZip} {
		bounds := job + " interval must be between 15 and 1440 minutes"
		cases = append(cases,
			validateCase{name: job + " interval under the floor", job: job, cfg: JobConfig{Mode: "interval", IntervalMin: MinIntervalMin - 1}, msg: bounds},
			validateCase{name: job + " interval at the floor", job: job, cfg: JobConfig{Mode: "interval", IntervalMin: MinIntervalMin}, ok: true},
			validateCase{name: job + " interval at the ceiling", job: job, cfg: JobConfig{Mode: "interval", IntervalMin: MaxIntervalMin}, ok: true},
			validateCase{name: job + " interval over the ceiling", job: job, cfg: JobConfig{Mode: "interval", IntervalMin: MaxIntervalMin + 1}, msg: bounds},
			validateCase{name: job + " interval zero cannot switch the job off", job: job, cfg: JobConfig{Mode: "interval"}, msg: bounds},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateJobConfig(tc.job, tc.cfg)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			require.NotEmpty(t, tc.msg, "a negative case without an expected message passes for any refusal")
			assert.ErrorContains(t, err, tc.msg)
		})
	}
}

// The refusal is what the handler returns verbatim and what the UI renders
// without restating: it has to carry the real numbers (INV-169's reasoning).
func TestValidateJobConfig_RefusalsNameTheRealNumbers(t *testing.T) {
	// The whole sentence, not the digit: "5" also appears in a message that
	// merely mentions 15 or 1440, so a single digit proves nothing about which
	// number the operator is being shown.
	err := ValidateJobConfig(JobDump, JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"mon", "tue"}})
	require.Error(t, err)
	assert.EqualError(t, err, "dump needs at least 5 weekdays, got 2")

	err = ValidateJobConfig(JobMirror, JobConfig{Mode: "interval", IntervalMin: 5})
	require.Error(t, err)
	assert.EqualError(t, err, "mirror interval must be between 15 and 1440 minutes — a row tunes the cadence, it cannot switch the job off")

	err = ValidateJobConfig(JobDrill, JobConfig{Mode: "times", Times: []string{}, Weekdays: []string{"sun"}})
	require.Error(t, err)
	assert.EqualError(t, err, "drill needs between 1 and 6 wall times — the floor is one run per scheduled day, never zero")
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

// nextFirings replays the scheduler's real loop — fire, then recompute from
// the moment it fired — which is what the run loop does with
// t.Next(time.Now()) after every execution. Across a DST transition the
// difference matters: recomputing from the FIRING INSTANT is not the same as
// recomputing from an arbitrary "now" the next day, and only the former is
// what the agent actually does.
func nextFirings(timing Timing, from time.Time, n int) []string {
	out := make([]string, 0, n)
	now := from
	for i := 0; i < n; i++ {
		now = timing.Next(now)
		out = append(out, now.Format("2006-01-02 15:04:05 MST"))
		now = now.Add(time.Second)
	}
	return out
}

// A DST transition is the one day a year when the agenda CANNOT honour its
// wall time: on spring forward the time does not exist, on fall back it exists
// twice. What Next promises is therefore not the wall time — it is that the
// agenda keeps moving and settles back onto it, because Next is RECOMPUTED
// rather than advanced by a fixed interval. SDD-OPS-BACKUP accepts that a slot
// may shift, be skipped or fire twice on a transition; the two tests below pin
// WHICH of those actually happens, so a change is noticed instead of absorbed.
// Neither asserts a requirement the code does not already meet.
func TestTiming_NextAcrossSpringForward(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	// 2026-03-08 02:00 EST jumps straight to 03:00 EDT: 02:30 never happens.
	timing := Timing{Anchors: []Anchor{mustAnchor(t, "02:30")}}

	// The whole disturbance, start to finish. It is TWO days long, not one,
	// and the extra run lands the day AFTER the transition:
	//
	//   03-08  time.Date resolves the non-existent 02:30 to the instant it
	//          would have been at the pre-transition offset, which renders as
	//          01:30 EST — an hour EARLY, not an hour late.
	//   03-09  the recomputation starts from that shifted instant, so
	//          AddDate carries 01:30 into the next day and fires early once
	//          more; recomputing from THAT finds the day's real 02:30 still
	//          ahead and fires it too. Two runs in one day.
	//   03-10  settled back on the wall time and staying there.
	//
	// A duplicate run is harmless — the second is fast and retention prunes
	// it — and nothing is skipped, which is what the SDD accepts.
	assert.Equal(t, []string{
		"2026-03-08 01:30:00 EST",
		"2026-03-09 01:30:00 EDT",
		"2026-03-09 02:30:00 EDT",
		"2026-03-10 02:30:00 EDT",
		"2026-03-11 02:30:00 EDT",
	}, nextFirings(timing, time.Date(2026, 3, 7, 3, 0, 0, 0, loc), 5))
}

func TestTiming_NextAcrossFallBack(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	// 2026-11-01 02:00 EDT falls back to 01:00 EST, so 01:30 happens TWICE —
	// once at -0400 and again an hour of real time later at -0500. An anchor
	// inside the repeated hour is the ambiguous case.
	timing := Timing{Anchors: []Anchor{mustAnchor(t, "01:30")}}

	// The FIRST pass wins and the second is not a second run: time.Date
	// resolves the ambiguous wall time at the offset still in effect, and the
	// recomputation from that firing finds the day's candidate already past.
	// The repeated hour costs nothing and the agenda never leaves 01:30.
	assert.Equal(t, []string{
		"2026-11-01 01:30:00 EDT",
		"2026-11-02 01:30:00 EST",
		"2026-11-03 01:30:00 EST",
		"2026-11-04 01:30:00 EST",
	}, nextFirings(timing, time.Date(2026, 10, 31, 2, 0, 0, 0, loc), 4))

	// The other half of "not a second run": a scheduler that woke up INSIDE
	// the repeated hour — a restart, a slow job — also moves on to tomorrow
	// instead of firing 01:30 again sixty minutes later.
	firstPass := time.Date(2026, 11, 1, 1, 30, 0, 0, loc)
	insideTheRepeatedHour := firstPass.Add(45 * time.Minute)
	require.Equal(t, "2026-11-01 01:15:00 EST", insideTheRepeatedHour.Format("2006-01-02 15:04:05 MST"),
		"precondition: this instant is the second pass of 01:xx, at the post-transition offset")
	assert.Equal(t, "2026-11-02 01:30:00 EST",
		timing.Next(insideTheRepeatedHour).Format("2006-01-02 15:04:05 MST"))
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

	// A CLUSTERED day set is where the widest silence sits INSIDE the week
	// rather than across its edge: sun 00:00 → fri 00:00 is 120h, while the
	// wraparound fri → sun is only 48h. Every other case here lets the
	// wraparound win, so without this one the interior comparison could
	// return the wraparound gap and no test would notice — a catch-up that
	// compares against 48h would call a job late three times a week.
	clustered := Timing{
		Anchors:  []Anchor{mustAnchor(t, "00:00")},
		Weekdays: []time.Weekday{time.Sunday, time.Friday},
	}
	assert.Equal(t, 120*time.Hour, clustered.MaxGap())

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

// Both migration 000043 statements that cast interval_min to int are guarded
// on jsonb_typeof(...) = 'number', so a hand-written string interval is
// SKIPPED instead of aborting the statement and taking every other job's
// rewrite with it. This is the Go half of that guard: what the skipped row
// then costs, which is more than the mirror's own agenda — the document does
// not decode into JobConfig at all, so ScheduleStore.Load fails for the WHOLE
// read and every job keeps the timing it already had.
//
// It covers the quoted number as well as the plainly bad value, because the
// two are NOT the same in SQL: ->> unquotes, so "360" would have cast cleanly
// and only "nightly" aborts. The guard skips both — it asks whether the
// document is the shape the schema declares — and Go refuses both for the
// same reason, which is what makes skipping both the right answer.
func TestJobConfig_AHandWrittenStringIntervalDoesNotDecode(t *testing.T) {
	for _, raw := range []string{`{"interval_min":"360"}`, `{"interval_min":"nightly"}`} {
		var cfg JobConfig
		err := json.Unmarshal([]byte(raw), &cfg)
		require.Error(t, err, "raw=%s: a quoted interval is not the shape the schema declares", raw)
		assert.Contains(t, err.Error(), "interval_min", "raw=%s", raw)
	}

	// The unquoted number is the shape the guard lets through, so the two
	// refusals above are about the type and nothing else.
	var cfg JobConfig
	require.NoError(t, json.Unmarshal([]byte(`{"interval_min":360}`), &cfg))
	assert.Equal(t, 360, cfg.IntervalMin)
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

// The legacy validator accepted {"enabled":false,"time":"02:30"} — switched
// off, with the agenda it would have followed still written down. The unified
// one refuses the pair, so carrying the agenda across would make the row
// invalid, fall user_zip back to the env baseline and START IT RUNNING AGAIN,
// reversing the one thing the operator explicitly asked for. The agenda goes,
// the "off" stays.
func TestNormalize_ADisabledLegacyRowDropsItsAgenda(t *testing.T) {
	off := JobConfig{Enabled: boolPtr(false), Time: "02:30"}.normalized(JobUserZip)
	require.NotNil(t, off.Enabled)
	assert.False(t, *off.Enabled)
	assert.Empty(t, off.Times)
	assert.Empty(t, off.Weekdays)
	assert.NoError(t, ValidateJobConfig(JobUserZip, off),
		"a row that normalizes into a document the floors refuse is a job silently switched back ON")

	withTimes := JobConfig{Enabled: boolPtr(false), Times: []string{"02:30", "14:30"}}.normalized(JobUserZip)
	assert.Empty(t, withTimes.Times)
	assert.NoError(t, ValidateJobConfig(JobUserZip, withTimes))

	withInterval := JobConfig{Enabled: boolPtr(false), IntervalMin: 360}.normalized(JobUserZip)
	assert.Zero(t, withInterval.IntervalMin)
	assert.NoError(t, ValidateJobConfig(JobUserZip, withInterval))
}

// "enabled" belongs to user_zip alone, and it must survive whichever legacy
// shape carried it — the interval branch used to replace the whole struct and
// drop it on the floor.
func TestNormalize_EnabledSurvivesEveryBranchAndOnlyOnUserZip(t *testing.T) {
	interval := JobConfig{Enabled: boolPtr(true), IntervalMin: 360}.normalized(JobUserZip)
	assert.Equal(t, "interval", interval.Mode)
	assert.Equal(t, 360, interval.IntervalMin)
	require.NotNil(t, interval.Enabled)
	assert.True(t, *interval.Enabled)

	times := JobConfig{Enabled: boolPtr(true), Times: []string{"02:30"}}.normalized(JobUserZip)
	require.NotNil(t, times.Enabled)
	assert.True(t, *times.Enabled)

	// On every other job it is stripped: kept, it would normalize into a
	// document ValidateJobConfig refuses forever, pinning the job to the env
	// baseline behind nothing louder than a warning.
	for _, job := range []string{JobDump, JobDrill, JobMirror} {
		for _, legacy := range []JobConfig{
			{Enabled: boolPtr(true), Time: "02:30"},
			{Enabled: boolPtr(false), Time: "02:30"},
			{Enabled: boolPtr(true), IntervalMin: 360},
		} {
			got := legacy.normalized(job)
			assert.Nil(t, got.Enabled, "job=%s", job)
			assert.NoError(t, ValidateJobConfig(job, got), "job=%s", job)
		}
	}
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
