package roleperm

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_ResolveAtMost15(t *testing.T) {
	testsupport.AssertCognitAtMost(t, testsupport.CognitScores(t), "Resolve", 15)
}
