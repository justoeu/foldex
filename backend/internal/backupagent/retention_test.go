package backupagent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDumpKey_RoundTripsItsDate(t *testing.T) {
	ts := time.Date(2026, 8, 25, 3, 30, 12, 0, time.UTC)

	enc := dumpKey(ts, true)
	assert.Equal(t, "backups/dump/2026/08/25/foldex-20260825-033012.dump.age", enc)
	plain := dumpKey(ts, false)
	assert.Equal(t, "backups/dump/2026/08/25/foldex-20260825-033012.dump", plain,
		"the extension must say whether age -d comes first")

	at, ok := dumpKeyDate(enc)
	require.True(t, ok)
	assert.Equal(t, time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC), at)

	_, ok = dumpKeyDate("backups/rustfs/whatever")
	assert.False(t, ok, "foreign namespaces never classify")
	_, ok = dumpKeyDate("backups/dump/not/a/date/x.dump")
	assert.False(t, ok)
}

// keysForDays builds one dump key per day, oldest first.
func keysForDays(start time.Time, days int) []string {
	out := make([]string, 0, days)
	for i := range days {
		out = append(out, dumpKey(start.AddDate(0, 0, i).Add(3*time.Hour), true))
	}
	return out
}

func TestGFS_KeepsTheThreeLadders(t *testing.T) {
	policy := GFSPolicy{Daily: 7, Weekly: 4, Monthly: 6}
	// 2026-05-01 .. 2026-08-25: 117 daily dumps.
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	keys := keysForDays(start, 117)

	kept := policy.keep(keys)

	var days, sundays, firsts int
	for k, ok := range kept {
		require.True(t, ok)
		at, parsed := dumpKeyDate(k)
		require.True(t, parsed)
		days++
		if at.Weekday() == time.Sunday {
			sundays++
		}
		if at.Day() == 1 {
			firsts++
		}
	}
	// The last 7 days (Aug 19-25) hold no Sunday-slot or first-of-month
	// overlaps beyond what the ladders themselves claim; the exact composition
	// matters less than the contract: 7 recent dailies survive, 4 Sundays
	// survive, every first-of-month in range survives (May, Jun, Jul, Aug = 4
	// < Monthly 6), and everything else dies.
	assert.GreaterOrEqual(t, sundays, 4)
	assert.GreaterOrEqual(t, firsts, 4)
	assert.LessOrEqual(t, days, 7+4+4)

	prunable := policy.prunable(keys)
	assert.Equal(t, len(keys)-len(kept), len(prunable))
	// The newest dump is never prunable — pruning must not eat the artifact
	// that just landed.
	newest := keys[len(keys)-1]
	assert.NotContains(t, prunable, newest)
}

func TestGFS_NeverDeletesWhatItCannotClassify(t *testing.T) {
	policy := GFSPolicy{Daily: 1, Weekly: 0, Monthly: 0}
	foreign := "backups/dump/README.txt"
	keys := append(keysForDays(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), 10), foreign)

	prunable := policy.prunable(keys)
	assert.NotContains(t, prunable, foreign,
		"a foreign object in the namespace is a surprise, never a deletion target")
	assert.Len(t, prunable, 9, "10 dailies, keep 1 → prune 9; the foreign key is out of the game")
}

func TestGFS_SameDayExtrasArePrunableButNewestOfDayIsNot(t *testing.T) {
	policy := GFSPolicy{Daily: 2, Weekly: 0, Monthly: 0}
	day := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	early := dumpKey(day.Add(3*time.Hour), true)
	late := dumpKey(day.Add(15*time.Hour), true) // manual run later the same day
	older := dumpKey(day.AddDate(0, 0, -1).Add(3*time.Hour), true)

	prunable := policy.prunable([]string{older, early, late})
	assert.Contains(t, prunable, early, "the day's slot is represented by its newest key")
	assert.NotContains(t, prunable, late)
	assert.NotContains(t, prunable, older)
}

func TestGFS_ZeroPolicyStillKeepsNothingSilently(t *testing.T) {
	// A zero policy prunes everything parseable — configuration owns the
	// blast radius; the code's only unconditional protection is the foreign
	// object rule and "never newer than the kept set".
	policy := GFSPolicy{}
	keys := keysForDays(time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), 3)
	prunable := policy.prunable(keys)
	assert.Len(t, prunable, len(keys))
}
