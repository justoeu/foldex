package folders

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/httperr"
)

func TestHTTPErrorMapping(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{"not found", fmt.Errorf("get: %w", domainerr.ErrNotFound), http.StatusNotFound, "not_found", "resource not found"},
		{"locked", fmt.Errorf("gate: %w", ErrLocked), http.StatusForbidden, "folder_locked", "this folder is password-protected"},
		{"wrong current password", fmt.Errorf("update: %w", ErrWrongPassword), http.StatusUnauthorized, "wrong_password", "current password is required to change or remove an existing password"},
		{"hint without password", ErrHintWithoutPassword, http.StatusBadRequest, "invalid_input", "cannot set a password hint on a folder without a password"},
		{"hint matches password", ErrHintMatchesPassword, http.StatusBadRequest, "invalid_input", "password hint must not be the same as the password"},
		{"parent cycle", fmt.Errorf("update: %w", ErrParentCycle), http.StatusConflict, "parent_cycle", "parent_id would create a folder cycle"},
		{"protected descendant", fmt.Errorf("delete: %w", ErrDescendantProtected), http.StatusConflict, "descendant_protected", "folder subtree contains password-protected descendants"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *httperr.Error
			require.ErrorAs(t, HTTPError(tt.err), &got)
			assert.Equal(t, tt.status, got.Status)
			assert.Equal(t, tt.code, got.Code)
			assert.Equal(t, tt.message, got.Message)
		})
	}

	unknown := errors.New("database unavailable")
	assert.ErrorIs(t, HTTPError(unknown), unknown)
}
