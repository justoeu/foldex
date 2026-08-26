package backupagent

import (
	"context"
	"io"

	"foldex/internal/backup"
	"foldex/internal/storage"
)

// NewStorageUploader adapts *storage.Client to the narrow Uploader the jobs
// consume — the same shim shape cmd/server uses for backup.StorageBucket, so
// tests keep mocking a three-method interface instead of the whole client.
func NewStorageUploader(c *storage.Client) Uploader {
	return storageUploader{c: c}
}

type storageUploader struct{ c *storage.Client }

func (u storageUploader) PutObjectStream(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	return u.c.PutObjectStream(ctx, key, r, size, contentType)
}

func (u storageUploader) OpenObject(ctx context.Context, key string) (io.ReadCloser, error) {
	return u.c.OpenObject(ctx, key)
}

func (u storageUploader) WalkObjects(ctx context.Context, prefix string, visit func(ObjectInfo) error) error {
	return u.c.WalkObjects(ctx, prefix, func(object storage.ObjectInfo) error {
		return visit(ObjectInfo{Key: object.Key, Size: object.Size,
			ETag: object.ETag, LastModified: object.LastModified})
	})
}

func (u storageUploader) DeleteObjects(ctx context.Context, keys []string) error {
	return u.c.DeleteObjects(ctx, keys)
}

// NewBackupSourceBucket adapts *storage.Client to backup.StorageBucket so the
// agent can build a backup.Service over the SOURCE RustFS for user_zip — the
// same shim cmd/server keeps privately (backupStorageAdapter), duplicated here
// on purpose: exporting it from main is impossible and the storage package
// staying dependency-free of backup is a deliberate boundary.
func NewBackupSourceBucket(c *storage.Client) backup.StorageBucket {
	return backupSourceBucket{c: c}
}

type backupSourceBucket struct{ c *storage.Client }

func (b backupSourceBucket) WalkObjects(ctx context.Context, prefix string, visit func(backup.ObjectInfo) error) error {
	return b.c.WalkObjects(ctx, prefix, func(object storage.ObjectInfo) error {
		return visit(backup.ObjectInfo{Key: object.Key, Size: object.Size})
	})
}

func (b backupSourceBucket) OpenObject(ctx context.Context, key string) (io.ReadCloser, error) {
	return b.c.OpenObject(ctx, key)
}

func (b backupSourceBucket) PutObjectStream(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	return b.c.PutObjectStream(ctx, key, r, size, contentType)
}

func (b backupSourceBucket) ExistingObjects(ctx context.Context, keys []string) (map[string]bool, error) {
	return b.c.ExistingObjects(ctx, keys)
}

func (b backupSourceBucket) DeleteObjects(ctx context.Context, keys []string) error {
	return b.c.DeleteObjects(ctx, keys)
}
