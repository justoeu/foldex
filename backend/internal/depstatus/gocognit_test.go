package depstatus

import "testing"

func TestS3776_RefreshAtMost15(t *testing.T) {
	assertCognitAtMost(t, cognitScores(t), "(*Checker).refresh", 15)
}
