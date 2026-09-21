package links

import (
	"errors"
	"net/http"

	"foldex/internal/pkg/contenthttp"
	"foldex/internal/pkg/httperr"
)

func repositoryHTTPError(err error) error {
	switch {
	case errors.Is(err, ErrURLTaken):
		return httperr.New(http.StatusConflict, "url_taken", "url already in use")
	case errors.Is(err, ErrSlugTaken):
		return httperr.New(http.StatusConflict, "slug_taken", "slug already in use")
	case errors.Is(err, ErrStaleWrite):
		return httperr.New(http.StatusConflict, "conflict", "link was modified; refetch and retry")
	default:
		return contenthttp.Map(err)
	}
}
