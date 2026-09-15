package push

import "testing"

func TestS3776_LoadOrGenerateAtMost15(t *testing.T) {
	assertCognitAtMost(t, cognitScores(t), "LoadOrGenerate", 15)
}
