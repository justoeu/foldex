// Package settings owns app-level, UI-mutable configuration stored in the
// generic app_setting key/value table (migration 000016). Its first key is
// the master recovery password hash — see ADR-29. The plaintext master
// password is never stored or logged; only its bcrypt hash lives here.
package settings

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/pwhash"
)

// masterPasswordKey is the app_setting.key under which the bcrypt hash of the
// master recovery password lives. masterPasswordHintKey holds the optional,
// NON-secret reminder phrase (ADR-29) shown to help recall the master — like
// folder.password_hint, it is returned verbatim, never hashed.
const (
	masterPasswordKey     = "master_password_hash"
	masterPasswordHintKey = "master_password_hint"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// masterHash returns the stored bcrypt hash of the master password, or ("",
// false) when none is configured.
func (r *Repository) masterHash(ctx context.Context) (string, bool, error) {
	var hash string
	err := r.pool.QueryRow(ctx, `SELECT value FROM app_setting WHERE key = $1`, masterPasswordKey).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read master password hash: %w", err)
	}
	return hash, true, nil
}

// MasterPasswordConfigured reports whether a master password is set.
func (r *Repository) MasterPasswordConfigured(ctx context.Context) (bool, error) {
	_, ok, err := r.masterHash(ctx)
	return ok, err
}

// MasterPasswordHint returns the stored non-secret reminder phrase, or nil when
// none is set (or no master is configured).
func (r *Repository) MasterPasswordHint(ctx context.Context) (*string, error) {
	var hint string
	err := r.pool.QueryRow(ctx, `SELECT value FROM app_setting WHERE key = $1`, masterPasswordHintKey).Scan(&hint)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read master password hint: %w", err)
	}
	return &hint, nil
}

// SetMasterPassword hashes and upserts the master password, and applies the
// optional reminder hint in the SAME transaction (so the pair never drifts). The
// plaintext is bcrypt-hashed here and discarded. hint is TRI-STATE:
//   - nil            → leave the existing hint untouched (a password change with
//     an empty hint field must NOT silently wipe the reminder)
//   - non-nil, ""    → clear the hint
//   - non-nil, "x"   → set/replace the hint (stored verbatim, never hashed)
func (r *Repository) SetMasterPassword(ctx context.Context, plain string, hint *string) error {
	hash, err := pwhash.Hash(plain)
	if err != nil {
		return fmt.Errorf("hash master password: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin set master tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, `
        INSERT INTO app_setting (key, value, updated_at)
        VALUES ($1, $2, now())
        ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
    `, masterPasswordKey, hash); err != nil {
		return fmt.Errorf("upsert master password: %w", err)
	}
	if hint != nil {
		if *hint == "" {
			if _, err = tx.Exec(ctx, `DELETE FROM app_setting WHERE key = $1`, masterPasswordHintKey); err != nil {
				return fmt.Errorf("clear master password hint: %w", err)
			}
		} else if _, err = tx.Exec(ctx, `
            INSERT INTO app_setting (key, value, updated_at)
            VALUES ($1, $2, now())
            ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
        `, masterPasswordHintKey, *hint); err != nil {
			return fmt.Errorf("upsert master password hint: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ClearMasterPassword removes the master password AND its hint (recovery
// disabled — a hint for a nonexistent password is dead data).
func (r *Repository) ClearMasterPassword(ctx context.Context) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM app_setting WHERE key = ANY($1)`,
		[]string{masterPasswordKey, masterPasswordHintKey}); err != nil {
		return fmt.Errorf("clear master password: %w", err)
	}
	return nil
}

// VerifyMaster reports whether plain matches the configured master password.
// configured is false when no master password is set — the caller distinguishes
// "wrong password" (configured=true, ok=false) from "no master configured"
// (configured=false) to return the right error. Satisfies the
// folders.MasterPasswordVerifier interface consumed by the folder reset route.
func (r *Repository) VerifyMaster(ctx context.Context, plain string) (ok bool, configured bool, err error) {
	hash, present, err := r.masterHash(ctx)
	if err != nil {
		return false, false, err
	}
	if !present {
		return false, false, nil
	}
	return pwhash.Verify(hash, plain), true, nil
}
