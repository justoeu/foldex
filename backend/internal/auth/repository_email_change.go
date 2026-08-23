package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/pgerr"
	"foldex/internal/pkg/secrets"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrUsernameTaken is the unique-index violation on username_normalized,
	// surfaced as a semantic error so the handler can answer 409 instead of the
	// 500 a raw pgx error would become.
	ErrUsernameTaken = errors.New("auth: username already taken")

	// ErrEmailUnchanged is the request that asks for the address the account
	// already has. Refused rather than treated as success: answering 202 would
	// promise a confirmation e-mail that is never sent.
	ErrEmailUnchanged = errors.New("auth: email unchanged")

	// ErrEmailChangeInvalid is the ONE answer for every unusable confirmation —
	// unknown, expired, spent, or minted in a credential epoch that has since
	// been bumped. Distinguishing them would let an unauthenticated caller probe
	// which tokens ever existed.
	ErrEmailChangeInvalid = errors.New("auth: email change token invalid")
)

// PendingEmailChange is what the profile screen needs to say "we sent a link to
// X, your current address still works". It deliberately carries no token.
type PendingEmailChange struct {
	NewEmail  string    `json:"new_email"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RequestEmailChange records a pending move to a new address and queues both
// messages in the SAME transaction as the row.
//
// Two messages, to two different mailboxes, and both are required:
//
//   - the NEW address gets the confirmation link. It is the only thing that can
//     complete the change, which is what stops a typo from becoming the
//     account's login and recovery channel.
//   - the OLD address gets a notice with NO link. Someone reading it who did
//     not ask is being taken over, and the address they still control is the
//     only channel left to tell them. A link there would be an excellent
//     phishing template, which is why `session_revoked` carries none either.
//
// The caller has already proven the current password; this method does not
// re-check it, but it does lock the account row and bind the request to the
// live credential epoch, so a password changed between the proof and the commit
// invalidates the request rather than racing it.
func (r *Repository) RequestEmailChange(ctx context.Context, uid authctx.UserID,
	sessionID int64, newEmail string, ttl time.Duration,
	confirm func(storedLocale string) MailDraft,
	notice func(oldEmail, storedLocale string) MailDraft) (PendingEmailChange, error) {

	norm := NormalizeEmail(newEmail)
	raw, hash, err := secrets.NewToken()
	if err != nil {
		return PendingEmailChange{}, fmt.Errorf("email change token: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return PendingEmailChange{}, fmt.Errorf("email change begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentEmail, currentNorm, status, storedLocale string
	var tokenVersion int
	if err := tx.QueryRow(ctx, `
		SELECT email, email_normalized, status, token_version, coalesce(locale, '')
		FROM app_user WHERE id = $1
		FOR NO KEY UPDATE`, int64(uid)).Scan(
		&currentEmail, &currentNorm, &status, &tokenVersion, &storedLocale); err != nil {
		return PendingEmailChange{}, fmt.Errorf("email change lock user: %w", err)
	}
	if status != StatusActive {
		return PendingEmailChange{}, ErrEmailChangeInvalid
	}
	if norm == currentNorm {
		return PendingEmailChange{}, ErrEmailUnchanged
	}

	// Checked here so the caller gets a usable answer, and checked AGAIN at
	// consumption because the address can be claimed in between — a race this
	// one cannot close and the unique index can.
	var taken bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app_user WHERE email_normalized = $1)`, norm).Scan(&taken); err != nil {
		return PendingEmailChange{}, fmt.Errorf("email change taken check: %w", err)
	}
	if taken {
		return PendingEmailChange{}, ErrEmailTaken
	}

	// Superseding keeps exactly one live link. Asking twice with two different
	// addresses would otherwise leave two mailboxes each able to take the
	// account, and the owner has only seen the second one.
	if _, err := tx.Exec(ctx, `
		UPDATE email_change SET consumed_at = now()
		WHERE user_id = $1 AND consumed_at IS NULL`, int64(uid)); err != nil {
		return PendingEmailChange{}, fmt.Errorf("supersede email changes: %w", err)
	}

	var expires time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO email_change
			(user_id, new_email, new_email_normalized, token_hash, token_version,
			 session_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, now() + $7::interval)
		RETURNING expires_at`,
		int64(uid), newEmail, norm, hash, tokenVersion,
		nullSessionID(sessionID), intervalArg(ttl)).Scan(&expires); err != nil {
		return PendingEmailChange{}, fmt.Errorf("insert email change: %w", err)
	}

	if err := r.enqueueDraft(ctx, tx, confirm(storedLocale), raw); err != nil {
		return PendingEmailChange{}, fmt.Errorf("queue email change confirm: %w", err)
	}
	// The notice carries no token; `Build` ignores the argument.
	if err := r.enqueueDraft(ctx, tx, notice(currentEmail, storedLocale), ""); err != nil {
		return PendingEmailChange{}, fmt.Errorf("queue email change notice: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return PendingEmailChange{}, fmt.Errorf("email change commit: %w", err)
	}
	return PendingEmailChange{NewEmail: newEmail, ExpiresAt: expires}, nil
}

// ConsumeEmailChange spends the token and moves the address, in one transaction.
//
// Everything that has to be true at COMMIT is checked under the account's row
// lock, not at request time: the epoch still matches, the address is still free,
// the row is still unspent. The uniqueness re-check is the one that matters —
// between the request and the click, somebody else can have taken the address,
// and without it the move would either fail as a 500 or, worse, succeed against
// a stale reading.
//
// It bumps `token_version` and revokes EVERY session, the current one included.
// The login identifier just changed; a session issued against the old one is a
// credential for an account that no longer answers to that name, and the person
// clicking the link may be on a device that never signed in at all.
func (r *Repository) ConsumeEmailChange(ctx context.Context, tokenHash []byte) (User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("consume email change begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id, userID int64
	var newEmail, newNorm string
	var rowVersion int
	var sessionID *int64
	err = tx.QueryRow(ctx, `
		SELECT id, user_id, new_email, new_email_normalized, token_version, session_id
		FROM email_change
		WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
		FOR UPDATE`, tokenHash).Scan(&id, &userID, &newEmail, &newNorm, &rowVersion, &sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrEmailChangeInvalid
	}
	if err != nil {
		return User{}, fmt.Errorf("load email change: %w", err)
	}

	var liveVersion int
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT token_version, status FROM app_user WHERE id = $1
		FOR NO KEY UPDATE`, userID).Scan(&liveVersion, &status); err != nil {
		return User{}, fmt.Errorf("email change lock target: %w", err)
	}
	// Fails closed on a password change, a reset or a logout-all since the
	// request — the same epoch binding every other credential proof carries.
	if liveVersion != rowVersion || status != StatusActive {
		return User{}, ErrEmailChangeInvalid
	}

	// And on the ONE session that proved the password, which the epoch alone
	// does not cover: revoking a single session does not bump `token_version`.
	// Someone who spots a strange device in their session list and revokes just
	// that one — the proportionate response, since they still trust their
	// password — would otherwise leave the pending move alive for whoever was
	// on it. Same binding `oauth_state` already carries for linking an identity.
	if sessionID != nil {
		var live bool
		if err := tx.QueryRow(ctx,
			`SELECT revoked_at IS NULL FROM session WHERE id = $1`, *sessionID).Scan(&live); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return User{}, ErrEmailChangeInvalid
			}
			return User{}, fmt.Errorf("email change session check: %w", err)
		}
		if !live {
			return User{}, ErrEmailChangeInvalid
		}
	}

	// Spending the token and moving the address are ONE statement for the same
	// reason e-mail verification is: split in two, a failure between them burns
	// the token while the address stays put, and the only way to get another is
	// a flow the user may no longer be able to reach.
	u, err := scanUser(tx.QueryRow(ctx, `
		WITH spent AS (
			UPDATE email_change SET consumed_at = now() WHERE id = $1 RETURNING user_id
		)
		UPDATE app_user SET
			email             = $2,
			email_normalized  = $3,
			-- Following the link IS the proof of control, so the new address
			-- arrives verified. Anything else would immediately ask the user to
			-- prove the thing they just proved.
			email_verified_at = now(),
			token_version     = token_version + 1,
			updated_at        = now()
		FROM spent
		WHERE app_user.id = spent.user_id
		RETURNING `+userColumns, id, newEmail, newNorm))
	if pgerr.UniqueConstraint(err) == "app_user_email_norm_uniq" {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("apply email change: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE session SET revoked_at = now(), revoked_reason = $2
		WHERE user_id = $1 AND revoked_at IS NULL`, userID, ReasonEmailChanged); err != nil {
		return User{}, fmt.Errorf("revoke sessions on email change: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("consume email change commit: %w", err)
	}
	return u, nil
}

// PendingEmailChangeFor returns the caller's live request, if any.
//
// Owner-scoped by parameter like every other repository method: the id is what
// the profile screen is allowed to read, and there is no path that asks for
// somebody else's.
func (r *Repository) PendingEmailChangeFor(ctx context.Context, uid authctx.UserID) (*PendingEmailChange, error) {
	var p PendingEmailChange
	err := r.pool.QueryRow(ctx, `
		SELECT new_email, expires_at FROM email_change
		WHERE user_id = $1 AND consumed_at IS NULL AND expires_at > now()`,
		int64(uid)).Scan(&p.NewEmail, &p.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pending email change: %w", err)
	}
	return &p, nil
}

// CancelEmailChange drops the caller's pending request.
func (r *Repository) CancelEmailChange(ctx context.Context, uid authctx.UserID) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE email_change SET consumed_at = now()
		WHERE user_id = $1 AND consumed_at IS NULL`, int64(uid)); err != nil {
		return fmt.Errorf("cancel email change: %w", err)
	}
	return nil
}

// nullSessionID keeps the FK honest for a caller with no session row, which the
// admin surfaces do not have.
func nullSessionID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
