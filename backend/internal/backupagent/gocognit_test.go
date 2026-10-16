package backupagent

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/testsupport"
)

func TestCognitiveComplexity_JobRuns(t *testing.T) {
	cases := []struct {
		file string
		name string
	}{
		{file: "drill.go", name: "(*DrillJob).Run"},
		{file: "dump.go", name: "(*DumpJob).Run"},
		{file: "userzip.go", name: "(*UserZipJob).Run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testsupport.CognitOf(t, tc.file, tc.name)
			assert.LessOrEqual(t, got, 15,
				"%s cognitive=%d — extract early returns / unexported helpers, no NOSONAR", tc.name, got)
		})
	}
}

func TestCognitiveComplexity_CopyDelta(t *testing.T) {
	got := testsupport.CognitOf(t, "mirror.go", "(*MirrorJob).copyDelta")
	assert.LessOrEqual(t, got, 15,
		"copyDelta cognitive=%d — extract early returns / unexported helpers, no NOSONAR", got)
}

func TestCognitiveComplexity_ScheduleExecuteTimings(t *testing.T) {
	cases := []struct {
		file string
		name string
	}{
		{file: "agent.go", name: "(*Agent).scheduleLoop"},
		{file: "agent.go", name: "(*Agent).execute"},
		{file: "agent.go", name: "(*Agent).computeTimings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testsupport.CognitOf(t, tc.file, tc.name)
			assert.LessOrEqual(t, got, 15,
				"%s cognitive=%d — extract early returns / unexported helpers, no NOSONAR", tc.name, got)
		})
	}
}

func TestCognitiveComplexity_GFSPolicyKeep(t *testing.T) {
	got := testsupport.CognitOf(t, "retention.go", "(GFSPolicy).keep")
	assert.LessOrEqual(t, got, 15,
		"GFSPolicy.keep cognitive=%d — extract early returns / unexported helpers, no NOSONAR", got)
}
