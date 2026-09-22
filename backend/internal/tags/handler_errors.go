package tags

import (
	"errors"
	"net/http"

	"foldex/internal/pkg/httperr"
)

func HTTPError(err error) error {
	switch {
	case errors.Is(err, ErrNameTaken):
		return httperr.New(http.StatusConflict, "tag_name_taken", "tag name already exists")
	default:
		if mapped := httperr.FromDomain(err); mapped != nil {
			return mapped
		}
		return err
	}
}

func repositoryHTTPError(err error) error { return HTTPError(err) }
