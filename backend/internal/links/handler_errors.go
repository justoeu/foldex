package links

import (
	"errors"
	"net/http"

	"foldex/internal/folders"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/httperr"
	"foldex/internal/tags"
)

func repositoryHTTPError(err error) error {
	switch {
	case errors.Is(err, domainerr.ErrNotFound):
		return httperr.ErrNotFound
	case errors.Is(err, ErrURLTaken):
		return httperr.New(http.StatusConflict, "url_taken", "url already in use")
	case errors.Is(err, ErrSlugTaken):
		return httperr.New(http.StatusConflict, "slug_taken", "slug already in use")
	case errors.Is(err, ErrStaleWrite):
		return httperr.New(http.StatusConflict, "conflict", "link was modified; refetch and retry")
	case errors.Is(err, tags.ErrNameTaken):
		return httperr.New(http.StatusConflict, "tag_name_taken", "tag name already exists")
	case errors.Is(err, folders.ErrLocked):
		return folders.HTTPError(err)
	case errors.Is(err, domainerr.ErrInvalidInput):
		message, _ := domainerr.InvalidInputMessage(err)
		return httperr.New(http.StatusBadRequest, "invalid_input", message)
	default:
		return err
	}
}
