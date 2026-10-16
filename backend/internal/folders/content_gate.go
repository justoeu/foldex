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
// transaction: it demands a valid unlock token when the entity's CURRENT
// folder has a password. Checking in the handler before opening the
// transaction would leave a window where a password lands between the
// check and the write. The destination folder of a move is deliberately
// never gated (INV-065: writing into a locked folder reveals nothing).
//
// Lock order is folder BEFORE entity — the same order deleteCascade takes
// (whole hierarchy, then entities) — so the two can never deadlock. The
// first folder_id read is unlocked; after the folder row is locked the
// entity row is locked and membership is re-verified. A folder change in
// between fails closed with ErrLocked rather than chasing the new folder
// (which could invert the lock order); the caller may simply retry.
func CheckEntityFolderTx(ctx context.Context, tx pgx.Tx, table string, uid authctx.UserID, entityID int64, secret []byte, token string) error {
	switch table {
	case "link", "note":
	default:
		return fmt.Errorf("folder gate: unknown entity table %q", table)
	}
	folderID, err := entityFolderID(ctx, tx, table, uid, entityID, false)
	if err != nil {
		return err
	}
	var passwordHash *string
	if folderID != nil {
		err = tx.QueryRow(ctx,
			`SELECT password_hash FROM folder WHERE user_id = $1 AND id = $2 FOR NO KEY UPDATE`,
			int64(uid), *folderID).Scan(&passwordHash)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("folder gate: lock folder: %w", err)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			passwordHash = nil
		}
	}
	lockedFolderID, err := entityFolderID(ctx, tx, table, uid, entityID, true)
	if err != nil {
		return err
	}
	if !sameFolder(folderID, lockedFolderID) {
		return ErrLocked
	}
	if lockedFolderID == nil {
		return nil
	}
	return CheckUnlock(secret, *lockedFolderID, passwordHash, token)
}

func entityFolderID(ctx context.Context, tx pgx.Tx, table string, uid authctx.UserID, entityID int64, lock bool) (*int64, error) {
	q := fmt.Sprintf(`SELECT folder_id FROM %s WHERE user_id = $1 AND id = $2`, table)
	if lock {
		q += ` FOR NO KEY UPDATE`
	}
	var folderID *int64
	err := tx.QueryRow(ctx, q, int64(uid), entityID).Scan(&folderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domainerr.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("folder gate: read entity folder: %w", err)
	}
	return folderID, nil
}

func sameFolder(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
