package mailer

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_SendLoadAssetsRenderAtMost15(t *testing.T) {
	scores := testsupport.CognitScores(t)
	testsupport.AssertCognitAtMost(t, scores, "(*smtpMailer).Send", 15)
	testsupport.AssertCognitAtMost(t, scores, "loadAssets", 15)
	testsupport.AssertCognitAtMost(t, scores, "(*assets).render", 15)
}
