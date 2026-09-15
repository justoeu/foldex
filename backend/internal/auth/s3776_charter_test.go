package auth

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Characterization of the 18 in-scope auth functions before the S3776 extracts.
// Each pin is a contract the extracts must keep: HTTP status, SQL shape, and
// the INV-003/004/040/041/014/044/180 control flow. Production code is unchanged.

func TestS3776CharterLoginFailuresAreByteIdentical(t *testing.T) {
	login := mustFindMethod(t, "Handler", "Login")
	require.True(t, hasCallNamed(login.Body, "burnDummyHash"),
		"unknown e-mail must pay bcrypt or timing enumerates accounts")
	require.True(t, hasCallNamed(login.Body, "recordLoginFailure"),
		"unknown, wrong password and disabled must share one failure branch (INV-041)")
	require.True(t, hasCallNamed(login.Body, "errInvalidCredentials"),
		"the shared failure branch must write the same envelope")
	require.True(t, containsIdent(login.Body, "StatusActive"),
		"a pending/disabled account must fail like a wrong password")

	var recordCallers []string
	err := walkProductionFuncs(func(_ string, fn *ast.FuncDecl) {
		if fn.Body != nil && hasCallNamed(fn.Body, "recordLoginFailure") {
			recordCallers = append(recordCallers, funcName(fn))
		}
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"Handler.Login"}, recordCallers,
		"a second call site is how unknown/wrong/disabled become distinguishable")
}

func TestS3776CharterTOTPSpendIsConditionalUpdate(t *testing.T) {
	fn := mustFindFunc(t, "consumeTOTPProofIfCurrentTx")
	require.True(t, containsLit(fn.Body, "UPDATE totp_secret SET last_used_counter"),
		"the replay guard must be an UPDATE, not a Go comparison (INV-003)")
	require.True(t, containsLit(fn.Body, "last_used_counter IS NULL OR last_used_counter < $2"),
		"a spent counter must refuse the same step by the WHERE clause")

	var leaked []string
	err := walkProductionFuncs(func(path string, fn *ast.FuncDecl) {
		switch funcName(fn) {
		case "Repository.ConsumeTOTPProof", "Repository.ConsumeRecoveryCode", "Repository.ConsumeEmailOTP":
			leaked = append(leaked, path+":"+funcName(fn))
		}
	})
	require.NoError(t, err)
	assert.Empty(t, leaked, "standalone spend wrappers split the write from the authorizing tx")
}

func TestS3776CharterAuditDaysIncludeTheEmptyOnes(t *testing.T) {
	fn := mustFindMethod(t, "Repository", "AuditStatsSince")
	require.True(t, containsLit(fn.Body, "generate_series"),
		"empty days must be built in SQL, not dropped by GROUP BY (INV-180)")
	require.True(t, containsLit(fn.Body, "date_trunc('day', now())"),
		"the series upper bound must be the database clock, not the process calendar")
	require.True(t, containsLit(fn.Body, "date_trunc('day', $1::timestamptz)"),
		"the series start must stay timestamptz so a UTC-3 host does not shift the last bucket")
}

func TestS3776CharterRefreshReplayKillsTheFamily(t *testing.T) {
	once := mustFindMethod(t, "Repository", "rotateOnce")
	require.True(t, containsIdent(once.Body, "Serializable"),
		"rotation must run in one SERIALIZABLE transaction (INV-040)")
	require.True(t, hasCallNamed(once.Body, "handleConsumed"),
		"a consumed token must not fall through to a live rotate")

	consumed := mustFindMethod(t, "Repository", "handleConsumed")
	require.True(t, hasCallNamed(consumed.Body, "revokeAndPurgeFamily") ||
		hasCallNamed(consumed.Body, "replayOutsideGraceTx") ||
		hasCallNamed(consumed.Body, "graceSiblingTx"),
		"replay outside grace must kill the family, not the presented token")

	var reuse, purge bool
	err := walkProductionFuncs(func(_ string, fn *ast.FuncDecl) {
		switch funcName(fn) {
		case "Repository.handleConsumed", "Repository.replayOutsideGraceTx", "Repository.graceSiblingTx", "revokeAndPurgeFamily":
			if hasCallNamed(fn.Body, "revokeAndPurgeFamily") || fn.Name.Name == "revokeAndPurgeFamily" {
				purge = true
			}
			if containsIdent(fn.Body, "ErrSessionReuse") {
				reuse = true
			}
		}
	})
	require.NoError(t, err)
	require.True(t, purge, "replay outside grace must kill the family, not the presented token")
	require.True(t, reuse, "family kill must surface as reuse, not a generic invalid session")
}

func TestS3776CharterNumericOTPIsSeparatedByStrippedLength(t *testing.T) {
	step := mustFindMethod(t, "Handler", "tryStepUpProof")
	require.True(t, hasCallNamed(step.Body, "numericOTP"),
		"six-digit vs recovery must be separator-stripped length, not digit count (INV-004)")
	require.True(t, containsIdent(step.Body, "recoveryCodeChars"),
		"a recovery code is sixteen symbols once hyphens are gone")

	proof := mustFindMethod(t, "Handler", "challengeProof")
	require.True(t, hasCallNamed(proof.Body, "numericOTP"),
		"login 2FA must use the same length split as step-up")
}

func TestS3776CharterEmailChangeSpendsAndMovesInOneStatement(t *testing.T) {
	fn := mustFindMethod(t, "Repository", "ConsumeEmailChange")
	require.True(t, containsLit(fn.Body, "WITH spent AS"),
		"spending the token and moving the address are one statement (INV-014)")
	require.True(t, containsLit(fn.Body, "email_verified_at = now()"),
		"the new address arrives verified — the click is the proof")
	require.True(t, containsIdent(fn.Body, "ReasonEmailChanged"),
		"every session, current included, must die with the identifier change")
}

func TestS3776CharterLastAdminCannotBeRemoved(t *testing.T) {
	update := mustFindMethod(t, "Repository", "UpdateUser")
	require.True(t, hasCallNamed(update.Body, "guardLastAdminTx"),
		"demote/disable of the last admin must refuse in the repository (INV-044)")
	require.True(t, containsIdent(update.Body, "ErrOwnerImmutable"),
		"the owner seat is out of reach of ordinary edits")
}

func mustFindFunc(t *testing.T, name string) *ast.FuncDecl {
	t.Helper()
	var found *ast.FuncDecl
	err := walkProductionFuncs(func(_ string, fn *ast.FuncDecl) {
		if fn.Recv == nil && fn.Name.Name == name {
			found = fn
		}
	})
	require.NoError(t, err)
	require.NotNil(t, found, "%s not found", name)
	return found
}

func containsIdent(body *ast.BlockStmt, name string) bool {
	var hit bool
	ast.Inspect(body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && id.Name == name {
			hit = true
			return false
		}
		return true
	})
	return hit
}
