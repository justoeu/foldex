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

// ErrObjectNotFound is the adapter-independent "this key holds nothing".
//
// It exists so callers can tell a MISSING object from an unreachable store,
// and that distinction is load-bearing: the file proxy uses it to decide
// whether a link's preview should be regenerated. A transport failure — the
// store down, DNS gone, a timeout — must NEVER take that branch, or one
// network blip clears every og_image_url on the instance and the worker
// re-screenshots the whole library. Same rule as the push subscriptions in
// CLAUDE.md §4: 404/410 removes the row, a transport error never does.
var ErrObjectNotFound = errors.New("storage: object not found")

// IsObjectNotFound recognizes the canonical sentinel through wrapping layers.
func IsObjectNotFound(err error) bool {
	return errors.Is(err, ErrObjectNotFound)
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
