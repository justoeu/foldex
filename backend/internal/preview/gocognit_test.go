package preview

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_ProcessFetchParseHeadAtMost15(t *testing.T) {
	scores := testsupport.CognitScores(t)
	testsupport.AssertCognitAtMost(t, scores, "(*Worker).process", 15)
	testsupport.AssertCognitAtMost(t, scores, "(*Fetcher).Fetch", 15)
	testsupport.AssertCognitAtMost(t, scores, "parseHead", 15)
}
