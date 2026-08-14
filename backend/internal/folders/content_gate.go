package folders

import (
	"context"

	"foldex/internal/pkg/authctx"
)

// PasswordHashLookup resolves the current password hash of an owned folder.
type PasswordHashLookup interface {
	PasswordHashFor(ctx context.Context, uid authctx.UserID, id int64) (*string, error)
}

// ContentGate validates the unlock proof used by folder-scoped list endpoints.
type ContentGate struct {
	lookup    PasswordHashLookup
	unlockKey []byte
}

func NewContentGate(lookup PasswordHashLookup, unlockKey []byte) ContentGate {
	return ContentGate{lookup: lookup, unlockKey: unlockKey}
}

func (g ContentGate) Check(ctx context.Context, uid authctx.UserID, folderID int64, token string) error {
	if g.lookup == nil {
		return ErrGateUnavailable
	}
	hash, err := g.lookup.PasswordHashFor(ctx, uid, folderID)
	if err != nil {
		return err
	}
	return CheckUnlock(g.unlockKey, folderID, hash, token)
}

// ListWithContentGate runs a typed list operation between two checks of the
// current folder password hash. The second check prevents content fetched under
// an old hash from being returned after a concurrent password change.
func ListWithContentGate[T any](ctx context.Context, gate ContentGate, uid authctx.UserID, folderID *int64, token string, list func() (T, error)) (T, error) {
	if folderID == nil {
		return list()
	}

	var zero T
	if err := gate.Check(ctx, uid, *folderID, token); err != nil {
		return zero, err
	}
	out, err := list()
	if err != nil {
		return zero, err
	}
	if err := gate.Check(ctx, uid, *folderID, token); err != nil {
		return zero, err
	}
	return out, nil
}
