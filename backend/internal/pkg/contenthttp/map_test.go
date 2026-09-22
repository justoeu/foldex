package contenthttp

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/folders"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/httperr"
	"foldex/internal/tags"
)

func TestMap_SharedSentinels(t *testing.T) {
	got := Map(tags.ErrNameTaken)
	var he *httperr.Error
	require.ErrorAs(t, got, &he)
	assert.Equal(t, http.StatusConflict, he.Status)
	assert.Equal(t, "tag_name_taken", he.Code)

	got = Map(folders.ErrLocked)
	require.ErrorAs(t, got, &he)
	assert.Equal(t, http.StatusForbidden, he.Status)
	assert.Equal(t, "folder_locked", he.Code)

	got = Map(domainerr.ErrNotFound)
	assert.Equal(t, httperr.ErrNotFound, got)

	passthrough := errors.New("other")
	assert.Equal(t, passthrough, Map(passthrough))
}
