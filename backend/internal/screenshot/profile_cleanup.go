package screenshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"syscall"
)

const profileCleanupBatchSize = 128

func removeAllContext(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.Remove(path)
	}
	root, err := os.OpenRoot(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	openedInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = root.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("screenshot: profile changed during cleanup")
	}
	cleanupErr := removeRootContents(ctx, root)
	closeErr := root.Close()
	if cleanupErr != nil {
		return cleanupErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func removeRootContents(ctx context.Context, root *os.Root) error {
	for {
		entries, err := readRootDirBatch(ctx, root, ".")
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		for _, entry := range entries {
			if err := removeRootEntry(ctx, root, entry.Name()); err != nil {
				return err
			}
		}
	}
}

func removeRootEntry(ctx context.Context, root *os.Root, name string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return removeRootLeaf(root, name)
		}
		done, err := removeRootDirectoryPass(ctx, root, name)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

func removeRootLeaf(root *os.Root, name string) error {
	err := root.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func removeRootDirectoryPass(ctx context.Context, root *os.Root, name string) (bool, error) {
	entries, err := readRootDirBatch(ctx, root, name)
	if err != nil {
		return rootEntryDisposition(root, name, err)
	}
	for _, entry := range entries {
		if err := removeRootEntry(ctx, root, path.Join(name, entry.Name())); err != nil {
			return false, err
		}
	}
	err = root.Remove(name)
	switch {
	case err == nil || errors.Is(err, os.ErrNotExist):
		return true, nil
	case errors.Is(err, syscall.ENOTEMPTY):
		return false, nil
	default:
		return false, err
	}
}

func rootEntryDisposition(root *os.Root, name string, readErr error) (bool, error) {
	latest, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if latest.IsDir() {
		return false, readErr
	}
	return false, nil
}

func readRootDirBatch(ctx context.Context, root *os.Root, name string) ([]os.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	entries, readErr := dir.ReadDir(profileCleanupBatchSize)
	closeErr := dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return entries, nil
}
