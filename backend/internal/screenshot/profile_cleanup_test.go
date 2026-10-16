package screenshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipIfPermissionsBypassed(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can read and delete through missing permissions")
	}
}

func writeProfileFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("state"), 0o600))
}

func TestRemoveAllContext_MissingPathIsSuccess(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-existed")
	assert.NoError(t, removeAllContext(context.Background(), missing))
}

func TestRemoveAllContext_RegularFilePath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "profile-file")
	require.NoError(t, os.WriteFile(file, []byte("state"), 0o600))

	require.NoError(t, removeAllContext(context.Background(), file))
	_, err := os.Lstat(file)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestRemoveAllContext_SymlinkPathRemovesOnlyTheLink(t *testing.T) {
	target := t.TempDir()
	keep := filepath.Join(target, "keep")
	require.NoError(t, os.WriteFile(keep, []byte("state"), 0o600))
	link := filepath.Join(t.TempDir(), "profile")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	require.NoError(t, removeAllContext(context.Background(), link))
	_, err := os.Lstat(link)
	assert.True(t, errors.Is(err, os.ErrNotExist))
	content, err := os.ReadFile(keep)
	require.NoError(t, err)
	assert.Equal(t, []byte("state"), content)
}

func TestRemoveAllContext_PathBelowARegularFileErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(file, []byte("state"), 0o600))

	err := removeAllContext(context.Background(), filepath.Join(file, "child"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, os.ErrNotExist)
}

func TestRemoveAllContext_EmptyAndDeeplyNestedTrees(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty-profile")
	require.NoError(t, os.Mkdir(empty, 0o755))
	require.NoError(t, removeAllContext(context.Background(), empty))
	_, err := os.Lstat(empty)
	assert.True(t, errors.Is(err, os.ErrNotExist))

	profile := filepath.Join(t.TempDir(), "profile")
	for _, rel := range []string{
		"Default/Cache/blob",
		"Default/Code Cache/js/index",
		"deep/a/b/c/d/e/leaf",
		".hidden-file",
		"-dash-led",
		"nome com espaços e Ünïcode",
	} {
		writeProfileFile(t, filepath.Join(profile, rel))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(profile, "empty/sub"), 0o755))

	require.NoError(t, removeAllContext(context.Background(), profile))
	_, err = os.Lstat(profile)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestRemoveAllContext_MoreEntriesThanOneBatch(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, os.Mkdir(profile, 0o755))
	for i := range profileCleanupBatchSize + 37 {
		writeProfileFile(t, filepath.Join(profile, fmt.Sprintf("file-%03d", i)))
	}
	for i := range 5 {
		sub := filepath.Join(profile, fmt.Sprintf("dir-%d", i))
		for j := range profileCleanupBatchSize + 3 {
			writeProfileFile(t, filepath.Join(sub, fmt.Sprintf("nested-%03d", j)))
		}
	}

	require.NoError(t, removeAllContext(context.Background(), profile))
	_, err := os.Lstat(profile)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestRemoveAllContext_BrokenAndFileSymlinksInside(t *testing.T) {
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "keep")
	require.NoError(t, os.WriteFile(outsideFile, []byte("state"), 0o600))
	profile := filepath.Join(t.TempDir(), "profile")
	writeProfileFile(t, filepath.Join(profile, "nested", "real"))
	if err := os.Symlink(outsideFile, filepath.Join(profile, "nested", "file-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	require.NoError(t, os.Symlink("does-not-exist", filepath.Join(profile, "nested", "broken")))

	require.NoError(t, removeAllContext(context.Background(), profile))
	content, err := os.ReadFile(outsideFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("state"), content)
}

func TestRemoveAllContext_ReadOnlyFileAndFIFO(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "profile")
	writeProfileFile(t, filepath.Join(profile, "locked-cookie"))
	require.NoError(t, os.Chmod(filepath.Join(profile, "locked-cookie"), 0o400))
	fifo := filepath.Join(profile, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Logf("mkfifo unavailable, FIFO leaf not exercised: %v", err)
	}

	require.NoError(t, removeAllContext(context.Background(), profile))
	_, err := os.Lstat(profile)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestRemoveAllContext_UnreadableProfileDirErrors(t *testing.T) {
	skipIfPermissionsBypassed(t)
	profile := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, os.Mkdir(profile, 0o755))
	t.Cleanup(func() { _ = os.Chmod(profile, 0o755) })
	require.NoError(t, os.Chmod(profile, 0o000))

	assert.Error(t, removeAllContext(context.Background(), profile),
		"an unreadable profile cannot be opened as a root")
}

func TestRemoveAllContext_UnwritableSubdirSurfacesErrorAndKeepsTree(t *testing.T) {
	skipIfPermissionsBypassed(t)
	profile := filepath.Join(t.TempDir(), "profile")
	sub := filepath.Join(profile, "sealed")
	writeProfileFile(t, filepath.Join(sub, "cookie"))
	// Restorable before t.TempDir's own cleanup runs (cleanups run LIFO).
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	require.NoError(t, os.Chmod(sub, 0o500))

	err := removeAllContext(context.Background(), profile)
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(sub, "cookie"))
	assert.NoError(t, statErr, "a failed cleanup must leave the tree for a retry")
}

