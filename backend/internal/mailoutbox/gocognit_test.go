package mailoutbox

import (
	"testing"

	"foldex/internal/testsupport"
)

func TestS3776_DrainAndPingAtMost15(t *testing.T) {
	scores := testsupport.CognitScores(t)
	testsupport.AssertCognitAtMost(t, scores, "(*Relay).drain", 15)
	testsupport.AssertCognitAtMost(t, scores, "Ping", 15)
}
