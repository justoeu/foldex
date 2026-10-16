package push

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_LoadOrGenerateAtMost15(t *testing.T) {
	testsupport.AssertCognitAtMost(t, testsupport.CognitScores(t), "LoadOrGenerate", 15)
}
