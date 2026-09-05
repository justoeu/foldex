package auth

import (
	"context"
	"go/ast"
	"testing"

	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

// TestAdminCreateUserTakesNamedInput is BP-MEN-004: AdminCreateUser used to
// take three adjacent strings (email, name, password), so a caller could
// compile a password into the name slot. Named fields refuse that swap.
func TestAdminCreateUserTakesNamedInput(t *testing.T) {
	fn := mustFindMethod(t, "Repository", "AdminCreateUser")
	params := flattenParamTypes(fn.Type.Params)
	require.GreaterOrEqual(t, len(params), 2, "AdminCreateUser(%v)", typeNames(params))
	require.Equal(t, "context.Context", typeName(params[0]))
	require.LessOrEqual(t, len(params), 2,
		"AdminCreateUser must take named input, not a positional clump: %v", typeNames(params))
	require.Equal(t, 0, maxAdjacentStrings(params),
		"AdminCreateUser must not take adjacent strings: %v", typeNames(params))
	require.Equal(t, "NewUser", typeName(params[1]),
		"AdminCreateUser must take NewUser, got %v", typeNames(params))

	var _ interface {
		AdminCreateUser(context.Context, NewUser) (User, error)
	} = (*Repository)(nil)
	_ = NewUser{Email: "a@b.c", Name: "n", Password: "p", Role: authctx.RoleEditor}
}

// TestEnrollmentCompleteTakesNamedInput is BP-MEN-003: TOTP and e-mail
// enrollment complete used to take a duplicated positional session clump
// (sessionID, challenge, ttl, ip, ua). Named fields so IP cannot compile
// into UA's slot, and both factors share the same input.
func TestEnrollmentCompleteTakesNamedInput(t *testing.T) {
	totp := mustFindMethod(t, "Repository", "CompleteTOTPEnrollment")
	email := mustFindMethod(t, "Repository", "CompleteEmailFactorEnrollment")

	totpParams := flattenParamTypes(totp.Type.Params)
	emailParams := flattenParamTypes(email.Type.Params)
	require.GreaterOrEqual(t, len(totpParams), 2)
	require.GreaterOrEqual(t, len(emailParams), 2)

	require.LessOrEqual(t, len(totpParams), 3,
		"CompleteTOTPEnrollment must take named input, not a positional clump: %v", typeNames(totpParams))
	require.LessOrEqual(t, len(emailParams), 3,
		"CompleteEmailFactorEnrollment must take named input, not a positional clump: %v", typeNames(emailParams))
	require.Equal(t, 0, maxAdjacentStrings(totpParams),
		"CompleteTOTPEnrollment must not take adjacent strings: %v", typeNames(totpParams))
	require.Equal(t, 0, maxAdjacentStrings(emailParams),
		"CompleteEmailFactorEnrollment must not take adjacent strings: %v", typeNames(emailParams))

	totpIn := sharedStructParam(totpParams)
	emailIn := sharedStructParam(emailParams)
	require.NotEmpty(t, totpIn, "CompleteTOTPEnrollment has no named struct param: %v", typeNames(totpParams))
	require.Equal(t, totpIn, emailIn,
		"TOTP and e-mail enrollment must share one named input, got %q vs %q", totpIn, emailIn)
	require.Equal(t, "EnrollmentComplete", totpIn)

	var _ interface {
		CompleteTOTPEnrollment(context.Context, EnrollmentComplete, TOTPProof) (User, issuedTokens, error)
		CompleteEmailFactorEnrollment(context.Context, EnrollmentComplete, []byte) (User, issuedTokens, error)
	} = (*Repository)(nil)
	_ = EnrollmentComplete{UID: 1, TokenVersion: 0, Session: LiveSession{ID: 1}}
	_ = EnrollmentComplete{UID: 1, TokenVersion: 0, Session: PreAuth{IP: "1.1.1.1", UA: "ua"}}
}

func mustFindMethod(t *testing.T, recv, name string) *ast.FuncDecl {
	t.Helper()
	var found *ast.FuncDecl
	err := walkProductionFuncs(func(_ string, fn *ast.FuncDecl) {
		if fn.Name.Name != name {
			return
		}
		if fn.Recv == nil || len(fn.Recv.List) == 0 {
			return
		}
		if recvName(fn.Recv.List[0].Type) == recv {
			found = fn
		}
	})
	require.NoError(t, err)
	require.NotNil(t, found, "%s.%s not found", recv, name)
	return found
}

func typeNames(types []ast.Expr) []string {
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = typeName(t)
	}
	return out
}

func sharedStructParam(types []ast.Expr) string {
	for _, t := range types {
		name := typeName(t)
		switch name {
		case "", "string", "int", "int64", "bool", "[]byte", "[][]byte",
			"context.Context", "TOTPProof":
			continue
		}
		if len(name) > 0 && name[0] != '*' && name[0] != '[' {
			return name
		}
	}
	return ""
}
