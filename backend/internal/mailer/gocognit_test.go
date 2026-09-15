package mailer

import "testing"

func TestS3776_SendLoadAssetsRenderAtMost15(t *testing.T) {
	scores := cognitScores(t)
	assertCognitAtMost(t, scores, "(*smtpMailer).Send", 15)
	assertCognitAtMost(t, scores, "loadAssets", 15)
	assertCognitAtMost(t, scores, "(*assets).render", 15)
}
