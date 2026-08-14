// Package domainerr contains cross-feature error semantics that are independent
// of any delivery protocol.
package domainerr

import "errors"

var (
	ErrNotFound     = errors.New("resource not found")
	ErrInvalidInput = errors.New("invalid input")
)

type invalidInputError struct {
	message string
}

func (e *invalidInputError) Error() string { return e.message }

func (e *invalidInputError) Unwrap() error { return ErrInvalidInput }

func InvalidInput(message string) error {
	return &invalidInputError{message: message}
}

func InvalidInputMessage(err error) (string, bool) {
	var invalid *invalidInputError
	if !errors.As(err, &invalid) {
		return "", false
	}
	return invalid.message, true
}
