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

	weekly, err := ParseAnchor("04:30 SUN")
	require.NoError(t, err)
	assert.True(t, weekly.Weekly)
	assert.Equal(t, time.Sunday, weekly.Weekday)
}

func TestParseAnchor_RejectsWhatItCannotSchedule(t *testing.T) {
	for _, raw := range []string{"", "25:00", "03:60", "3h30", "03:30 someday", "03:30 sun extra", "aa:bb"} {
		_, err := ParseAnchor(raw)
		assert.Error(t, err, "raw=%q", raw)
	}
	var zero Anchor
	assert.False(t, zero.Enabled(), "the zero Anchor means job disabled, never a schedule at midnight")
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
