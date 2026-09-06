package auth

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCredentialStepUpLockIsShared is DUP-ECH-005: DisableTOTP and
// RegenerateRecoveryCodes independently locked the user, verified the
// password and spent the second-factor proof. A third bound added to one
// writer would miss the other. The scan looks at the lock SQL plus bcrypt
// plus proof consumption, not the helper's name, because renaming is how
// a second copy would hide.
func TestCredentialStepUpLockIsShared(t *testing.T) {
	var owners []string
	err := walkProductionFuncs(func(path string, fn *ast.FuncDecl) {
		if !ownsCredentialStepUpLock(fn) {
			return
		}
		owners = append(owners, path+":"+funcName(fn))
	})
	require.NoError(t, err)
	require.Len(t, owners, 1,
		"password+proof lock must have one owner, found %v", owners)
}

func ownsCredentialStepUpLock(fn *ast.FuncDecl) bool {
	if fn.Body == nil {
		return false
	}
	return containsLit(fn.Body, "password_hash, token_version, status") &&
		containsLit(fn.Body, "FOR NO KEY UPDATE") &&
		hasCallNamed(fn.Body, "Verify") &&
		hasCallNamed(fn.Body, "consumeSecondFactorTx")
}
