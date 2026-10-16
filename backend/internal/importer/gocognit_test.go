package importer

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_ValidateLinksAndParseUploadAtMost15(t *testing.T) {
	scores := testsupport.CognitScores(t)
	testsupport.AssertCognitAtMost(t, scores, "(JSONFile).validateLinks", 15)
	testsupport.AssertCognitAtMost(t, scores, "(*Handler).parseUpload", 15)
}
