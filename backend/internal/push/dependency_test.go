package push

import (
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSiblingFeaturesDoNotProvideSharedInfrastructure(t *testing.T) {
	packages, err := parser.ParseDir(token.NewFileSet(), ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.ImportsOnly)
	require.NoError(t, err)

	for _, pkg := range packages {
		for filename, file := range pkg.Files {
			for _, spec := range file.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				require.NoError(t, err)
				assert.NotEqual(t, "foldex/internal/preview", path, "%s imports infrastructure from a sibling feature", filename)
			}
		}
	}
}
