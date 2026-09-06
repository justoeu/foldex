package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMadminGoBootstrapCoupling(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.ImportsOnly)
	require.NoError(t, err)

	found := false
	for _, spec := range file.Imports {
		if spec.Path.Value == `"github.com/minio/madmin-go/v3"` {
			found = true
			break
		}
	}
	require.True(t, found,
		"rustfs-bootstrap must keep importing madmin-go — do not replace it with ad-hoc HTTP; "+
			"run the bootstrap path against the digest-pinned rustfs image before bumping either side")

	root := repoRoot(t)
	mod, err := os.ReadFile(filepath.Join(root, "backend/go.mod"))
	require.NoError(t, err)
	ver := regexp.MustCompile(`github.com/minio/madmin-go/v3\s+(v[\d.]+)`).FindSubmatch(mod)
	require.NotNil(t, ver, "go.mod must require madmin-go")

	stack, err := os.ReadFile(filepath.Join(root, "docs/STACK.md"))
	require.NoError(t, err)
	require.Contains(t, string(stack), string(ver[1]),
		"docs/STACK.md must pair the madmin-go version with the rustfs digest")
	require.Contains(t, string(stack), "sha256:7d6d361c49c08d427250fb59aae5d78df83d644c3405d9ccf4b21cda0b0692d0",
		"docs/STACK.md must name the rustfs digest this madmin-go pin was verified against")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
