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

func TestDue_CatchUpContract(t *testing.T) {
	a, err := ParseAnchor("03:30")
	require.NoError(t, err)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	assert.True(t, a.Due(now, time.Time{}), "never succeeded means due — the agent that never ran must not look healthy")
	assert.False(t, a.Due(now, now.Add(-2*time.Hour)), "a fresh success is not due")
	assert.False(t, a.Due(now, now.Add(-25*time.Hour)),
		"one interval plus a restart minutes later must not double-run: the 25%% grace absorbs it")
	assert.True(t, a.Due(now, now.Add(-31*time.Hour)), "past interval+grace is due")

	var disabled Anchor
	assert.False(t, disabled.Due(now, time.Time{}), "a disabled job is never due")
}
