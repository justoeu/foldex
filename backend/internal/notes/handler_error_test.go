package notes

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/httperr"
	"foldex/internal/tags"
)

func TestRepositoryHTTPErrorMapping(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{"not found", fmt.Errorf("get: %w", domainerr.ErrNotFound), http.StatusNotFound, "not_found", "resource not found"},
		{"slug taken", fmt.Errorf("create: %w", ErrSlugTaken), http.StatusConflict, "slug_taken", "slug already in use"},
		{"stale write", fmt.Errorf("update: %w", ErrStaleWrite), http.StatusConflict, "conflict", "note was modified; refetch and retry"},
		{"pending tag taken", fmt.Errorf("create tags: %w", tags.ErrNameTaken), http.StatusConflict, "tag_name_taken", "tag name already exists"},
		{"invalid tags", domainerr.InvalidInput("unknown tag id"), http.StatusBadRequest, "invalid_input", "unknown tag id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *httperr.Error
			require.ErrorAs(t, repositoryHTTPError(tt.err), &got)
			assert.Equal(t, tt.status, got.Status)
			assert.Equal(t, tt.code, got.Code)
			assert.Equal(t, tt.message, got.Message)
		})
	}

	unknown := errors.New("database unavailable")
	assert.ErrorIs(t, repositoryHTTPError(unknown), unknown)
}
