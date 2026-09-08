package auth

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTryStepUpProofHasNoLimiterSideEffects(t *testing.T) {
	var tryFn *ast.FuncDecl
	var leak []string
	err := walkProductionFuncs(func(path string, fn *ast.FuncDecl) {
		name := funcName(fn)
		if name == "Handler.tryStepUpProof" {
			tryFn = fn
		}
		if name == "Handler.stepUpSecondFactor" || name == "Handler.tryStepUpProof" {
			return
		}
		if fn.Body != nil && hasCallNamed(fn.Body, "stepUpSecondFactor") && !hasDeferNamed(fn.Body, "settleStepUp") {
			leak = append(leak, name)
		}
	})
	require.NoError(t, err)
	require.NotNil(t, tryFn, "tryStepUpProof must exist")
	require.False(t, hasCallNamed(tryFn.Body, "Begin"), "tryStepUpProof must not touch the limiter")
	require.False(t, hasCallNamed(tryFn.Body, "writeRateLimited"), "tryStepUpProof must not write HTTP")
	require.False(t, hasCallNamed(tryFn.Body, "CommitFail"), "tryStepUpProof must not settle the limiter")
	require.False(t, hasCallNamed(tryFn.Body, "CommitSuccess"), "tryStepUpProof must not settle the limiter")
	require.False(t, hasCallNamed(tryFn.Body, "Release"), "tryStepUpProof must not settle the limiter")
	require.Empty(t, leak, "every stepUpSecondFactor caller must defer settleStepUp: %v", leak)
}

func hasDeferNamed(body *ast.BlockStmt, name string) bool {
	var hit bool
	ast.Inspect(body, func(n ast.Node) bool {
		d, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		ast.Inspect(d, func(n ast.Node) bool {
			switch f := n.(type) {
			case *ast.Ident:
				if f.Name == name {
					hit = true
				}
			case *ast.SelectorExpr:
				if f.Sel.Name == name {
					hit = true
				}
			}
			return true
		})
		return true
	})
	return hit
}
