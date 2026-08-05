package push

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

func TestRepository_Save_RejectsEmptyFields(t *testing.T) {
	// No pool needed — validation runs before any query.
	r := &Repository{pool: nil}
	_, err := r.Save(context.Background(), authctx.UserID(1), "", "k", "a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")

	_, err = r.Save(context.Background(), authctx.UserID(1), "https://push.example/x", "", "a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")

	_, err = r.Save(context.Background(), authctx.UserID(1), "https://push.example/x", "k", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}
