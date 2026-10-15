package folders

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/domainerr"
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

// Configured reports whether this gate can actually decide. A zero gate
// (a handler nobody wired WithFolderGate on) must fail closed on foldered
// entities rather than wave them through.
func (g ContentGate) Configured() bool { return g.lookup != nil }

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

// CheckEntityFolderTx is the mutation-side gate, run inside the caller's
// transaction: it locks the entity row, reads the CURRENT folder's password
// hash under the folder row lock, and demands a valid unlock token when one
// is set. Checking in the handler before opening the transaction would
// leave a window where a password lands between the check and the write.
// The destination folder of a move is deliberately never gated (INV-065:
// writing into a locked folder reveals nothing).
func CheckEntityFolderTx(ctx context.Context, tx pgx.Tx, table string, uid authctx.UserID, entityID int64, secret []byte, token string) error {
	switch table {
	case "link", "note":
	default:
		return fmt.Errorf("folder gate: unknown entity table %q", table)
	}
	var folderID *int64
	err := tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT folder_id FROM %s WHERE user_id = $1 AND id = $2 FOR NO KEY UPDATE`, table),
		int64(uid), entityID).Scan(&folderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domainerr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("folder gate: lock entity: %w", err)
	}
	if folderID == nil {
		return nil
	}
	var passwordHash *string
	err = tx.QueryRow(ctx,
		`SELECT password_hash FROM folder WHERE user_id = $1 AND id = $2 FOR NO KEY UPDATE`,
		int64(uid), *folderID).Scan(&passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("folder gate: lock folder: %w", err)
	}
	return CheckUnlock(secret, *folderID, passwordHash, token)
}
