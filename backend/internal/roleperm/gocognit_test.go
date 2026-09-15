package roleperm

import "testing"

func TestS3776_ResolveAtMost15(t *testing.T) {
	assertCognitAtMost(t, cognitScores(t), "Resolve", 15)
}
