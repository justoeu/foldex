package policy

import "testing"

func TestS3776_ValidateAtMost15(t *testing.T) {
	assertCognitAtMost(t, cognitScores(t), "(Policy).Validate", 15)
}
