// Package ports holds cross-domain interfaces so packages do not import
// links solely for Uploader / Enqueuer (ARCH: links as shared kernel).
package ports

import "context"

// Uploader stores and fetches object bytes (S3-compatible storage adapters).
type Uploader interface {
	Upload(ctx context.Context, key string, data []byte, contentType string) error
	GetObject(ctx context.Context, key string) ([]byte, string, error)
	DeleteObject(ctx context.Context, key string) error
}

// Enqueuer schedules async work (preview / changecheck workers).
type Enqueuer interface {
	Enqueue(linkID int64) error
}
