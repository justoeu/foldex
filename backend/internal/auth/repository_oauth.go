package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/pgerr"
	"foldex/internal/pkg/pwhash"
	"foldex/internal/pkg/secrets"
)

// OAuth redirect purposes, mirroring oauth_state_purpose_check.
const (
	OAuthPurposeLogin        = "login"
	OAuthPurposeLink         = "link"
	OAuthPurposeAcceptInvite = "accept_invite"
)

// ProviderGoogle is the only provider the schema accepts today.
const ProviderGoogle = "google"

var (
	// ErrOAuthStateInvalid covers unknown, expired and already-spent states
	// alike. One error for all three: a caller who can tell them apart learns
	// whether a state they did not mint ever existed.
	ErrOAuthStateInvalid = errors.New("auth: oauth state invalid")
	// ErrOAuthLinkInvalid covers a stale proof, changed credential epoch or a
	// callback presented by any session other than the one that started linking.
	ErrOAuthLinkInvalid = errors.New("auth: oauth link proof invalid")
	// ErrIdentityTaken means the provider account is already linked elsewhere.
	ErrIdentityTaken = errors.New("auth: provider account is linked to another user")
	// ErrIdentityExists means THIS account already has an identity for the
	// provider. Distinct from ErrIdentityTaken because the remedies differ:
	// unlink first, versus "that Google account belongs to someone else".
	ErrIdentityExists = errors.New("auth: account already has an identity for this provider")
	// ErrIdentityMissing means there is nothing to unlink.
	ErrIdentityMissing = errors.New("auth: no linked provider account")
	// ErrLastCredential refuses an unlink that would leave the account with no
	// way to sign in. The database's constraint trigger (migration 000021) is
	// the real guarantee; this is the readable error the UI shows.
	ErrLastCredential = errors.New("auth: cannot remove the only remaining credential")
)

// Identity is one linked provider account, as the settings screen sees it.
type Identity struct {
	Provider    string     `json:"provider"`
	EmailAtLink string     `json:"email_at_link,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// OAuthState is a consumed redirect state, carrying what the callback needs to
// finish the flow it belongs to.
type OAuthState struct {
	ID       int64
	Provider string
	Purpose  string
	// Verifier is the PKCE code_verifier. It lives server-side and never
	// reaches the browser: the fx_oauth cookie carries only the state, so an
	// attacker who can read the redirect URL still cannot complete an exchange.
	Verifier string
	// UserID is set for purpose=link — the account the session proved, NEVER
	// one derived from the Google profile.
	UserID *authctx.UserID
	// InviteID is set for purpose=accept_invite.
	InviteID *int64
	// SessionID, TokenVersion and ProofAt are all set for purpose=link. Together
	// they bind the recent step-up to one live login and credential epoch.
	SessionID    *int64
	TokenVersion *int
	ProofAt      *time.Time
}

// ─────────────────────────────────────────────────────────────────────
// Redirect state
// ─────────────────────────────────────────────────────────────────────

// CreateOAuthState stores the PKCE verifier and returns the raw state to put in
// the redirect (and in the fx_oauth cookie).
func (r *Repository) CreateOAuthState(ctx context.Context, provider, purpose, verifier string,
	uid *authctx.UserID, inviteID *int64, ttl time.Duration) (string, error) {

	raw, hash, err := secrets.NewToken()
	if err != nil {
		return "", fmt.Errorf("oauth state token: %w", err)
	}
	var owner *int64
	if uid != nil {
		v := int64(*uid)
		owner = &v
	}
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO oauth_state (state_hash, code_verifier, provider, purpose, user_id, invite_id,
		                         expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, now() + $7::interval)`,
		hash, verifier, provider, purpose, owner, inviteID, intervalArg(ttl)); err != nil {
		return "", fmt.Errorf("insert oauth state: %w", err)
	}
	return raw, nil
}

