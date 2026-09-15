package tags

import "testing"

func TestS3776_SetEntityTagsWithPendingAtMost15(t *testing.T) {
	assertCognitAtMost(t, cognitScores(t), "SetEntityTagsWithPending", 15)
}
