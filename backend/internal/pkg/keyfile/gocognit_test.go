package keyfile

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_LoadAtMost15(t *testing.T) {
	testsupport.AssertCognitAtMost(t, testsupport.CognitScores(t), "Load", 15)
}
