package backupagent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAnchor_DailyAndWeekly(t *testing.T) {
	daily, err := ParseAnchor("03:30")
	require.NoError(t, err)
	assert.True(t, daily.Enabled())
	assert.False(t, daily.Weekly)
	assert.Equal(t, 3, daily.Hour)
	assert.Equal(t, 30, daily.Minute)
	assert.Equal(t, 24*time.Hour, daily.Interval())

	weekly, err := ParseAnchor("04:30 SUN")
	require.NoError(t, err)
	assert.True(t, weekly.Weekly)
	assert.Equal(t, time.Sunday, weekly.Weekday)
	assert.Equal(t, 7*24*time.Hour, weekly.Interval())
}

func TestParseAnchor_RejectsWhatItCannotSchedule(t *testing.T) {
	for _, raw := range []string{"", "25:00", "03:60", "3h30", "03:30 someday", "03:30 sun extra", "aa:bb"} {
		_, err := ParseAnchor(raw)
		assert.Error(t, err, "raw=%q", raw)
	}
	var zero Anchor
	assert.False(t, zero.Enabled(), "the zero Anchor means job disabled, never a schedule at midnight")
}

func TestNext_DailyFiresTodayOrTomorrow(t *testing.T) {
	a, err := ParseAnchor("03:30")
	require.NoError(t, err)
	loc := time.FixedZone("test", -3*3600)

	before := time.Date(2026, 8, 25, 1, 0, 0, 0, loc)
	assert.Equal(t, time.Date(2026, 8, 25, 3, 30, 0, 0, loc), a.Next(before))

	// Exactly at the anchor: strictly-after, so it rolls to tomorrow — firing
	// twice for one instant is how a scheduler double-runs a slot.
	at := time.Date(2026, 8, 25, 3, 30, 0, 0, loc)
	assert.Equal(t, time.Date(2026, 8, 26, 3, 30, 0, 0, loc), a.Next(at))

	after := time.Date(2026, 8, 25, 22, 0, 0, 0, loc)
	assert.Equal(t, time.Date(2026, 8, 26, 3, 30, 0, 0, loc), a.Next(after))
}

func TestNext_WeeklyLandsOnTheConfiguredWeekday(t *testing.T) {
	a, err := ParseAnchor("04:30 sun")
	require.NoError(t, err)
	// 2026-08-25 is a Tuesday.
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	next := a.Next(now)
	assert.Equal(t, time.Sunday, next.Weekday())
	assert.Equal(t, time.Date(2026, 8, 30, 4, 30, 0, 0, time.UTC), next)

	// On the weekday itself but past the hour: next week, not today.
	sundayLate := time.Date(2026, 8, 30, 23, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 9, 6, 4, 30, 0, 0, time.UTC), a.Next(sundayLate))
}

func TestNext_RecomputesAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	a, errA := ParseAnchor("02:30")
	require.NoError(t, errA)

	// Spring forward 2026-03-08: 02:30 does not exist. time.Date normalizes it
	// into the following hour, so the anchor still fires exactly once — the
	// documented "may shift on the transition day" behaviour, not a skip.
	now := time.Date(2026, 3, 8, 1, 0, 0, 0, loc)
	next := a.Next(now)
	assert.True(t, next.After(now))
	assert.Equal(t, 8, next.Day())
}

func TestPreviousSlot_IsTheSlotACatchUpSatisfies(t *testing.T) {
	daily, err := ParseAnchor("03:30")
	require.NoError(t, err)

	// Restart after the anchor: the missed slot is TODAY's.
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 8, 25, 3, 30, 0, 0, time.UTC), daily.PreviousSlot(now))

	// Restart before the anchor: the missed slot is YESTERDAY's — recording
	// today's would claim a slot that has not happened yet.
	early := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 8, 24, 3, 30, 0, 0, time.UTC), daily.PreviousSlot(early))

	// Exactly ON the anchor: that instant IS the slot. Next is strictly
	// after, so the subtraction lands back on now itself.
	at := time.Date(2026, 8, 25, 3, 30, 0, 0, time.UTC)
	assert.Equal(t, at, daily.PreviousSlot(at))

	weekly, err := ParseAnchor("04:30 sun")
	require.NoError(t, err)
	// 2026-08-25 is a Tuesday; the previous Sunday is the 23rd.
	assert.Equal(t, time.Date(2026, 8, 23, 4, 30, 0, 0, time.UTC), weekly.PreviousSlot(now))
	// Exactly on the weekly anchor: same identity property.
	sundayAt := time.Date(2026, 8, 23, 4, 30, 0, 0, time.UTC)
	assert.Equal(t, sundayAt, weekly.PreviousSlot(sundayAt))
	// A minute later the slot just fired is still the previous one.
	assert.Equal(t, sundayAt, weekly.PreviousSlot(sundayAt.Add(time.Minute)))
}

func TestDue_CatchUpContract(t *testing.T) {
	a, err := ParseAnchor("03:30")
	require.NoError(t, err)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	assert.True(t, a.Due(now, time.Time{}), "never succeeded means due — the agent that never ran must not look healthy")
	assert.False(t, a.Due(now, now.Add(-2*time.Hour)), "a fresh success is not due")
	assert.False(t, a.Due(now, now.Add(-25*time.Hour)),
		"one interval plus a restart minutes later must not double-run: the 25%% grace absorbs it")
	assert.False(t, a.Due(now, now.Add(-29*time.Hour)),
		"the grace is interval/4 = 6h: 29h is still inside it — a tighter divisor would double-run restarts")
	assert.True(t, a.Due(now, now.Add(-31*time.Hour)), "past interval+grace is due")

	var disabled Anchor
	assert.False(t, disabled.Due(now, time.Time{}), "a disabled job is never due")
}

func TestAnchorString_RendersTheEnvSyntaxBack(t *testing.T) {
	daily, err := ParseAnchor("03:30")
	require.NoError(t, err)
	assert.Equal(t, "03:30", daily.String())
	weekly, err := ParseAnchor("04:05 SUN")
	require.NoError(t, err)
	assert.Equal(t, "04:05 sun", weekly.String())
	var disabled Anchor
	assert.Equal(t, "disabled", disabled.String())
}

func TestIntervalDue_CatchUpContract(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	interval := 6 * time.Hour

	assert.True(t, intervalDue(now, time.Time{}, interval),
		"never succeeded means due — a mirror that never ran must not look healthy")
	assert.False(t, intervalDue(now, now.Add(-2*time.Hour), interval), "a fresh success is not due")
	assert.False(t, intervalDue(now, now.Add(-7*time.Hour), interval),
		"one interval plus a restart inside the 25%% grace must not double-run")
	assert.True(t, intervalDue(now, now.Add(-8*time.Hour), interval),
		"past interval+grace (7h30) is due")
	assert.False(t, intervalDue(now, time.Time{}, 0),
		"interval 0 means the job is off — never due, even having never run")
}
