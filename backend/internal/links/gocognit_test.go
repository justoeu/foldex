package links

import "testing"

func TestS3776_CaptureAndStoreAtMost15(t *testing.T) {
	assertCognitAtMost(t, cognitScores(t), "(*ScreenshotHandler).CaptureAndStore", 15)
}
