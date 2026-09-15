package importer

import "testing"

func TestS3776_ValidateLinksAndParseUploadAtMost15(t *testing.T) {
	scores := cognitScores(t)
	assertCognitAtMost(t, scores, "(JSONFile).validateLinks", 15)
	assertCognitAtMost(t, scores, "(*Handler).parseUpload", 15)
}
