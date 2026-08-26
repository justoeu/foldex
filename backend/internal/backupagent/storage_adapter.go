package backupagent

import (
	"context"
	"io"

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

func (u storageUploader) WalkObjects(ctx context.Context, prefix string, visit func(ObjectInfo) error) error {
	return u.c.WalkObjects(ctx, prefix, func(object storage.ObjectInfo) error {
		return visit(ObjectInfo{Key: object.Key, Size: object.Size})
	})
}

func (u storageUploader) DeleteObjects(ctx context.Context, keys []string) error {
	return u.c.DeleteObjects(ctx, keys)
}
