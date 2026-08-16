package push

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

func TestRepository_Save_RejectsEmptyFields(t *testing.T) {
	// No pool needed — validation runs before any query.
	r := &Repository{pool: nil}
	_, err := r.Save(context.Background(), authctx.UserID(1), "", "k", "a")
	require.ErrorIs(t, err, ErrInvalidSubscription)

	_, err = r.Save(context.Background(), authctx.UserID(1), "https://push.example/x", "", "a")
	require.ErrorIs(t, err, ErrInvalidSubscription)

	_, err = r.Save(context.Background(), authctx.UserID(1), "https://push.example/x", "k", "")
	require.ErrorIs(t, err, ErrInvalidSubscription)
	assert.False(t, errors.Is(err, ErrSubscriptionLimit))
}

func TestRepository_BatchStateChangesSkipEmptyBatches(t *testing.T) {
	r := &Repository{pool: nil}
	uid := authctx.UserID(1)

	assert.NoError(t, r.DeleteGone(context.Background(), uid, nil))
	assert.NoError(t, r.MarkUsed(context.Background(), uid, nil))
}
