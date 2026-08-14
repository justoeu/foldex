package folders

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

type contentGateLookup struct {
	hashes    []*string
	errs      []error
	calls     int
	userIDs   []authctx.UserID
	folderIDs []int64
}

func (l *contentGateLookup) PasswordHashFor(_ context.Context, uid authctx.UserID, folderID int64) (*string, error) {
	i := l.calls
	l.calls++
	l.userIDs = append(l.userIDs, uid)
	l.folderIDs = append(l.folderIDs, folderID)
	if i < len(l.errs) && l.errs[i] != nil {
		return nil, l.errs[i]
	}
	if i >= len(l.hashes) {
		return nil, nil
	}
	return l.hashes[i], nil
}

func TestListWithContentGate_UngatedTypedList(t *testing.T) {
	type result struct{ ID int64 }
	want := []result{{ID: 7}}
	calls := 0

	got, err := ListWithContentGate(context.Background(), ContentGate{}, authctx.UserID(41), nil, "ignored", func() ([]result, error) {
		calls++
		return want, nil
	})

	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 1, calls)
}

func TestListWithContentGate_FailsClosedWithoutLookup(t *testing.T) {
	folderID := int64(9)
	listCalled := false

	got, err := ListWithContentGate(context.Background(), ContentGate{}, authctx.UserID(41), &folderID, "", func() ([]string, error) {
		listCalled = true
		return []string{"secret"}, nil
	})

	require.ErrorIs(t, err, ErrGateUnavailable)
	assert.Nil(t, got)
	assert.False(t, listCalled)
}

func TestListWithContentGate_ChecksBeforeAndAfterTypedList(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	hash := "current-hash"
	folderID := int64(9)
	uid := authctx.UserID(41)
	lookup := &contentGateLookup{hashes: []*string{&hash, &hash}}
	token := IssueUnlockToken(key, folderID, hash)

	got, err := ListWithContentGate(context.Background(), NewContentGate(lookup, key), uid, &folderID, token, func() (map[string]int, error) {
		return map[string]int{"typed": 1}, nil
	})

	require.NoError(t, err)
	assert.Equal(t, map[string]int{"typed": 1}, got)
	assert.Equal(t, []authctx.UserID{uid, uid}, lookup.userIDs)
	assert.Equal(t, []int64{folderID, folderID}, lookup.folderIDs)
}

func TestListWithContentGate_RejectsPasswordHashChangeAfterList(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	oldHash := "old-hash"
	newHash := "new-hash"
	folderID := int64(9)
	lookup := &contentGateLookup{hashes: []*string{&oldHash, &newHash}}
	listCalls := 0

	got, err := ListWithContentGate(context.Background(), NewContentGate(lookup, key), authctx.UserID(41), &folderID,
		IssueUnlockToken(key, folderID, oldHash), func() ([]string, error) {
			listCalls++
			return []string{"secret"}, nil
		})

	require.ErrorIs(t, err, ErrLocked)
	assert.Nil(t, got, "post-check failure must not return the fetched content")
	assert.Equal(t, 1, listCalls)
	assert.Equal(t, 2, lookup.calls)
}

func TestListWithContentGate_ReturnsListErrorWithoutASecondLookup(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	hash := "current-hash"
	folderID := int64(9)
	lookup := &contentGateLookup{hashes: []*string{&hash}}
	wantErr := errors.New("list failed")

	got, err := ListWithContentGate(context.Background(), NewContentGate(lookup, key), authctx.UserID(41), &folderID,
		IssueUnlockToken(key, folderID, hash), func() (int, error) {
			return 42, wantErr
		})

	require.ErrorIs(t, err, wantErr)
	assert.Zero(t, got)
	assert.Equal(t, 1, lookup.calls)
}
