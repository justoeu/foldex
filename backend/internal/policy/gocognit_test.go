package policy

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_ValidateAtMost15(t *testing.T) {
	testsupport.AssertCognitAtMost(t, testsupport.CognitScores(t), "(Policy).Validate", 15)
}
