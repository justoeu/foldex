package auth

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/httperr"
)

func TestRepositoryHTTPErrorMapping(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{"invite not found", fmt.Errorf("revoke: %w", ErrInviteNotFound), http.StatusNotFound, "invite not found"},
		{"session not found", fmt.Errorf("revoke: %w", ErrSessionNotFound), http.StatusNotFound, "resource not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *httperr.Error
			require.ErrorAs(t, repositoryHTTPError(tt.err), &got)
			assert.Equal(t, tt.status, got.Status)
			assert.Equal(t, "not_found", got.Code)
			assert.Equal(t, tt.message, got.Message)
		})
	}

	unknown := errors.New("database unavailable")
	assert.ErrorIs(t, repositoryHTTPError(unknown), unknown)
}