// CreateOAuthLinkState stores a recently stepped-up state only while the exact
// session and password epoch that produced the proof are still current.
func (r *Repository) CreateOAuthLinkState(ctx context.Context, provider, verifier string,
	uid authctx.UserID, sessionID int64, tokenVersion int, ttl time.Duration) (string, error) {

	raw, hash, err := secrets.NewToken()
	if err != nil {
		return "", fmt.Errorf("oauth link state token: %w", err)
	}
	var id int64
	err = r.pool.QueryRow(ctx, `
		INSERT INTO oauth_state (
			state_hash, code_verifier, provider, purpose, user_id,
			session_id, token_version, proof_at, expires_at
		)
		SELECT $1, $2, $3, 'link', u.id, s.id, u.token_version, now(), now() + $7::interval
		FROM session s
		JOIN app_user u ON u.id = s.user_id
		WHERE s.id = $4 AND s.user_id = $5
		  AND s.revoked_at IS NULL AND s.access_expires_at > now()
		  AND u.status = 'active' AND u.token_version = $6
		RETURNING id`, hash, verifier, provider, sessionID, int64(uid), tokenVersion, intervalArg(ttl)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrOAuthLinkInvalid
	}
	if err != nil {
		return "", fmt.Errorf("insert oauth link state: %w", err)
	}
	return raw, nil
}

// ConsumeOAuthState spends a state and returns it.
//
// A conditional UPDATE, not a read-then-write. Two callbacks arriving with the
// same state — the honest one and a replay — must not both succeed, and the
// row lock is what decides that rather than a race between two SELECTs.
func (r *Repository) ConsumeOAuthState(ctx context.Context, rawState string) (OAuthState, error) {
	if rawState == "" {
		return OAuthState{}, ErrOAuthStateInvalid
	}
	var s OAuthState
	var uid *int64
	err := r.pool.QueryRow(ctx, `
		UPDATE oauth_state SET consumed_at = now()
		WHERE state_hash = $1 AND consumed_at IS NULL AND expires_at > now()
		RETURNING id, provider, purpose, code_verifier, user_id, invite_id,
		          session_id, token_version, proof_at`,
		secrets.Hash(rawState)).Scan(&s.ID, &s.Provider, &s.Purpose, &s.Verifier, &uid, &s.InviteID,
		&s.SessionID, &s.TokenVersion, &s.ProofAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthState{}, ErrOAuthStateInvalid
	}
	if err != nil {
		return OAuthState{}, fmt.Errorf("consume oauth state: %w", err)
	}
	if uid != nil {
		v := authctx.UserID(*uid)
		s.UserID = &v
	}
	return s, nil
}

// SweepOAuthState deletes states that are long past use.
//
// Unlike session_used_token, nothing here is a detector: a spent state has no
// forensic value once the flow it belonged to is over, so these rows are
// deleted rather than retained.
func (r *Repository) SweepOAuthState(ctx context.Context, retain time.Duration) (int64, error) {
	ct, err := r.pool.Exec(ctx,
		`DELETE FROM oauth_state WHERE expires_at < now() - $1::interval`, intervalArg(retain))
	if err != nil {
		return 0, fmt.Errorf("sweep oauth state: %w", err)
	}
	return ct.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────
// Identities
// ─────────────────────────────────────────────────────────────────────

// UserByIdentity resolves an account from a provider subject.
//
// This is the ONLY lookup that may log someone in through OAuth. The e-mail is
// deliberately not part of it: a Google account's address can be changed by its
// owner, and matching on it would let that change move a foldex account.
func (r *Repository) UserByIdentity(ctx context.Context, provider, subject string) (User, error) {
	u, err := scanUser(r.pool.QueryRow(ctx, `
		SELECT `+userColumns+`
		FROM app_user
		JOIN user_identity i ON i.user_id = app_user.id
		WHERE i.provider = $1 AND i.subject = $2`, provider, subject))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNoUser
	}
	if err != nil {
		return User{}, fmt.Errorf("user by identity: %w", err)
	}
	return u, nil
}

