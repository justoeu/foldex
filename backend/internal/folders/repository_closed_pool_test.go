package folders

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/testsupport"
)

// Every exported Repository method must surface a closed-pool failure as a
// non-nil error instead of panicking or swallowing it into a zero value.
// Inputs are valid enough to clear any pre-DB branch in the method so the
// failure comes from the pool operation itself; the pre-DB validation cases
// (oversized password hash, self-parent) are pinned separately because they
// error before the pool is ever touched.

var closedPoolFolderUnlockKey = []byte("01234567890123456789012345678901")

func TestRepositoryClosedPoolErrors(t *testing.T) {
	repo := NewRepository(testsupport.ClosedPool(t))
	ctx := context.Background()
	uid := authctx.UserID(7)

	password := "folder-pass"
	hint := "a reminder"
	oversized := strings.Repeat("x", 73)
	name := "Renamed"
	parentID := int64(9)
	selfParent := int64(42)

	tests := []struct {
		name string
		call func() error
	}{
		{"Create unprotected", func() error {
			_, err := repo.Create(ctx, uid, CreateInput{Name: "Docs", Color: "#6366F1"})
			return err
		}},
		{"Create protected", func() error {
			_, err := repo.Create(ctx, uid, CreateInput{Name: "Docs", Color: "#6366F1", Password: &password, PasswordHint: &hint})
			return err
		}},
		{"Create oversized password fails hashing pre-DB", func() error {
			_, err := repo.Create(ctx, uid, CreateInput{Name: "Docs", Color: "#6366F1", Password: &oversized})
			return err
		}},
		{"PasswordHashFor", func() error {
			_, err := repo.PasswordHashFor(ctx, uid, 42)
			return err
		}},
		{"List full", func() error {
			_, err := repo.List(ctx, uid, ListQuery{})
			return err
		}},
		{"List slim", func() error {
			_, err := repo.List(ctx, uid, ListQuery{Slim: true})
			return err
		}},
		{"List root only", func() error {
			_, err := repo.List(ctx, uid, ListQuery{RootOnly: true})
			return err
		}},
		{"List by parent", func() error {
			_, err := repo.List(ctx, uid, ListQuery{ParentID: &parentID})
			return err
		}},
		{"Get", func() error {
			_, err := repo.Get(ctx, uid, 42)
			return err
		}},
		{"Update empty falls back to Get", func() error {
			_, err := repo.Update(ctx, uid, 42, UpdateInput{})
			return err
		}},
		{"Update rename", func() error {
			_, err := repo.Update(ctx, uid, 42, UpdateInput{Name: &name})
			return err
		}},
		{"Update set password", func() error {
			_, err := repo.Update(ctx, uid, 42, UpdateInput{PasswordSet: true, Password: &password})
			return err
		}},
		{"Update oversized password fails hashing pre-DB", func() error {
			_, err := repo.Update(ctx, uid, 42, UpdateInput{PasswordSet: true, Password: &oversized})
			return err
		}},
		{"Update self-parent rejected pre-DB", func() error {
			_, err := repo.Update(ctx, uid, 42, UpdateInput{ParentIDSet: true, ParentID: &selfParent})
			return err
		}},
		{"ResetPasswordByMaster", func() error {
			return repo.ResetPasswordByMaster(ctx, uid, 42, "master-pass")
		}},
		{"Delete", func() error {
			return repo.Delete(ctx, uid, 42, closedPoolFolderUnlockKey, "")
		}},
		{"deleteCascade", func() error {
			_, err := repo.deleteCascade(ctx, uid, 42, closedPoolFolderUnlockKey, "")
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, tt.call())
		})
	}
}
