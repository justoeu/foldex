package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCognitiveComplexityAtMost15(t *testing.T) {
	cases := []struct{ recv, name string }{
		{"Repository", "CompleteEmailFactorEnrollment"},
		{"Handler", "ConfirmEmailFactor"},
		{"Handler", "tryStepUpProof"},
		{"Handler", "ConfirmTOTP"},
		{"Repository", "CompleteTOTPEnrollment"},
		{"Repository", "CreateChallengeEmailOTP"},
		{"Repository", "CreateChallenge"},
		{"Repository", "UpdateUser"},
		{"Repository", "SetPassword"},
		{"Repository", "acceptInvite"},
		{"Repository", "rotateOnce"},
		{"Repository", "handleConsumed"},
		{"Repository", "AuditStatsSince"},
		{"AdminHandler", "ExportAudit"},
		{"Repository", "ConvertToProvider"},
		{"Repository", "ConsumeEmailChange"},
		{"AdminHandler", "UpdateUser"},
	}
	for _, tc := range cases {
		t.Run(tc.recv+"."+tc.name, func(t *testing.T) {
			fn := mustFindMethod(t, tc.recv, tc.name)
			got := Complexity(fn)
			assert.LessOrEqual(t, got, 15,
				"%s.%s cognitive=%d — extract unexported helpers until ≤15", tc.recv, tc.name, got)
		})
	}
}
