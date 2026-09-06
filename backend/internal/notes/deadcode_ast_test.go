package notes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoteImageKeysUseCanonicalHelper(t *testing.T) {
	require.NotContains(t, productionFuncNames(t), "extractImageKeys",
		"extractImageKeys is a pass-through; note image-key tests must hit notemedia.Keys")
}

func TestSlugUpdateIsNotALocalTrampoline(t *testing.T) {
	require.NotContains(t, productionFuncNames(t), "resolveUpdateSlug",
		"resolveUpdateSlug only forwards to slug.ResolveUpdate; call that helper at the Update site")
}

func TestNoteTagAttachmentUsesCanonicalHelper(t *testing.T) {
	require.NotContains(t, productionFuncNames(t), "setNoteTags",
		"setNoteTags only binds the kind string; call tags.SetEntityTagsWithPending directly")
}

func productionFuncNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err, name)
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			names = append(names, fn.Name.Name)
			return true
		})
	}
	return names
}
