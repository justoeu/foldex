// Package contenthttp maps the errors shared by link/note handlers so the
// tag-taken and locked-folder envelopes stay in lockstep. Feature-specific
// sentinels (url_taken, slug_taken, stale write) stay in each package.
package contenthttp

import (
	"errors"
	"net/http"

	"foldex/internal/folders"
	"foldex/internal/pkg/httperr"
	"foldex/internal/tags"
)

func Map(err error) error {
	switch {
	case errors.Is(err, tags.ErrNameTaken):
		return httperr.New(http.StatusConflict, "tag_name_taken", "tag name already exists")
	case errors.Is(err, folders.ErrLocked):
		return folders.HTTPError(err)
	default:
		if mapped := httperr.FromDomain(err); mapped != nil {
			return mapped
		}
		return err
	}
}
