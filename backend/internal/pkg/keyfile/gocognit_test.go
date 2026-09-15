package keyfile

import "testing"

func TestS3776_LoadAtMost15(t *testing.T) {
	assertCognitAtMost(t, cognitScores(t), "Load", 15)
}
