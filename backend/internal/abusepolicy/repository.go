package abusepolicy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// settingKey is the single app_setting row this whole policy lives in.
//
// No table of its own: app_setting is the generic key/value store migration
// 000016 introduced for exactly this, and internal/policy already keeps the
// instance policy there. One JSON document rather than a row per knob, for
// internal/policy's reason — the values are read together on every enforcement
// decision and written together by one form, and a row-per-knob would let a
// partial write leave the instance running half the old limits and half the
// new ones.
//
// Living in app_setting also inherits the property that matters most here:
// that table is outside the backup surface in BOTH directions. streamSnapshotJSON
// emits a literal `"app_settings":null` and never queries the table, restore has
// read-but-IGNORED the field since snapshot v6, and the per-user wipe does not
// touch it. A hand-crafted ZIP therefore cannot rewrite this instance's rate
// limits — the same guarantee INV-048 relies on for the password floor.
const settingKey = "abuse_policy"

// Repository persists the policy. It is the write side of the Reader seam the
// enforcement sites depend on.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Get returns the stored policy, sanitised, or the defaults when none was saved.
//
// It NEVER answers a missing or unreadable document with an error, for
// internal/policy's reason applied to a hotter path: this is read on the login
// path and by the quota middleware, so a settings row that somehow got
// corrupted must not become a total outage — the instance still has to be able
// to authenticate the person who would repair it.
//
// The degradation is per FIELD, which is where this parts company with
// internal/policy. A document that does not parse at all falls back to the
// whole default, because there is nothing else to keep. A document that PARSES
// but carries one knob out of range keeps every other knob exactly as written
// and reverts only the offending one — Sanitize's contract. INV-169 records
// what the alternative cost: tightening one bound made an instance configured
// above it silently lose unrelated settings too, and a rule getting stricter
// must never be the thing that switches the other rules off.
func (r *Repository) Get(ctx context.Context) (Policy, error) {
	var raw string
	err := r.pool.QueryRow(ctx,
		`SELECT value FROM app_setting WHERE key = $1`, settingKey).Scan(&raw)
	if err == pgx.ErrNoRows {
		return Default(), nil
	}
	if err != nil {
		return Default(), fmt.Errorf("abuse policy get: %w", err)
	}
	p := Default()
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Default(), nil
	}
	return p.Sanitize(), nil
}

// Set stores a policy the owner is SAVING.
//
// ValidateForWrite refuses rather than clamps: a clamped value would show the
// owner a screen that disagrees with what they typed and never say why. The
// error is returned unwrapped so the handler can hand its message to the form
// verbatim — it names the field and the real bounds, which are documented
// limits rather than secrets.
func (r *Repository) Set(ctx context.Context, p Policy) error {
	if err := p.ValidateForWrite(); err != nil {
		return err
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("abuse policy encode: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO app_setting (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		settingKey, string(encoded))
	if err != nil {
		return fmt.Errorf("abuse policy set: %w", err)
	}
	return nil
}
