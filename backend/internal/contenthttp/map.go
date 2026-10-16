// Package contenthttp maps the errors shared by link/note handlers so the
// tag-taken and locked-folder envelopes stay in lockstep. Feature-specific
// sentinels (url_taken, slug_taken, stale write) stay in each package.
package contenthttp

import (
	"errors"

	"foldex/internal/folders"
	"foldex/internal/tags"
)

func Map(err error) error {
	if errors.Is(err, folders.ErrLocked) {
		return folders.HTTPError(err)
	}
	return tags.HTTPError(err)
}
