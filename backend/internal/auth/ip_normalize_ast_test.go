package auth

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTwinIPNormalizeAliasesAreGone(t *testing.T) {
	err := walkProductionFuncs(func(path string, fn *ast.FuncDecl) {
		switch fn.Name.Name {
		case "normalizeAuditIP":
			t.Errorf("%s still defines normalizeAuditIP; call ipblock.Normalize", path)
		case "NormalizeIP":
			if path != "ipblock_export.go" {
				t.Errorf("%s still defines NormalizeIP; call ipblock.Normalize", path)
			}
		}
	})
	require.NoError(t, err)
}