// UserByEmail resolves an account by normalized address.
//
// Used only to decide whether an unknown Google subject should be offered the
// CONVERSION flow — never to log anyone in. The distinction is the whole
// anti-takeover argument: a matching address opens a door that still demands
// the account's current password.
func (r *Repository) UserByEmail(ctx context.Context, email string) (User, error) {
	u, err := scanUser(r.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM app_user WHERE email_normalized = $1`, NormalizeEmail(email)))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNoUser
	}
	if err != nil {
		return User{}, fmt.Errorf("user by email: %w", err)
	}
	return u, nil
}

// ListIdentities returns the providers linked to one account.
func (r *Repository) ListIdentities(ctx context.Context, uid authctx.UserID) ([]Identity, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT provider, COALESCE(email_at_link, ''), created_at, last_login_at
		FROM user_identity WHERE user_id = $1 ORDER BY provider`, int64(uid))
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}
	defer rows.Close()
	out := []Identity{}
	for rows.Next() {
		var i Identity
		if err := rows.Scan(&i.Provider, &i.EmailAtLink, &i.CreatedAt, &i.LastLoginAt); err != nil {
			return nil, fmt.Errorf("scan identity: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ValidateOAuthLinkProof rejects a callback before the provider exchange when
// its state is no longer backed by the same live session and credential epoch.
// LinkIdentity repeats this check under row locks before writing.
func (r *Repository) ValidateOAuthLinkProof(ctx context.Context, state OAuthState,
	rawAccess string, maxAge time.Duration) error {

	if state.UserID == nil || state.SessionID == nil || state.TokenVersion == nil || state.ProofAt == nil || rawAccess == "" {
		return ErrOAuthLinkInvalid
	}
	var valid bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM session s
			JOIN app_user u ON u.id = s.user_id
			WHERE s.id = $1 AND s.user_id = $2
			  AND s.access_token_hash = $3
			  AND s.revoked_at IS NULL AND s.access_expires_at > now()
			  AND u.status = 'active' AND u.token_version = $4
			  AND $5::timestamptz > now() - $6::interval
		)`, *state.SessionID, int64(*state.UserID), secrets.Hash(rawAccess),
		*state.TokenVersion, *state.ProofAt, intervalArg(maxAge)).Scan(&valid)
	if err != nil {
		return fmt.Errorf("validate oauth link proof: %w", err)
	}
	if !valid {
		return ErrOAuthLinkInvalid
	}
	return nil
}

// LinkIdentity attaches the provider only if the same session, principal,
// credential epoch and recent proof are still valid at the write boundary.
func (r *Repository) LinkIdentity(ctx context.Context, state OAuthState, rawAccess,
	provider, subject, emailAtLink string, maxProofAge time.Duration) error {

	if state.UserID == nil || state.SessionID == nil || state.TokenVersion == nil || state.ProofAt == nil || rawAccess == "" {
		return ErrOAuthLinkInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("link identity begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedUser int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM app_user
		WHERE id = $1 AND status = 'active' AND token_version = $2
		  AND $3::timestamptz > now() - $4::interval
		FOR NO KEY UPDATE`, int64(*state.UserID), *state.TokenVersion,
		*state.ProofAt, intervalArg(maxProofAge)).Scan(&lockedUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrOAuthLinkInvalid
	}
	if err != nil {
		return fmt.Errorf("lock oauth link user: %w", err)
	}

	var lockedSession int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM session
		WHERE id = $1 AND user_id = $2 AND access_token_hash = $3
		  AND revoked_at IS NULL AND access_expires_at > now()
		FOR UPDATE`, *state.SessionID, int64(*state.UserID), secrets.Hash(rawAccess)).Scan(&lockedSession)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrOAuthLinkInvalid
	}
	if err != nil {
		return fmt.Errorf("lock oauth link session: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO user_identity (user_id, provider, subject, email_at_link, last_login_at)
		VALUES ($1, $2, $3, $4, now())`,
		int64(*state.UserID), provider, subject, nullString(emailAtLink))
	if err := mapIdentityConflict(err); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("link identity commit: %w", err)
	}
	return nil
}

// TouchIdentity records a successful sign-in through the provider. Best-effort:
// a failure here must not fail the login, so it is deliberately silent, the
// same treatment TouchSession gets.
func (r *Repository) TouchIdentity(ctx context.Context, provider, subject string) {
	_, _ = r.pool.Exec(ctx,
		`UPDATE user_identity SET last_login_at = now() WHERE provider = $1 AND subject = $2`,
		provider, subject)
}

