package backup

import (
	"errors"
	"net/http"

	"foldex/internal/pkg/httperr"
)

func restoreHTTPError(err error) error {
	switch {
	case errors.Is(err, ErrRestoreInProgress):
		return httperr.New(http.StatusConflict, "restore_in_progress", err.Error())
	case errors.Is(err, ErrInvalidBackup):
		status := http.StatusBadRequest
		if IsUnprocessableBackup(err) {
			status = http.StatusUnprocessableEntity
		}
		return httperr.New(status, "invalid_backup", err.Error())
	default:
		return err
	}
}
