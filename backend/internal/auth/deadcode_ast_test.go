package auth

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecondFactorSpendIsOnlyReachableInsideTheAuthorizingTransaction(t *testing.T) {
	forbidden := map[string]struct{}{
		"Repository.ConsumeTOTPProof":    {},
		"Repository.ConsumeRecoveryCode": {},
		"Repository.ConsumeEmailOTP":     {},
	}
	required := map[string]struct{}{
		"Repository.Complete2FA":         {},
		"Repository.ConsumeSecondFactor": {},
		"consumeTOTPProofTx":             {},
		"consumeSingleUseTx":             {},
		"consumeSecondFactorTx":          {},
		"consumeChallengeProofTx":        {},
	}

	var leaked []string
	seen := map[string]bool{}
	err := walkProductionFuncs(func(path string, fn *ast.FuncDecl) {
		name := funcName(fn)
		seen[name] = true
		if _, bad := forbidden[name]; bad {
			leaked = append(leaked, path+":"+name)
		}
	})
	require.NoError(t, err)
	require.Empty(t, leaked,
		"standalone second-factor spend wrappers split the write from the authorizing transaction (INV-003/028): %v",
		leaked)
	for name := range required {
		require.True(t, seen[name], "live spend path %s must remain", name)
	}
}
