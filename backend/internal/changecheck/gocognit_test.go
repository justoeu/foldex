package changecheck

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_ExtractAndPushLoopAtMost15(t *testing.T) {
	scores := testsupport.CognitScores(t)
	testsupport.AssertCognitAtMost(t, scores, "extractMainContent", 15)
	testsupport.AssertCognitAtMost(t, scores, "extractFeedURL", 15)
	testsupport.AssertCognitAtMost(t, scores, "(*Worker).pushLoop", 15)
}
