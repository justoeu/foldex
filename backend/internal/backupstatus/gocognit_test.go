package backupstatus

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/testsupport"
)

func TestCognitiveComplexity_Download(t *testing.T) {
	got := testsupport.CognitOf(t, "download_handler.go", "(*Handler).Download")
	assert.LessOrEqual(t, got, 15,
		"Download cognitive=%d — extract early returns / unexported helpers, no NOSONAR", got)
}
