package folders

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/pwhash"
)

func TestUnmarshalFolderPreviews_RedactionArms(t *testing.T) {
	ok := &Folder{}
	require.NoError(t, unmarshalFolderPreviews(ok, []byte(`[{"id":1,"title":"a"}]`), []byte(`[{"id":2,"name":"b","color":"#abc"}]`)))
	require.Len(t, ok.Previews, 1)
	assert.Equal(t, "a", ok.Previews[0].Title)
	require.Len(t, ok.PreviewFolders, 1)
	assert.Equal(t, "b", ok.PreviewFolders[0].Name)

	empty := &Folder{}
	require.NoError(t, unmarshalFolderPreviews(empty, nil, nil))
	assert.Empty(t, empty.Previews)
	assert.Empty(t, empty.PreviewFolders)

	err := unmarshalFolderPreviews(&Folder{}, []byte(`{`), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal previews")

	err = unmarshalFolderPreviews(&Folder{}, nil, []byte(`{`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal preview_folders")
}

func TestPrepareFolderUpdate_EmptyParentSelfAndPassword(t *testing.T) {
	got, err := prepareFolderUpdate(authctx.UserID(1), 9, UpdateInput{})
	require.NoError(t, err)
	assert.Nil(t, got, "an empty update must not build SQL")

	self := int64(9)
	_, err = prepareFolderUpdate(authctx.UserID(1), 9, UpdateInput{ParentIDSet: true, ParentID: &self})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent_id cannot equal id")

	tooLong := strings.Repeat("a", pwhash.MaxPlainBytes+1)
	_, err = prepareFolderUpdate(authctx.UserID(1), 9, UpdateInput{PasswordSet: true, Password: &tooLong})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hash new password")

	name := "Docs"
	color := "#abc"
	pw := "hunter22"
	hint := "not the password"
	parent := int64(3)
	got, err = prepareFolderUpdate(authctx.UserID(7), 9, UpdateInput{
		Name: &name, Color: &color,
		PasswordSet: true, Password: &pw,
		PasswordHintSet: true, PasswordHint: &hint,
		ParentIDSet: true, ParentID: &parent,
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, authctx.UserID(7), got.uid)
	assert.EqualValues(t, 9, got.id)
	assert.True(t, got.cycleCheckNeeded)
	require.NotNil(t, got.newPasswordHash)
	require.NotNil(t, got.hintToValidate)
	assert.Equal(t, hint, *got.hintToValidate)
	assert.Contains(t, got.q, "name = $1")
	assert.Contains(t, got.q, "password_hash")
	assert.Contains(t, got.q, "password_hint")
	assert.Contains(t, got.q, "parent_id")
}

func TestPrepareFolderUpdate_RemovingPasswordClearsHint(t *testing.T) {
	got, err := prepareFolderUpdate(authctx.UserID(1), 4, UpdateInput{PasswordSet: true, Password: nil})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.newPasswordHash)
	assert.Nil(t, got.hintToValidate)
	assert.Contains(t, got.q, "password_hash")
	assert.Contains(t, got.q, "password_hint")
}

func TestListWhere_ParentRootAndFlat(t *testing.T) {
	args := []any{int64(1)}
	assert.Equal(t, "WHERE f.user_id = $1", listWhere(ListQuery{}, &args))

	args = []any{int64(1)}
	assert.Equal(t, "WHERE f.user_id = $1 AND f.parent_id IS NULL", listWhere(ListQuery{RootOnly: true}, &args))

	parent := int64(8)
	args = []any{int64(1)}
	assert.Equal(t, "WHERE f.user_id = $1 AND f.parent_id = $2", listWhere(ListQuery{ParentID: &parent}, &args))
	assert.Equal(t, []any{int64(1), int64(8)}, args)
}

func TestUpdate_PrepareErrorDoesNotTouchTheDatabase(t *testing.T) {
	self := int64(1)
	_, err := (*Repository)(nil).Update(context.Background(), 1, 1, UpdateInput{ParentIDSet: true, ParentID: &self})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent_id cannot equal id")
}
