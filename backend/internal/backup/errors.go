package backup

import "errors"

var (
	ErrRestoreInProgress = errors.New("another restore is already in progress")
	ErrInvalidBackup     = errors.New("invalid backup")
)

type invalidBackupError struct {
	message       string
	unprocessable bool
}

func (e *invalidBackupError) Error() string { return e.message }

func (e *invalidBackupError) Unwrap() error { return ErrInvalidBackup }

func invalidBackup(message string) error {
	return &invalidBackupError{message: message}
}

func unprocessableBackup(message string) error {
	return &invalidBackupError{message: message, unprocessable: true}
}

func IsUnprocessableBackup(err error) bool {
	var inv *invalidBackupError
	return errors.As(err, &inv) && inv.unprocessable
}
