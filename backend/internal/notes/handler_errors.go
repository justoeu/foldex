package notes

import (
	"errors"
	"net/http"

	"foldex/internal/pkg/contenthttp"
	"foldex/internal/pkg/httperr"
)

func repositoryHTTPError(err error) error {
	switch {
	case errors.Is(err, ErrSlugTaken):
		return httperr.New(http.StatusConflict, "slug_taken", "slug already in use")
	case errors.Is(err, ErrStaleWrite):
		return httperr.New(http.StatusConflict, "conflict", "note was modified; refetch and retry")
	default:
		return contenthttp.Map(err)
	}
}
