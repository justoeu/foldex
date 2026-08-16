package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/secrets"
)

// APITokenPrefix marks a foldex bearer credential.
//
// A fixed, distinctive prefix is not decoration: it is what makes a leaked
// token findable. Secret scanners, CI log filters and grep all key on a literal
// like this, and an opaque base64 blob is indistinguishable from any other
// base64 blob in a log file.
const APITokenPrefix = "fx_"

// ScopeContent is the only scope issued today: the content API, never the
// identity or backup surfaces.
const ScopeContent = "content"

var (
	// ErrTokenInvalid covers unknown, revoked and expired bearer tokens alike.
	ErrTokenInvalid = errors.New("auth: api token invalid")
	// ErrTokenNotFound means the caller owns no token with that id. Distinct
	// from ErrTokenInvalid, which is about a PRESENTED credential.
	ErrTokenNotFound = errors.New("auth: api token not found")
	// ErrTooManyAPITokens means the per-user live credential cap is full.
	ErrTooManyAPITokens = errors.New("auth: too many api tokens")
)

// maxTokensPerUser bounds how many live tokens one account may hold. This is
// enforced under the owner row lock in CreateAPIToken, not by a handler-side
// count that parallel requests can all pass.
const maxTokensPerUser = 20

// APIToken is one long-lived credential, as its owner sees it.
type APIToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	// Token is populated ONLY by the response that creates it. The server keeps
	// sha256, so it genuinely cannot be shown again.
	Token string `json:"token,omitempty"`
}

