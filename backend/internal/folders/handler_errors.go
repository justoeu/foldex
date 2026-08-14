package folders

import (
	"errors"
	"net/http"

	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/httperr"
)

// HTTPError is used by handlers that expose folder-backed operations, including
// the shared content gate consumed by links, notes, and entries.
func HTTPError(err error) error {
	switch {
	case errors.Is(err, domainerr.ErrNotFound):
		return httperr.ErrNotFound
	case errors.Is(err, ErrLocked):
		return httperr.New(http.StatusForbidden, "folder_locked", "this folder is password-protected")
	case errors.Is(err, ErrWrongPassword):
		return httperr.New(http.StatusUnauthorized, "wrong_password", "current password is required to change or remove an existing password")
	case errors.Is(err, ErrHintWithoutPassword):
		return httperr.New(http.StatusBadRequest, "invalid_input", "cannot set a password hint on a folder without a password")
	case errors.Is(err, ErrHintMatchesPassword):
		return httperr.New(http.StatusBadRequest, "invalid_input", "password hint must not be the same as the password")
	case errors.Is(err, ErrParentCycle):
		return httperr.New(http.StatusConflict, "parent_cycle", "parent_id would create a folder cycle")
	case errors.Is(err, ErrDescendantProtected):
		return httperr.New(http.StatusConflict, "descendant_protected", "folder subtree contains password-protected descendants")
	default:
		return err
	}
}
