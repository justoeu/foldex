package tags

import (
	"errors"
	"net/http"

	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/httperr"
)

func repositoryHTTPError(err error) error {
	switch {
	case errors.Is(err, domainerr.ErrNotFound):
		return httperr.ErrNotFound
	case errors.Is(err, ErrNameTaken):
		return httperr.New(http.StatusConflict, "tag_name_taken", "tag name already exists")
	case errors.Is(err, domainerr.ErrInvalidInput):
		message, _ := domainerr.InvalidInputMessage(err)
		return httperr.New(http.StatusBadRequest, "invalid_input", message)
	default:
		return err
	}
}
