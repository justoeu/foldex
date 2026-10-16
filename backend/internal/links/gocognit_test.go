package links

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_CaptureAndStoreAtMost15(t *testing.T) {
	testsupport.AssertCognitAtMost(t, testsupport.CognitScores(t), "(*ScreenshotHandler).CaptureAndStore", 15)
}
