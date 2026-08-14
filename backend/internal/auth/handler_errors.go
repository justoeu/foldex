package auth

import (
	"errors"
	"net/http"

	"foldex/internal/pkg/httperr"
)

func repositoryHTTPError(err error) error {
	switch {
	case errors.Is(err, ErrInviteNotFound):
		return httperr.New(http.StatusNotFound, "not_found", "invite not found")
	case errors.Is(err, ErrSessionNotFound):
		return httperr.ErrNotFound
	default:
		return err
	}
}
