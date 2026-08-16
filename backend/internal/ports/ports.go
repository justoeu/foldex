// Package ports holds cross-domain interfaces so packages do not import
// links solely for Uploader / Enqueuer (ARCH: links as shared kernel).
package ports

import (
	"context"
	"errors"
)

// ErrObjectTooLarge is the adapter-independent error returned when a buffered
// object read exceeds its serving ceiling.
var ErrObjectTooLarge = errors.New("storage: object exceeds max serve size")

// IsObjectTooLarge recognizes the canonical sentinel through wrapping layers.
func IsObjectTooLarge(err error) bool {
	return errors.Is(err, ErrObjectTooLarge)
}

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