func TestReadRootDirBatch_ErrorsAndBatchLimit(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, filepath.Join(dir, "regular"))
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = readRootDirBatch(ctx, root, ".")
	assert.ErrorIs(t, err, context.Canceled)

	_, err = readRootDirBatch(context.Background(), root, "missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist)

	_, err = readRootDirBatch(context.Background(), root, "regular")
	require.Error(t, err, "reading a regular file as a directory must fail")

	for i := range profileCleanupBatchSize + 10 {
		writeProfileFile(t, filepath.Join(dir, fmt.Sprintf("batch-%03d", i)))
	}
	entries, err := readRootDirBatch(context.Background(), root, ".")
	require.NoError(t, err)
	assert.Len(t, entries, profileCleanupBatchSize)
}

func TestRemoveRootLeaf_MissingPresentAndNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, filepath.Join(dir, "leaf"))
	writeProfileFile(t, filepath.Join(dir, "nonempty", "child"))
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	assert.NoError(t, removeRootLeaf(root, "never-there"))
	assert.NoError(t, removeRootLeaf(root, "leaf"))
	assert.Error(t, removeRootLeaf(root, "nonempty"))
}

func TestRootEntryDisposition_AllBranches(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, filepath.Join(dir, "regular"))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0o755))
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	readErr := errors.New("sentinel read failure")

	done, err := rootEntryDisposition(root, "missing", readErr)
	assert.True(t, done)
	assert.NoError(t, err)

	done, err = rootEntryDisposition(root, "subdir", readErr)
	assert.False(t, done)
	assert.ErrorIs(t, err, readErr)

	done, err = rootEntryDisposition(root, "regular", readErr)
	assert.False(t, done)
	assert.NoError(t, err, "the entry turned into a leaf: retry as one")

	// Lstat through a regular file fails with ENOTDIR, not ErrNotExist.
	done, err = rootEntryDisposition(root, "regular/child", readErr)
	assert.False(t, done)
	require.Error(t, err)
	assert.NotErrorIs(t, err, readErr)
}

func TestRemoveRootDirectoryPass_MissingFileAndRealDirs(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, filepath.Join(dir, "regular"))
	writeProfileFile(t, filepath.Join(dir, "tree", "nested", "leaf"))
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	ctx := context.Background()

	done, err := removeRootDirectoryPass(ctx, root, "missing")
	assert.True(t, done)
	assert.NoError(t, err)

	done, err = removeRootDirectoryPass(ctx, root, "regular")
	assert.False(t, done)
	assert.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(dir, "regular"))
	assert.NoError(t, statErr, "a pass over a leaf must not delete it")

	done, err = removeRootDirectoryPass(ctx, root, "tree")
	assert.True(t, done)
	require.NoError(t, err)
	_, statErr = os.Lstat(filepath.Join(dir, "tree"))
	assert.True(t, errors.Is(statErr, os.ErrNotExist))

	// Removing a listed-empty dir from a read-only parent fails with a real
	// error (not ENOTEMPTY), which the pass must surface.
	skipIfPermissionsBypassed(t)
	parent := filepath.Join(dir, "sealed-parent")
	require.NoError(t, os.MkdirAll(filepath.Join(parent, "child"), 0o755))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	require.NoError(t, os.Chmod(parent, 0o500))
	done, err = removeRootDirectoryPass(ctx, root, "sealed-parent/child")
	assert.False(t, done)
	assert.Error(t, err)
}

func TestRemoveRootEntry_CancelledAndUnreadableDir(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, filepath.Join(dir, "leaf"))
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, removeRootEntry(ctx, root, "leaf"), context.Canceled)
	_, statErr := os.Stat(filepath.Join(dir, "leaf"))
	assert.NoError(t, statErr)

	// Lstat through a regular file fails with ENOTDIR, not ErrNotExist.
	assert.Error(t, removeRootEntry(context.Background(), root, "leaf/child"))

	assert.NoError(t, removeRootEntry(context.Background(), root, "missing"))
	assert.NoError(t, removeRootEntry(context.Background(), root, "leaf"))

	skipIfPermissionsBypassed(t)
	sealed := filepath.Join(dir, "sealed")
	writeProfileFile(t, filepath.Join(sealed, "cookie"))
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })
	require.NoError(t, os.Chmod(sealed, 0o000))
	assert.Error(t, removeRootEntry(context.Background(), root, "sealed"))
}

func TestRemoveRootContents_CancelledAndChildFailure(t *testing.T) {
	skipIfPermissionsBypassed(t)
	dir := t.TempDir()
	sealed := filepath.Join(dir, "sealed")
	writeProfileFile(t, filepath.Join(sealed, "cookie"))
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, removeRootContents(ctx, root), context.Canceled)

	require.NoError(t, os.Chmod(sealed, 0o500))
	assert.Error(t, removeRootContents(context.Background(), root),
		"a child that cannot be removed must fail the whole sweep")

	require.NoError(t, os.Chmod(sealed, 0o755))
	assert.NoError(t, removeRootContents(context.Background(), root))
	assert.NoError(t, removeRootContents(context.Background(), root),
		"a second sweep over an empty root is a no-op")
}
