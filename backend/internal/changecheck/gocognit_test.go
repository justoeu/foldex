package changecheck

import "testing"

func TestS3776_ExtractAndPushLoopAtMost15(t *testing.T) {
	scores := cognitScores(t)
	assertCognitAtMost(t, scores, "extractMainContent", 15)
	assertCognitAtMost(t, scores, "extractFeedURL", 15)
	assertCognitAtMost(t, scores, "(*Worker).pushLoop", 15)
}
