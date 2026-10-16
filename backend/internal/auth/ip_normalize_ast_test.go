package auth

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTwinIPNormalizeAliasesAreGone(t *testing.T) {
	err := walkProductionFuncs(func(path string, fn *ast.FuncDecl) {
		switch fn.Name.Name {
		case "normalizeAuditIP", "NormalizeIP":
			t.Errorf("%s still defines %s; call ipblock.Normalize", path, fn.Name.Name)
		}
	})
	require.NoError(t, err)
}