// CreateAPIToken mints a bearer credential and returns it in display form.
//
// The value is `fx_<id>_<secret>`. Carrying the id lets resolution be a primary
// key hit rather than a lookup keyed on the secret itself, which matters
// because the secret half is compared in constant time afterwards — an index
// probe on a value an attacker supplies is a timing surface the id sidesteps.
func (r *Repository) CreateAPIToken(ctx context.Context, uid authctx.UserID, name string, ttl time.Duration) (APIToken, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return APIToken{}, fmt.Errorf("create api token begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedUser int64
	if err := tx.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(uid)).Scan(&lockedUser); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APIToken{}, ErrNoUser
		}
		return APIToken{}, fmt.Errorf("create api token lock user: %w", err)
	}
	var existing int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM api_token WHERE user_id = $1 AND revoked_at IS NULL`, int64(uid)).Scan(&existing); err != nil {
		return APIToken{}, fmt.Errorf("create api token count: %w", err)
	}
	if existing >= maxTokensPerUser {
		return APIToken{}, ErrTooManyAPITokens
	}

	raw, hash, err := secrets.NewToken()
	if err != nil {
		return APIToken{}, fmt.Errorf("api token secret: %w", err)
	}
	var expires *time.Time
	if ttl > 0 {
		t := time.Now().Add(ttl)
		expires = &t
	}

	var out APIToken
	err = tx.QueryRow(ctx, `
		INSERT INTO api_token (user_id, name, token_hash, scope, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, name, scope, created_at, last_used_at, expires_at`,
		int64(uid), name, hash, ScopeContent, expires,
	).Scan(&out.ID, &out.Name, &out.Scope, &out.CreatedAt, &out.LastUsedAt, &out.ExpiresAt)
	if err != nil {
		return APIToken{}, fmt.Errorf("create api token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return APIToken{}, fmt.Errorf("create api token commit: %w", err)
	}
	out.Token = APITokenPrefix + strconv.FormatInt(out.ID, 10) + "_" + raw
	return out, nil
}

// ResolveAPIToken turns a presented bearer value into a principal.
//
// The secret is compared with a constant-time equality on the stored sha256,
// never by putting the attacker's value in the WHERE clause. Every failure —
// malformed, unknown id, wrong secret, revoked, expired, owner disabled —
// returns the same ErrTokenInvalid, so probing tells the caller nothing beyond
// "this exact string does not work".
func (r *Repository) ResolveAPIToken(ctx context.Context, presented string) (authctx.Principal, error) {
	id, secret, ok := parseAPIToken(presented)
	if !ok {
		return authctx.Principal{}, ErrTokenInvalid
	}
	var uid int64
	var role authctx.Role
	var stored []byte
	var status string
	err := r.pool.QueryRow(ctx, `
		SELECT t.user_id, u.role, t.token_hash, u.status
		FROM api_token t
		JOIN app_user u ON u.id = t.user_id
		WHERE t.id = $1 AND t.revoked_at IS NULL
		  AND (t.expires_at IS NULL OR t.expires_at > now())`,
		id).Scan(&uid, &role, &stored, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return authctx.Principal{}, ErrTokenInvalid
	}
	if err != nil {
		return authctx.Principal{}, fmt.Errorf("resolve api token: %w", err)
	}
	if !secrets.Equal(secrets.Hash(secret), stored) {
		return authctx.Principal{}, ErrTokenInvalid
	}
	// The account check is here rather than in a second query for the same
	// reason ResolveAccess joins app_user: disabling a user must stop their
	// tokens at the same instant it stops their sessions.
	if status != StatusActive {
		return authctx.Principal{}, ErrTokenInvalid
	}
	return authctx.Principal{
		UserID:  authctx.UserID(uid),
		Role:    role,
		TokenID: id,
		// SessionID stays 0 and Via marks the credential, which is what the CSRF
		// check and the token-rejecting middleware both key on.
		Via: authctx.ViaAPIToken,
	}, nil
}

// TouchAPIToken records use. Best-effort and fire-and-forget, like
// TouchSession: a write failure must not fail the request it belongs to.
func (r *Repository) TouchAPIToken(ctx context.Context, id int64) {
	_, _ = r.pool.Exec(ctx, `UPDATE api_token SET last_used_at = now() WHERE id = $1`, id)
}

// ListAPITokens returns one user's tokens, newest first. The hash is never
// selected: there is nothing an owner could do with it.
func (r *Repository) ListAPITokens(ctx context.Context, uid authctx.UserID) ([]APIToken, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, scope, created_at, last_used_at, expires_at
		FROM api_token
		WHERE user_id = $1 AND revoked_at IS NULL
		ORDER BY created_at DESC`, int64(uid))
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Scope, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan api token: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken kills one token. Scoped by user_id, which is NOT redundant
// with the primary key: without it, any authenticated caller could revoke any
// token by guessing a dense BIGSERIAL id.
func (r *Repository) RevokeAPIToken(ctx context.Context, uid authctx.UserID, id int64) error {
	ct, err := r.pool.Exec(ctx, `
		UPDATE api_token SET revoked_at = now()
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, id, int64(uid))
	if err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrTokenNotFound
	}
	return nil
}

// parseAPIToken splits `fx_<id>_<secret>`.
//
// SplitN with a limit of 2 on the remainder, because the secret is base64url
// and may itself contain nothing that looks like a separator today — but
// splitting greedily would make that an assumption rather than a guarantee.
func parseAPIToken(presented string) (id int64, secret string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(presented), APITokenPrefix)
	if !found {
		return 0, "", false
	}
	parts := strings.SplitN(rest, "_", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, "", false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	return id, parts[1], true
}

// SweepAPITokens deletes tokens long past their end of life.
func (r *Repository) SweepAPITokens(ctx context.Context, retain time.Duration) (int64, error) {
	ct, err := r.pool.Exec(ctx, `
		DELETE FROM api_token
		WHERE (revoked_at IS NOT NULL AND revoked_at < now() - $1::interval)
		   OR (expires_at IS NOT NULL AND expires_at < now() - $1::interval)`, intervalArg(retain))
	if err != nil {
		return 0, fmt.Errorf("sweep api tokens: %w", err)
	}
	return ct.RowsAffected(), nil
}
