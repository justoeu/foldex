package backupstatus

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/backupagent"
)

func boolPtr(b bool) *bool { return &b }

// The trail has to answer "what was the agenda during the incident". One PUT
// now moves the mode, the weekday set and every wall time at once, so the
// event name alone says nothing — the detail carries both documents (INV-047).
func TestScheduleAudit_CarriesBothDocuments(t *testing.T) {
	before := renderSchedule(backupagent.JobConfig{
		Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}})
	after := renderSchedule(backupagent.JobConfig{
		Mode: "times", Times: []string{"06:00", "18:00"}, Weekdays: []string{"mon", "tue", "wed", "thu", "fri"}})

	detail := scheduleAudit("dump", scheduleActionSet, before, after)
	assert.Contains(t, detail, "dump schedule set")
	assert.Contains(t, detail, `"times":["03:30"]`)
	assert.Contains(t, detail, `"times":["06:00","18:00"]`)
	assert.Contains(t, detail, `"weekdays":["mon","tue","wed","thu","fri"]`)
	assert.Less(t, len(before), len(detail), "the previous document is part of the record, not a summary of it")
}

func TestRenderSchedule_KeepsEveryFieldThatDecidesWhenAJobRuns(t *testing.T) {
	assert.Equal(t, `{"mode":"interval","interval_min":360}`,
		renderSchedule(backupagent.JobConfig{Mode: "interval", IntervalMin: 360}))
	assert.Equal(t, `{"enabled":false,"mode":"times"}`,
		renderSchedule(backupagent.JobConfig{Mode: "times", Enabled: boolPtr(false)}),
		"\"switched off\" is the whole content of that edit — dropping it would record a no-op")
}

// "there was no row" and "the read failed" are different facts to whoever
// reads the trail later, so they are different words.
func TestScheduleAudit_NamesTheAbsenceOfARowAndTheFailureToReadOne(t *testing.T) {
	reset := scheduleAudit("user_zip", scheduleActionReset,
		renderSchedule(backupagent.JobConfig{Mode: "times", Enabled: boolPtr(false)}), scheduleBaseline)
	assert.Contains(t, reset, "user_zip schedule reset to the env baseline")
	assert.Contains(t, reset, "env baseline")

	assert.Contains(t, scheduleAudit("dump", scheduleActionSet, scheduleUnknown, "{}"), scheduleUnknown)
	assert.NotEqual(t, scheduleBaseline, scheduleUnknown)
}
