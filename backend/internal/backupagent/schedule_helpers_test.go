package backupagent

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backupjobs"
)

func TestAgentScheduleHelpers(t *testing.T) {
	t.Parallel()
	a := &Agent{
		logger:      slog.New(slog.DiscardHandler),
		cfg:         Config{},
		jobs:        []jobSpec{{name: backupjobs.JobDump}, {name: backupjobs.JobDrill}, {name: backupjobs.JobMirror}, {name: backupjobs.JobUserZip}},
		schedChange: make(chan struct{}),
		timings:     map[string]backupjobs.Timing{},
	}

	assert.False(t, a.registered("nope"))
	assert.True(t, a.registered(backupjobs.JobDump))

	ok, reason := a.capability(backupjobs.JobDrill)
	assert.False(t, ok)
	assert.Equal(t, "no_identity", reason)
	ok, reason = a.capability(backupjobs.JobMirror)
	assert.False(t, ok)
	assert.Equal(t, "mirror_off", reason)
	ok, reason = a.capability(backupjobs.JobUserZip)
	assert.True(t, ok)
	assert.Empty(t, reason)
	unregistered := &Agent{logger: a.logger, cfg: Config{}, jobs: []jobSpec{{name: backupjobs.JobDump}}}
	ok, reason = unregistered.capability(backupjobs.JobUserZip)
	assert.False(t, ok)
	assert.Equal(t, "no_source_credentials", reason)
	ok, reason = a.capability(backupjobs.JobDump)
	assert.True(t, ok)
	assert.Empty(t, reason)

	assert.Nil(t, a.validatedScheduleRow("dump", map[string]backupjobs.ScheduleRow{}))
	assert.Nil(t, a.validatedScheduleRow("dump", map[string]backupjobs.ScheduleRow{
		"dump": {Malformed: "not json"},
	}))
	assert.Nil(t, a.validatedScheduleRow("dump", map[string]backupjobs.ScheduleRow{
		"dump": {Config: backupjobs.JobConfig{}},
	}))
	valid := backupjobs.JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"mon", "tue", "wed", "thu", "fri"}}
	got := a.validatedScheduleRow("dump", map[string]backupjobs.ScheduleRow{"dump": {Config: valid}})
	require.NotNil(t, got)
	assert.Equal(t, "times", got.Mode)

	timings := a.computeTimings(nil)
	require.Contains(t, timings, backupjobs.JobDump)
	assert.Equal(t, "env", timings[backupjobs.JobDump].Source)
	assert.True(t, timingsEqual(timings, timings))
	assert.False(t, timingsEqual(timings, map[string]backupjobs.Timing{}))

	a.forceTiming(backupjobs.JobDump, backupjobs.Timing{Source: "test"})
	assert.Equal(t, "test", a.timing(backupjobs.JobDump).Source)
	_ = a.changeCh()
}
