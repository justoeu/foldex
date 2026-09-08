package auth

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPayloadBuildersThreadRequestContext(t *testing.T) {
	var authenticated, pending *ast.FuncDecl
	err := walkProductionFuncs(func(path string, fn *ast.FuncDecl) {
		switch funcName(fn) {
		case "Handler.authenticatedPayload":
			authenticated = fn
		case "Handler.pendingPayload":
			pending = fn
		}
	})
	require.NoError(t, err)
	require.NotNil(t, authenticated, "authenticatedPayload must exist")
	require.NotNil(t, pending, "pendingPayload must exist")

	authParams := flattenParamTypes(authenticated.Type.Params)
	pendParams := flattenParamTypes(pending.Type.Params)
	require.GreaterOrEqual(t, len(authParams), 1)
	require.GreaterOrEqual(t, len(pendParams), 1)
	require.Equal(t, "context.Context", typeName(authParams[0]),
		"authenticatedPayload must take context.Context first so liveFeatures sees the request")
	require.Equal(t, "context.Context", typeName(pendParams[0]),
		"pendingPayload must take context.Context first so liveFeatures sees the request")
	require.False(t, hasCallNamed(authenticated.Body, "Background"),
		"authenticatedPayload must not hard-code context.Background")
	require.False(t, hasCallNamed(pending.Body, "Background"),
		"pendingPayload must not hard-code context.Background")
}
