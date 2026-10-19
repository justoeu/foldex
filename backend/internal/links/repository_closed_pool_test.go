package links

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/testsupport"
)

// Every exported Repository method must surface a closed-pool failure as a
// non-nil error instead of panicking or swallowing it into a zero value.
// Inputs are valid enough to clear any pre-DB branch in the method so the
// failure comes from the pool operation itself.

var closedPoolUnlockKey = []byte("01234567890123456789012345678901")

func TestRepositoryClosedPoolErrors(t *testing.T) {
	repo := NewRepository(testsupport.ClosedPool(t))
	ctx := context.Background()
	uid := authctx.UserID(7)

	title := "Example"
	slug := "example-link"
	updatedAt := time.Unix(1700000000, 0).UTC()

	tests := []struct {
		name string
		call func() error
	}{
		{"Create derived slug", func() error {
			_, err := repo.Create(ctx, uid, CreateInput{URL: "https://example.com", Title: "Example"})
			return err
		}},
		{"Create explicit slug", func() error {
			_, err := repo.Create(ctx, uid, CreateInput{URL: "https://example.com", Title: "Example", Slug: &slug})
			return err
		}},
		{"Create untitled slug fallback", func() error {
			_, err := repo.Create(ctx, uid, CreateInput{URL: "https://example.com", Title: ""})
			return err
		}},
		{"AssertOwned", func() error {
			return repo.AssertOwned(ctx, uid, 42)
		}},
		{"Get", func() error {
			_, err := repo.Get(ctx, uid, 42)
			return err
		}},
		{"GetByURL", func() error {
			_, err := repo.GetByURL(ctx, uid, "https://example.com")
			return err
		}},
		{"List", func() error {
			_, err := repo.List(ctx, uid, ListQuery{Limit: 10})
			return err
		}},
		{"Update", func() error {
			_, err := repo.Update(ctx, uid, 42, UpdateInput{Title: &title}, closedPoolUnlockKey, "")
			return err
		}},
		{"Delete", func() error {
			return repo.Delete(ctx, uid, 42, closedPoolUnlockKey, "")
		}},
		{"ReplaceOGImage", func() error {
			_, err := repo.ReplaceOGImage(ctx, uid, 42, "https://example.com/og.png")
			return err
		}},
		{"UpdateOGImageIfUnchanged", func() error {
			ok, err := repo.UpdateOGImageIfUnchanged(ctx, uid, 42, "https://example.com/og.png", updatedAt)
			require.False(t, ok)
			return err
		}},
		{"ClearOGImage", func() error {
			return repo.ClearOGImage(ctx, uid, 42)
		}},
		{"InvalidateMissingPreview", func() error {
			ok, err := repo.InvalidateMissingPreview(ctx, uid, 42, "https://example.com/og.png")
			require.False(t, ok)
			return err
		}},
		{"MarkChangeSeen", func() error {
			return repo.MarkChangeSeen(ctx, uid, 42)
		}},
		{"ListRecentChanges clamped", func() error {
			_, err := repo.ListRecentChanges(ctx, uid, 0, 0)
			return err
		}},
		{"ListRecentChanges explicit", func() error {
			_, err := repo.ListRecentChanges(ctx, uid, 3600, 5)
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, tt.call())
		})
	}
}
