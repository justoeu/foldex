package backupjobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// INV-173: compiled floors, not the env, keep a row from lowering protection.
// The handler returns these messages verbatim; extracting ValidateJobConfig
// must not change a refusal or an acceptance.
func TestS3776_INV173_ValidateJobConfigFloorsHold(t *testing.T) {
	cases := []struct {
		name string
		job  string
		cfg  JobConfig
		ok   bool
		msg  string
	}{
		{name: "dump cannot be switched off", job: JobDump, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: allDays(), Enabled: boolPtr(true)}, msg: "dump cannot be switched off"},
		{name: "drill cannot be switched off", job: JobDrill, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"sun"}, Enabled: boolPtr(false)}, msg: "drill cannot be switched off"},
		{name: "mirror cannot be switched off", job: JobMirror, cfg: JobConfig{Mode: "interval", IntervalMin: 60, Enabled: boolPtr(false)}, msg: "mirror cannot be switched off"},
		{name: "dump on four weekdays", job: JobDump, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"mon", "tue", "wed", "thu"}}, msg: "dump needs at least 5 weekdays, got 4"},
		{name: "dump on five weekdays", job: JobDump, cfg: JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"mon", "tue", "wed", "thu", "fri"}}, ok: true},
		{name: "user_zip may disable without an agenda", job: JobUserZip, cfg: JobConfig{Mode: "times", Enabled: boolPtr(false)}, ok: true},
		{name: "user_zip disabled carrying an agenda", job: JobUserZip, cfg: JobConfig{Mode: "times", Enabled: boolPtr(false), Times: []string{"02:30"}, Weekdays: allDays()}, msg: "a disabled user_zip carries no agenda"},
		{name: "interval under the floor", job: JobDrill, cfg: JobConfig{Mode: "interval", IntervalMin: MinIntervalMin - 1}, msg: "drill interval must be between 15 and 1440 minutes"},
		{name: "interval at the floor", job: JobDrill, cfg: JobConfig{Mode: "interval", IntervalMin: MinIntervalMin}, ok: true},
		{name: "unknown job", job: "vacuum", cfg: JobConfig{Mode: "interval", IntervalMin: 60}, msg: `unknown job "vacuum"`},
		{name: "legacy time refused", job: JobUserZip, cfg: JobConfig{Mode: "times", Time: "02:30", Weekdays: allDays()}, msg: "are the previous schedule vocabulary and are read-only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateJobConfig(tc.job, tc.cfg)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.msg)
		})
	}
}

func TestS3776_INV173_RefusalsNameTheRealNumbers(t *testing.T) {
	err := ValidateJobConfig(JobDump, JobConfig{Mode: "times", Times: []string{"03:30"}, Weekdays: []string{"mon", "tue"}})
	require.EqualError(t, err, "dump needs at least 5 weekdays, got 2")

	err = ValidateJobConfig(JobMirror, JobConfig{Mode: "interval", IntervalMin: 5})
	require.EqualError(t, err, "mirror interval must be between 15 and 1440 minutes — a row tunes the cadence, it cannot switch the job off")
}
