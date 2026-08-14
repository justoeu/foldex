package linkimage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var legacyExtensions = []string{"png", "jpg", "gif", "webp"}

const cleanupTimeout = 10 * time.Second

type Uploader interface {
	Upload(ctx context.Context, key string, data []byte, contentType string) error
	DeleteObject(ctx context.Context, key string) error
}

type Stored struct {
	Key string
	URL string
}

// Store writes to an operation-owned key so a losing concurrent writer can
// delete only its own bytes.
func Store(ctx context.Context, uploader Uploader, prefix string, id int64, ext string, data []byte, contentType string) (Stored, error) {
	key := fmt.Sprintf("%s/%d.%s.%s", prefix, id, uuid.NewString(), ext)
	if err := uploader.Upload(ctx, key, data, contentType); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		_ = uploader.DeleteObject(cleanupCtx, key)
		cancel()
		return Stored{}, err
	}
	return Stored{Key: key, URL: "/api/files/" + key}, nil
}

func Delete(ctx context.Context, uploader Uploader, key string) error {
	return uploader.DeleteObject(ctx, key)
}

// PurgeLegacy removes only the deterministic pre-versioning keys. It never
// touches another operation's versioned object.
func PurgeLegacy(ctx context.Context, uploader Uploader, prefix string, id int64) []error {
	errs := make([]error, 0)
	for _, ext := range legacyExtensions {
		if err := uploader.DeleteObject(ctx, fmt.Sprintf("%s/%d.%s", prefix, id, ext)); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// LocalKey extracts only an exact local link-media proxy URL.
func LocalKey(proxyURL string) (string, bool) {
	const base = "/api/files/"
	if !strings.HasPrefix(proxyURL, base) {
		return "", false
	}
	key := strings.TrimPrefix(proxyURL, base)
	if strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return "", false
	}
	if !strings.HasPrefix(key, "screenshots/") && !strings.HasPrefix(key, "images/") {
		return "", false
	}
	return key, true
}
