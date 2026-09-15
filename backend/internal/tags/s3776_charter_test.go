package tags

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/domainerr"
)

func TestS3776Charter_SetEntityTagsWithPending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	uid := authctx.UserID(1)

	t.Run("invalid pending is refused before any write", func(t *testing.T) {
		t.Parallel()
		err := SetEntityTagsWithPending(ctx, nil, uid, "link", 1, nil, []CreateInput{{Name: ""}})
		require.Error(t, err)
		msg, ok := domainerr.InvalidInputMessage(err)
		require.True(t, ok)
		assert.Equal(t, "name is required", msg)
	})

	t.Run("invalid color is refused before any write", func(t *testing.T) {
		t.Parallel()
		err := SetEntityTagsWithPending(ctx, nil, uid, "link", 1, nil, []CreateInput{{Name: "work", Color: "red"}})
		require.Error(t, err)
		msg, ok := domainerr.InvalidInputMessage(err)
		require.True(t, ok)
		assert.Contains(t, msg, "color must be a hex")
	})

	t.Run("name too long is refused before any write", func(t *testing.T) {
		t.Parallel()
		err := SetEntityTagsWithPending(ctx, nil, uid, "note", 1, nil, []CreateInput{{Name: stringsRepeat("x", 81)}})
		require.Error(t, err)
		msg, ok := domainerr.InvalidInputMessage(err)
		require.True(t, ok)
		assert.Equal(t, "name too long (max 80)", msg)
	})
}

func stringsRepeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
