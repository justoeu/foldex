package screenshot

import "testing"

func TestS3776_AcquireBrowserAndCloseAtMost15(t *testing.T) {
	scores := cognitScores(t)
	assertCognitAtMost(t, scores, "(*Pool).acquireBrowser", 15)
	assertCognitAtMost(t, scores, "(*Pool).Close", 15)
}
