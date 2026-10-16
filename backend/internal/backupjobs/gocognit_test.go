package backupjobs

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/testsupport"
)

func TestCognitiveComplexity_ValidateJobConfig(t *testing.T) {
	got := testsupport.CognitOf(t, "schedule.go", "ValidateJobConfig")
	assert.LessOrEqual(t, got, 15,
		"ValidateJobConfig cognitive=%d — extract early returns / unexported helpers, no NOSONAR", got)
}
