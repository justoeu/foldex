package storage

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var rustfsImageLine = regexp.MustCompile(`(?m)^\s*image:\s*(rustfs/rustfs:\S+)`)

func TestRustfsImageIsDigestPinned(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	body, err := os.ReadFile(filepath.Join(root, "docker-compose.services.yml"))
	require.NoError(t, err)
	m := rustfsImageLine.FindSubmatch(body)
	require.NotNil(t, m, "docker-compose.services.yml must pin rustfs/rustfs")
	image := string(m[1])

	require.NotContains(t, image, "main-ubuntu22.04",
		"rustfs must stay on a digest-pinned release, not the floating main tag")
	require.Contains(t, image, "@sha256:", "rustfs image must be digest-pinned")
	require.True(t, strings.HasPrefix(image, "rustfs/rustfs:1.0.3@sha256:"),
		"unexpected rustfs pin %q — bump compose + STACK.md + storage tests together", image)

	stack, err := os.ReadFile(filepath.Join(root, "docs/STACK.md"))
	require.NoError(t, err)
	digest := image[strings.Index(image, "sha256:"):]
	require.Contains(t, string(stack), digest,
		"docs/STACK.md must record the same rustfs digest compose pins")

	arch, err := os.ReadFile(filepath.Join(root, "docs/ARCHITECTURE.md"))
	require.NoError(t, err)
	version := image[strings.Index(image, ":")+1 : strings.Index(image, "@")]
	require.Contains(t, string(arch), "RustFS "+version,
		"docs/ARCHITECTURE.md object-store row must name the same rustfs version compose pins")

	// The integration consts live behind `//go:build integration`, so a partial
	// bump (compose moved, consts forgotten) fails no unit test and the suite
	// keeps testing a stale — possibly vulnerable — image. The security guard
	// AST-parses source elsewhere in this repo; a regex over the two const
	// lines is the same discipline at the weight this needs.
	for _, name := range []string{
		"rustfs_testhelper_integration_test.go",
		"client_error_integration_test.go",
	} {
		body, err := os.ReadFile(filepath.Join(root, "backend", "internal", "storage", name))
		require.NoError(t, err)
		m := regexp.MustCompile(`const rustfsImage = "(\S+)"`).FindSubmatch(body)
		require.NotNil(t, m, "%s must define the rustfsImage pin", name)
		require.Equal(t, image, string(m[1]),
			"%s pins a different rustfs image than docker-compose.services.yml — bump them together", name)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
