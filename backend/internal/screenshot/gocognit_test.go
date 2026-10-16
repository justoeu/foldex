package screenshot

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_AcquireBrowserAndCloseAtMost15(t *testing.T) {
	scores := testsupport.CognitScores(t)
	testsupport.AssertCognitAtMost(t, scores, "(*Pool).acquireBrowser", 15)
	testsupport.AssertCognitAtMost(t, scores, "(*Pool).Close", 15)
}
