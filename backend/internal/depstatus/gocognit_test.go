package depstatus

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_RefreshAtMost15(t *testing.T) {
	testsupport.AssertCognitAtMost(t, testsupport.CognitScores(t), "(*Checker).refresh", 15)
}