// ConvertToProvider turns a password account into a provider-only account.
//
// One transaction, and the ordering inside it is the point. Linking the
// identity and retiring the password in two separate transactions would leave
// a window where a crash between them produces either an account with two
// credentials (harmless) or — with the other order — an account with none
// (unrecoverable through any UI). The deferred constraint trigger from
// migration 000021 checks the end state at COMMIT, which is why nulling the
// password before the INSERT would still be safe; doing both here means the
// question never comes up.
//
// Sessions are revoked because the credential set changed: anyone holding a
// session minted against the old password should have to present the new proof.
// The challenge is consumed in the same transaction so a replayed convert
// request cannot re-run any of it.
func (r *Repository) ConvertToProvider(ctx context.Context, challengeID int64,
	password string) (User, string, error) {
	var uid int64
	var challengeVersion int
	var provedHash *string
	err := r.pool.QueryRow(ctx, `
		SELECT c.user_id, c.token_version, u.password_hash
		FROM auth_challenge c
		JOIN app_user u ON u.id = c.user_id
		WHERE c.id = $1 AND c.purpose = 'convert_google'
		  AND c.consumed_at IS NULL AND c.expires_at > now()
		  AND c.token_version = u.token_version AND u.status = 'active'`, challengeID).
		Scan(&uid, &challengeVersion, &provedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrChallengeInvalid
	}
	if err != nil {
		return User{}, "", fmt.Errorf("load convert proof: %w", err)
	}
	if provedHash == nil || !pwhash.Verify(*provedHash, password) {
		return User{}, "", ErrBadCredentials
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, "", fmt.Errorf("convert begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var liveVersion int
	var passwordHash *string
	var status string
	err = tx.QueryRow(ctx, `
		SELECT password_hash, token_version, status
		FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, uid).
		Scan(&passwordHash, &liveVersion, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrChallengeInvalid
	}
	if err != nil {
		return User{}, "", fmt.Errorf("lock convert user: %w", err)
	}
	if status != StatusActive || liveVersion != challengeVersion || passwordHash == nil || *passwordHash != *provedHash {
		return User{}, "", ErrChallengeInvalid
	}

	var provider, subject, emailAtLink string
	err = tx.QueryRow(ctx, `
		UPDATE auth_challenge SET consumed_at = now()
		WHERE id = $1 AND user_id = $2 AND purpose = 'convert_google'
		  AND token_version = $3 AND consumed_at IS NULL AND expires_at > now()
		RETURNING COALESCE(oauth_provider, ''), COALESCE(oauth_subject, ''),
		          COALESCE(oauth_email, '')`, challengeID, uid, challengeVersion).
		Scan(&provider, &subject, &emailAtLink)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrChallengeInvalid
	}
	if err != nil {
		return User{}, "", fmt.Errorf("convert consume challenge: %w", err)
	}
	if provider == "" || subject == "" {
		return User{}, "", ErrChallengeInvalid
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO user_identity (user_id, provider, subject, email_at_link, last_login_at)
		VALUES ($1, $2, $3, $4, now())`,
		uid, provider, subject, nullString(emailAtLink)); err != nil {
		return User{}, "", mapIdentityConflict(err)
	}

	u, err := scanUser(tx.QueryRow(ctx, `
		UPDATE app_user SET password_hash = NULL, token_version = token_version + 1,
		                    updated_at = now()
		WHERE id = $1 AND status = 'active' AND token_version = $2 AND password_hash = $3
		RETURNING `+userColumns, uid, challengeVersion, *passwordHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrChallengeInvalid
	}
	if err != nil {
		return User{}, "", fmt.Errorf("retire password: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE session SET revoked_at = now(), revoked_reason = $2
		WHERE user_id = $1 AND revoked_at IS NULL`, uid, ReasonPasswordChanged); err != nil {
		return User{}, "", fmt.Errorf("convert revoke: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return User{}, "", fmt.Errorf("convert commit: %w", err)
	}
	return u, emailAtLink, nil
}

// UnlinkIdentity detaches a provider, refusing to strip the last credential.
//
// The check is inside the transaction that deletes, so a concurrent password
// removal cannot slip between "you still have a password" and the DELETE. The
// constraint trigger would catch that too, but as a 500 rather than as the
// 409 the UI can explain.
func (r *Repository) UnlinkIdentity(ctx context.Context, uid authctx.UserID, keepSession int64,
	tokenVersion int, provider, password string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("unlink begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var passwordHash *string
	var liveVersion int
	var status string
	var identities int
	if err := tx.QueryRow(ctx, `
		SELECT password_hash, token_version, status,
		       (SELECT count(*) FROM user_identity i WHERE i.user_id = app_user.id)
		FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(uid)).
		Scan(&passwordHash, &liveVersion, &status, &identities); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoUser
		}
		return fmt.Errorf("unlink lookup: %w", err)
	}
	if status != StatusActive || liveVersion != tokenVersion {
		return ErrSessionInvalid
	}
	if err := requireLiveSessionTx(ctx, tx, uid, keepSession); err != nil {
		return err
	}
	if identities == 0 {
		return ErrIdentityMissing
	}
	if passwordHash == nil {
		return ErrLastCredential
	}
	if !pwhash.Verify(*passwordHash, password) {
		return ErrBadCredentials
	}

	ct, err := tx.Exec(ctx,
		`DELETE FROM user_identity WHERE user_id = $1 AND provider = $2`, int64(uid), provider)
	if err != nil {
		return fmt.Errorf("unlink identity: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrIdentityMissing
	}
	if _, err := tx.Exec(ctx, `
		UPDATE app_user SET token_version = token_version + 1, updated_at = now()
		WHERE id = $1 AND token_version = $2`, int64(uid), tokenVersion); err != nil {
		return fmt.Errorf("unlink bump epoch: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE session SET revoked_at = now(), revoked_reason = $3
		WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`,
		int64(uid), keepSession, ReasonPasswordChanged); err != nil {
		return fmt.Errorf("unlink revoke sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("unlink commit: %w", err)
	}
	return nil
}

// mapIdentityConflict turns the two unique indexes on user_identity into
// distinguishable errors.
//
// Matching on the constraint NAME rather than on the message: a wrapped error
// whose chain drops Unwrap would break a string match silently, and the two
// cases mean different things to the person reading the response.
func mapIdentityConflict(err error) error {
	switch pgerr.UniqueConstraint(err) {
	case "":
		if err != nil {
			return fmt.Errorf("link identity: %w", err)
		}
		return nil
	case "user_identity_provider_subject_uniq":
		return ErrIdentityTaken
	case "user_identity_user_provider_uniq":
		return ErrIdentityExists
	default:
		return fmt.Errorf("link identity: %w", err)
	}
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ProvisionOAuthUser creates an account for a verified Google address that has
// none, and links the identity in the same transaction — ADR-35.
//
// One transaction is mandatory, not tidy. Migration 000021's deferred
// constraint trigger requires an ACTIVE account to hold at least one
// credential, and this account will never hold a password: the row and its
// identity are only jointly legal, and splitting them would either fail at
// COMMIT or leave a credential-less active account behind.
//
// The e-mail is recorded as verified because Google asserted it — the caller
// has already refused an unverified address, which is what makes that claim
// worth anything.
func (r *Repository) ProvisionOAuthUser(ctx context.Context, email, name, provider, subject string,
	role authctx.Role) (User, error) {

	// Refused here as well as in the handler and in policy.Validate. This is the
	// last gate before the INSERT, and the one that holds even if a future caller
	// forgets: an auto-provisioned account must never arrive administrative.
	if role != authctx.RoleEditor && role != authctx.RoleViewer {
		return User{}, fmt.Errorf("auth: refusing to auto-provision role %q", role)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("provision begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	u, err := scanUser(tx.QueryRow(ctx, `
		INSERT INTO app_user (email, email_normalized, name, role, status, email_verified_at)
		VALUES ($1, $2, $3, $4, 'active', now())
		RETURNING `+userColumns,
		strings.TrimSpace(email), NormalizeEmail(email), strings.TrimSpace(name), string(role)))
	if err != nil {
		// A concurrent callback for the same address loses the unique index here.
		if pgerr.UniqueConstraint(err) == "app_user_email_norm_uniq" {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("provision user: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO user_identity (user_id, provider, subject, email_at_link, last_login_at)
		VALUES ($1, $2, $3, $4, now())`,
		int64(u.ID), provider, subject, nullString(email)); err != nil {
		return User{}, mapIdentityConflict(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("provision commit: %w", err)
	}
	return u, nil
}
