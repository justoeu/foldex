package preview

import "testing"

func TestS3776_ProcessFetchParseHeadAtMost15(t *testing.T) {
	scores := cognitScores(t)
	assertCognitAtMost(t, scores, "(*Worker).process", 15)
	assertCognitAtMost(t, scores, "(*Fetcher).Fetch", 15)
	assertCognitAtMost(t, scores, "parseHead", 15)
}
