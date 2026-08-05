// Package settings owns the per-user master recovery password — see ADR-29 for
// what it does and ADR-30 for why it moved.
//
// Until migration 000017 the master password was a singleton in the generic
// app_setting key/value table. That was correct while foldex had one user, but
// in a multi-tenant install a global master would let any admin clear ANOTHER
// tenant's folder passwords — exactly the bypass ADR-28 refused to build. It
// now lives on app_user, one per account.
//
// app_setting survives as a table for genuinely instance-wide configuration;
// this package no longer writes to it.
//
// The plaintext master password is never stored or logged; only its bcrypt hash.
package settings

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/pwhash"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// masterHash returns the stored bcrypt hash for uid, or ("", false) when the
// user has no master password configured.
func (r *Repository) masterHash(ctx context.Context, uid authctx.UserID) (string, bool, error) {
	var hash *string
	err := r.pool.QueryRow(ctx,
		`SELECT master_password_hash FROM app_user WHERE id = $1`, int64(uid)).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read master password hash: %w", err)
	}
	if hash == nil {
		return "", false, nil
	}
	return *hash, true, nil
}

// MasterPasswordConfigured reports whether uid has a master password set.
func (r *Repository) MasterPasswordConfigured(ctx context.Context, uid authctx.UserID) (bool, error) {
	_, ok, err := r.masterHash(ctx, uid)
	return ok, err
}

// MasterPasswordHint returns the stored non-secret reminder phrase, or nil when
// none is set.
func (r *Repository) MasterPasswordHint(ctx context.Context, uid authctx.UserID) (*string, error) {
	var hint *string
	err := r.pool.QueryRow(ctx,
		`SELECT master_password_hint FROM app_user WHERE id = $1`, int64(uid)).Scan(&hint)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read master password hint: %w", err)
	}
	return hint, nil
}

// SetMasterPassword hashes and stores the master password for uid, applying the
// optional reminder hint in the SAME statement (so the pair never drifts). The
// plaintext is bcrypt-hashed here and discarded. hint is TRI-STATE:
//   - nil            → leave the existing hint untouched (a password change with
//     an empty hint field must NOT silently wipe the reminder)
//   - non-nil, ""    → clear the hint
//   - non-nil, "x"   → set/replace the hint (stored verbatim, never hashed)
func (r *Repository) SetMasterPassword(ctx context.Context, uid authctx.UserID, plain string, hint *string) error {
	hash, err := pwhash.Hash(plain)
	if err != nil {
		return fmt.Errorf("hash master password: %w", err)
	}
	// Both columns live on the same row, so the tri-state collapses into one
	// UPDATE: COALESCE keeps the current hint when hint is nil, and NULLIF maps
	// the explicit "" to NULL.
	ct, err := r.pool.Exec(ctx, `
        UPDATE app_user
        SET master_password_hash = $2,
            master_password_hint = CASE
                WHEN $3::text IS NULL THEN master_password_hint
                ELSE NULLIF($3::text, '')
            END,
            updated_at = now()
        WHERE id = $1
    `, int64(uid), hash, hint)
	if err != nil {
		return fmt.Errorf("set master password: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("set master password: user %d not found", int64(uid))
	}
	return nil
}

// ClearMasterPassword removes the master password AND its hint for uid
// (recovery disabled — a hint for a nonexistent password is dead data).
func (r *Repository) ClearMasterPassword(ctx context.Context, uid authctx.UserID) error {
	if _, err := r.pool.Exec(ctx, `
        UPDATE app_user
        SET master_password_hash = NULL, master_password_hint = NULL, updated_at = now()
        WHERE id = $1
    `, int64(uid)); err != nil {
		return fmt.Errorf("clear master password: %w", err)
	}
	return nil
}

// VerifyMaster reports whether plain matches uid's master password. configured
// is false when none is set — the caller distinguishes "wrong password"
// (configured=true, ok=false) from "no master configured" (configured=false) to
// return the right error. Satisfies folders.MasterPasswordVerifier.
func (r *Repository) VerifyMaster(ctx context.Context, uid authctx.UserID, plain string) (ok bool, configured bool, err error) {
	hash, present, err := r.masterHash(ctx, uid)
	if err != nil {
		return false, false, err
	}
	if !present {
		return false, false, nil
	}
	return pwhash.Verify(hash, plain), true, nil
}
