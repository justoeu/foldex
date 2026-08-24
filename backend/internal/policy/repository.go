package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
)

// settingKey is the single app_setting row the whole policy lives in.
//
// One JSON document rather than a key per field: the values are read together
// on every login and written together by one form, and a row-per-field would
// let a partial write leave the instance running half the old policy and half
// the new one.
const settingKey = "instance_policy"

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Get returns the stored policy, or the default when none was ever saved.
//
// A malformed or partial document also yields the default rather than an error.
// This is read on the login path, and an instance whose policy row somehow got
// corrupted must still be able to authenticate its users under the baseline
// rules — failing the login instead would turn a bad settings row into a total
// outage with no way in to fix it.
func (r *Repository) Get(ctx context.Context) (Policy, error) {
	var raw string
	err := r.pool.QueryRow(ctx,
		`SELECT value FROM app_setting WHERE key = $1`, settingKey).Scan(&raw)
	if err == pgx.ErrNoRows {
		return Default(), nil
	}
	if err != nil {
		return Default(), fmt.Errorf("policy get: %w", err)
	}
	p := Default()
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Default(), nil
	}
	if err := p.Validate(); err != nil {
		return Default(), nil
	}
	return p, nil
}

// Set stores a validated policy.
func (r *Repository) Set(ctx context.Context, p Policy) error {
	p = p.WithDefaults()
	if err := p.ValidateForWrite(); err != nil {
		return err
	}
	if p.GoogleAllowedDomains == nil {
		p.GoogleAllowedDomains = []string{}
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("policy encode: %w", err)
	}
	// app_setting is deliberately outside the backup surface in BOTH directions:
	// export never emits it, and restore has read-but-ignored it since snapshot
	// v6. That is what keeps a hand-crafted backup zip from rewriting the
	// instance's password floor or its Google allowlist.
	_, err = r.pool.Exec(ctx, `
		INSERT INTO app_setting (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		settingKey, string(encoded))
	if err != nil {
		return fmt.Errorf("policy set: %w", err)
	}
	return nil
}

// PasswordMinLength implements auth.PolicyReader.
//
// It answers with the compiled-in floor on any read failure rather than
// propagating the error. This is called while validating a password the user is
// currently typing into a form; refusing the whole operation because the
// settings row could not be read would turn a degraded database into an inability
// to change a password, and the caller applies max(floor, this) anyway — so the
// worst case of a failed read is the historical behaviour, never a weaker one.
func (r *Repository) PasswordMinLength(ctx context.Context) int {
	p, err := r.Get(ctx)
	if err != nil {
		return MinPasswordFloor
	}
	return p.PasswordMinLength
}

// GoogleAllows implements auth.PolicyReader.
//
// A read failure denies. This gate decides who may CREATE access, so the safe
// direction is the closed one: a degraded database must not become an open
// door. That is the opposite of PasswordMinLength above, where the safe
// direction is to keep working under the historical floor.
func (r *Repository) GoogleAllows(ctx context.Context, email string) bool {
	p, err := r.Get(ctx)
	if err != nil {
		return false
	}
	return p.AllowsEmail(email)
}

// GoogleProvisioning implements auth.PolicyReader. A read failure disables it,
// for the same reason GoogleAllows denies.
func (r *Repository) GoogleProvisioning(ctx context.Context) (bool, authctx.Role) {
	p, err := r.Get(ctx)
	if err != nil {
		return false, authctx.RoleViewer
	}
	return p.GoogleAutoProvision, p.GoogleDefaultRole
}

// OTPTTL implements auth.PolicyReader. A read failure yields the default, and
// the caller clamps to the compiled-in floor either way.
func (r *Repository) OTPTTL(ctx context.Context) time.Duration {
	p, err := r.Get(ctx)
	if err != nil {
		return time.Duration(Default().OTPTTLMinutes) * time.Minute
	}
	return time.Duration(p.OTPTTLMinutes) * time.Minute
}

// RequiresTOTPForAdmins implements auth.AdminFactorPolicy.
//
// A read failure yields FALSE — the permissive floor — and that direction is
// deliberate. Every other reader here fails toward the strict answer, because
// there the strict answer refuses an action. Here it would refuse the
// administration surface itself, so a transient database hiccup would lock the
// owner out of the very screen that configures this, with no way back in.
func (r *Repository) RequiresTOTPForAdmins(ctx context.Context) bool {
	p, err := r.Get(ctx)
	if err != nil {
		return false
	}
	return p.RequiresTOTPForAdmins()
}

// OTPResendCooldown implements auth.PolicyReader.
func (r *Repository) OTPResendCooldown(ctx context.Context) time.Duration {
	p, err := r.Get(ctx)
	if err != nil {
		return time.Duration(Default().OTPCooldownSeconds) * time.Second
	}
	return time.Duration(p.OTPCooldownSeconds) * time.Second
}

// WarnUnenforceableFloor logs once at boot when the stored password floor is
// one no password can satisfy.
//
// Get honours the stored value rather than clamping it (see
// maxStoredPasswordFloor), so such an instance refuses every password with
// password_too_short and nothing anywhere says why. The condition can only
// exist on an instance configured before the write bound tightened; the fix is
// to save a floor at or below the bound, and this is the only line that tells
// the operator to.
func (r *Repository) WarnUnenforceableFloor(ctx context.Context, log *slog.Logger) {
	p, err := r.Get(ctx)
	if err != nil || p.PasswordMinLength <= MaxPasswordFloor {
		return
	}
	log.Warn("instance password floor cannot be satisfied by any password",
		"configured", p.PasswordMinLength,
		"max_enforceable", MaxPasswordFloor,
		"remedy", "lower it in Settings > Administration > Policy")
}
