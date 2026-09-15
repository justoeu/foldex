package mailoutbox

import "testing"

func TestS3776_DrainAndPingAtMost15(t *testing.T) {
	scores := cognitScores(t)
	assertCognitAtMost(t, scores, "(*Relay).drain", 15)
	assertCognitAtMost(t, scores, "Ping", 15)
}
