package tags

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_SetEntityTagsWithPendingAtMost15(t *testing.T) {
	testsupport.AssertCognitAtMost(t, testsupport.CognitScores(t), "SetEntityTagsWithPending", 15)
}
