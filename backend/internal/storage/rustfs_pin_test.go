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
		"rustfs must stay on a digest-pinned RC, not the floating main tag")
	require.Contains(t, image, "@sha256:", "rustfs image must be digest-pinned")
	require.True(t, strings.HasPrefix(image, "rustfs/rustfs:1.0.0-rc.2@sha256:"),
		"unexpected rustfs pin %q — bump compose + STACK.md + storage tests together", image)

	stack, err := os.ReadFile(filepath.Join(root, "docs/STACK.md"))
	require.NoError(t, err)
	digest := image[strings.Index(image, "sha256:"):]
	require.Contains(t, string(stack), digest,
		"docs/STACK.md must record the same rustfs digest compose pins")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
