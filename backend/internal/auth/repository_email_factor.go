package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
)

// OTPPurposeEnrollEmail2FA is the purpose of a code that proves a mailbox in
// order to ENROLL it as a factor.
//
// A distinct purpose, not a reuse of login_2fa, and the separation is the whole
// point: ConsumeEmailOTP matches on (user, purpose, hash), so a shared purpose
// would let a code mailed at a login prompt be redeemed to install a new factor
// — and a code mailed to install one be redeemed to sign in.
const OTPPurposeEnrollEmail2FA = "enroll_email_2fa"

// ErrFactorAlreadyConfirmed means the account already holds this factor.
var ErrFactorAlreadyConfirmed = errors.New("auth: factor already confirmed")

// ErrNoPendingFactor means confirmation arrived with nothing to confirm.
var ErrNoPendingFactor = errors.New("auth: no pending factor enrollment")

// StartEmailFactorEnrollment opens a pending e-mail factor and mails its code.
//
// Row, code and message all commit together. Splitting them would produce the
// two failures this design exists to avoid: a pending factor with no code the
// user can ever supply, or a code charged against the cooldown for a message
// that was never queued.
func (r *Repository) StartEmailFactorEnrollment(ctx context.Context, uid authctx.UserID,
	tokenVersion int, sessionID int64, codeHash []byte, ttl, cooldown time.Duration,
	draft MailDraft) error {

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("start email factor begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedUser int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM app_user
		WHERE id = $1 AND status = 'active' AND token_version = $2
		FOR NO KEY UPDATE`, int64(uid), tokenVersion).Scan(&lockedUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrChallengeInvalid
	}
	if err != nil {
		return fmt.Errorf("start email factor lock user: %w", err)
	}
	// Settings enrollment binds the exact session that supplied the password;
	// the pre-auth path has no session and passes 0.
	if sessionID != 0 {
		if err := requireLiveSessionTx(ctx, tx, uid, sessionID); err != nil {
			return err
		}
	}

	var recent bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM email_otp
			WHERE user_id = $1 AND purpose = $2 AND created_at >= now() - $3::interval
		)`, int64(uid), OTPPurposeEnrollEmail2FA, intervalArg(cooldown)).Scan(&recent); err != nil {
		return fmt.Errorf("email factor cooldown: %w", err)
	}
	if recent {
		return ErrTooSoon
	}

	// The upsert refuses to touch a CONFIRMED row, exactly as TOTP's does:
	// restarting enrollment on a live factor would let anyone holding a session
	// silently replace it.
	ct, err := tx.Exec(ctx, `
		INSERT INTO email_factor (user_id, enrollment_token_version, enrollment_session_id)
		VALUES ($1, $2, NULLIF($3, 0))
		ON CONFLICT (user_id) DO UPDATE
		SET created_at               = now(),
		    confirmed_at             = NULL,
		    enrollment_token_version = EXCLUDED.enrollment_token_version,
		    enrollment_session_id    = EXCLUDED.enrollment_session_id
		WHERE email_factor.confirmed_at IS NULL`,
		int64(uid), tokenVersion, sessionID)
	if err != nil {
		return fmt.Errorf("start email factor: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrFactorAlreadyConfirmed
	}

	// Supersede any outstanding enrollment code. Leaving them live would let a
	// user accumulate valid codes by pressing the button, and each one is a
	// separate chance for an attacker who can read the mailbox.
	if _, err := tx.Exec(ctx, `
		UPDATE email_otp SET consumed_at = now()
		WHERE user_id = $1 AND purpose = $2 AND consumed_at IS NULL`,
		int64(uid), OTPPurposeEnrollEmail2FA); err != nil {
		return fmt.Errorf("supersede email factor codes: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO email_otp (user_id, purpose, code_hash, expires_at)
		VALUES ($1, $2, $3, now() + $4::interval)`,
		int64(uid), OTPPurposeEnrollEmail2FA, codeHash, intervalArg(ttl)); err != nil {
		return fmt.Errorf("insert email factor code: %w", err)
	}
	if err := r.enqueueDraft(ctx, tx, draft, ""); err != nil {
		return fmt.Errorf("queue email factor mail: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("start email factor commit: %w", err)
	}
	return nil
}

// CompleteEmailFactorEnrollment spends the code, confirms the factor, issues
// recovery codes and — on the pre-auth path — consumes the challenge and mints
// the first session, all in ONE transaction.
//
// Recovery codes are MANDATORY here, not a nicety. An account whose only factor
// is e-mail, arriving through a password-reset link, has `mailbox_already_proven`
// set and therefore cannot use the e-mail factor at all — by design, so one
// channel cannot satisfy both steps. Without recovery codes that safety property
// would become a lockout: the user would hold a factor the flow refuses to
// accept and no other way in.
func (r *Repository) CompleteEmailFactorEnrollment(ctx context.Context, uid authctx.UserID,
	tokenVersion int, codeHash []byte, recoveryHashes [][]byte, sessionID int64,
	challenge *Challenge, ttl SessionTTL, ip, ua string) (User, issuedTokens, error) {

	var issue sessionIssue
	var err error
	if challenge != nil {
		issue, err = newSessionIssue(ttl)
		if err != nil {
			return User{}, issuedTokens{}, err
		}
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, issuedTokens{}, fmt.Errorf("complete email factor begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedUser int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM app_user
		WHERE id = $1 AND status = 'active' AND token_version = $2
		FOR NO KEY UPDATE`, int64(uid), tokenVersion).Scan(&lockedUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, issuedTokens{}, ErrChallengeInvalid
	}
	if err != nil {
		return User{}, issuedTokens{}, fmt.Errorf("complete email factor lock user: %w", err)
	}
	if challenge == nil {
		if err := requireLiveSessionTx(ctx, tx, uid, sessionID); err != nil {
			return User{}, issuedTokens{}, err
		}
	}

	// The code is spent by the UPDATE's own WHERE clause, inside this
	// transaction — so a later failure here restores it instead of burning a
	// valid credential for an enrollment that never happened.
	ct, err := tx.Exec(ctx, `
		UPDATE email_otp SET consumed_at = now()
		WHERE user_id = $1 AND purpose = $2 AND code_hash = $3
		  AND consumed_at IS NULL AND expires_at > now()`,
		int64(uid), OTPPurposeEnrollEmail2FA, codeHash)
	if err != nil {
		return User{}, issuedTokens{}, fmt.Errorf("complete email factor consume code: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return User{}, issuedTokens{}, ErrBadCredentials
	}

	// Confirmation requires the SAME epoch — and, from Settings, the same
	// session — that opened the enrollment. A password change between start and
	// confirm must not be able to install a factor.
	ct, err = tx.Exec(ctx, `
		UPDATE email_factor
		SET confirmed_at             = now(),
		    enrollment_token_version = NULL,
		    enrollment_session_id    = NULL
		WHERE user_id = $1 AND confirmed_at IS NULL
		  AND enrollment_token_version = $2
		  AND ($3::bigint = 0 OR enrollment_session_id = $3)`,
		int64(uid), tokenVersion, sessionID)
	if err != nil {
		return User{}, issuedTokens{}, fmt.Errorf("complete email factor confirm: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return User{}, issuedTokens{}, ErrNoPendingFactor
	}

	if err := replaceRecoveryCodesTx(ctx, tx, uid, recoveryHashes); err != nil {
		return User{}, issuedTokens{}, err
	}

	if challenge != nil {
		ct, err := tx.Exec(ctx, `
			UPDATE auth_challenge SET consumed_at = now()
			WHERE id = $1 AND user_id = $2 AND purpose = 'enroll_2fa'
			  AND token_version = $3 AND consumed_at IS NULL AND expires_at > now()`,
			challenge.ID, int64(uid), tokenVersion)
		if err != nil {
			return User{}, issuedTokens{}, fmt.Errorf("complete email factor consume challenge: %w", err)
		}
		if ct.RowsAffected() == 0 {
			return User{}, issuedTokens{}, ErrChallengeInvalid
		}
		if _, err := issueSessionTx(ctx, tx, uid, issue, ip, ua); err != nil {
			return User{}, issuedTokens{}, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET last_login_at = now() WHERE id = $1`, int64(uid)); err != nil {
			return User{}, issuedTokens{}, fmt.Errorf("complete email factor touch user: %w", err)
		}
	}

	user, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM app_user WHERE id = $1`, int64(uid)))
	if err != nil {
		return User{}, issuedTokens{}, fmt.Errorf("complete email factor load user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, issuedTokens{}, fmt.Errorf("complete email factor commit: %w", err)
	}
	return user, issue.tokens, nil
}

// DisableEmailFactor removes the factor, and the recovery codes with it when no
// factor remains.
//
// Recovery codes outlive the factor only while ANOTHER one is still enrolled.
// Keeping them on an account with no second factor would leave a standing set
// of single-use credentials guarding nothing, which is the same reason
// disabling TOTP deletes them.
func (r *Repository) DisableEmailFactor(ctx context.Context, uid authctx.UserID,
	sessionID int64, tokenVersion int, proof SecondFactorProof) error {

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("disable email factor begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedUser int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM app_user
		WHERE id = $1 AND status = 'active' AND token_version = $2
		FOR NO KEY UPDATE`, int64(uid), tokenVersion).Scan(&lockedUser)
	if errors.Is(err, pgx.ErrNoRows) {
		// ErrSessionInvalid, not ErrChallengeInvalid: this path only ever runs
		// with a live session, so a stale epoch here means the credentials moved
		// under the caller — a password changed in another tab. Reporting it as
		// a dead CHALLENGE made the handler answer `challenge_invalid` and clear
		// a pre-auth cookie that this flow never had, which is DisableTOTP's
		// answer to the same situation spelled a different way.
		return ErrSessionInvalid
	}
	if err != nil {
		return fmt.Errorf("disable email factor lock user: %w", err)
	}
	if err := requireLiveSessionTx(ctx, tx, uid, sessionID); err != nil {
		return err
	}
	// Spent HERE, in the transaction that removes the factor. A proof consumed
	// before the write would be gone even when the removal failed, leaving the
	// user to obtain another one to retry an operation that never happened.
	if err := consumeSecondFactorTx(ctx, tx, uid, proof); err != nil {
		return err
	}

	ct, err := tx.Exec(ctx, `DELETE FROM email_factor WHERE user_id = $1`, int64(uid))
	if err != nil {
		return fmt.Errorf("disable email factor: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNoPendingFactor
	}
	// Both purposes: an outstanding step-up code is a live proof issued BY the
	// factor being removed, so leaving it behind would let a mailbox authorize
	// operations after the account stopped accepting that mailbox as a factor.
	if _, err := tx.Exec(ctx, `
		DELETE FROM email_otp WHERE user_id = $1 AND purpose = ANY($2)`,
		int64(uid), []string{OTPPurposeEnrollEmail2FA, OTPPurposeStepUp2FA}); err != nil {
		return fmt.Errorf("disable email factor clear codes: %w", err)
	}

	// Read the remaining factor INSIDE the transaction, under the user lock, so
	// a concurrent TOTP disable cannot leave both paths believing the other
	// factor survives and both keeping the recovery codes.
	var totpLeft bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM totp_secret WHERE user_id = $1 AND confirmed_at IS NOT NULL
		)`, int64(uid)).Scan(&totpLeft); err != nil {
		return fmt.Errorf("disable email factor check totp: %w", err)
	}
	if !totpLeft {
		if _, err := tx.Exec(ctx, `DELETE FROM recovery_code WHERE user_id = $1`, int64(uid)); err != nil {
			return fmt.Errorf("disable email factor clear recovery: %w", err)
		}
	}

	// Removing a factor is a CREDENTIAL-SET mutation, so it bumps the epoch and
	// revokes every other session — exactly as disabling TOTP does. Without it
	// the account that removes its e-mail factor BECAUSE the mailbox was
	// compromised leaves the intruder's other session live, along with any
	// password_reset or auth_challenge bound to the epoch that just stopped
	// being trustworthy. The caller's own session survives, so the user is not
	// signed out of the device they are using.
	if _, err := tx.Exec(ctx, `
		UPDATE app_user SET token_version = token_version + 1, updated_at = now()
		WHERE id = $1 AND token_version = $2`, int64(uid), tokenVersion); err != nil {
		return fmt.Errorf("disable email factor bump epoch: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE session SET revoked_at = now(), revoked_reason = $3
		WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`,
		int64(uid), sessionID, ReasonPasswordChanged); err != nil {
		return fmt.Errorf("disable email factor revoke sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("disable email factor commit: %w", err)
	}
	return nil
}

// OTPPurposeStepUp2FA is the purpose of a code an ENROLLED e-mail factor mails
// to authorize a session-authenticated credential change.
//
// Separate from OTPPurposeEnrollEmail2FA because the two prove different
// things. An enrollment code proves a mailbox the account has not accepted yet;
// a step-up code is that already-accepted factor presenting itself. Sharing a
// purpose would let the first be redeemed as the second, so someone who could
// read the mailbox during a half-finished enrollment would reach the operations
// that exist to require a live factor.
const OTPPurposeStepUp2FA = "step_up_2fa"

// CreateStepUpEmailOTP mails a step-up code for an account whose e-mail factor
// is confirmed.
//
// The `confirmed_at IS NOT NULL` predicate is the load-bearing part and it is
// checked HERE, in the same transaction that writes the code, rather than only
// in the handler. A code minted for an account with no e-mail factor would be a
// mailbox standing in for a factor that was never enrolled — precisely the
// substitution the enrollment requirement exists to prevent.
func (r *Repository) CreateStepUpEmailOTP(ctx context.Context, uid authctx.UserID,
	sessionID int64, tokenVersion int, codeHash []byte, ttl, cooldown time.Duration,
	draft MailDraft) error {

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("step-up otp begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedUser int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM app_user
		WHERE id = $1 AND status = 'active' AND token_version = $2
		FOR NO KEY UPDATE`, int64(uid), tokenVersion).Scan(&lockedUser)
	if errors.Is(err, pgx.ErrNoRows) {
		// Session-only path; see DisableEmailFactor above.
		return ErrSessionInvalid
	}
	if err != nil {
		return fmt.Errorf("step-up otp lock user: %w", err)
	}
	if err := requireLiveSessionTx(ctx, tx, uid, sessionID); err != nil {
		return err
	}

	var enrolled bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM email_factor WHERE user_id = $1 AND confirmed_at IS NOT NULL
		)`, int64(uid)).Scan(&enrolled); err != nil {
		return fmt.Errorf("step-up otp factor check: %w", err)
	}
	if !enrolled {
		return ErrNoPendingFactor
	}

	var recent bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM email_otp
			WHERE user_id = $1 AND purpose = $2 AND created_at >= now() - $3::interval
		)`, int64(uid), OTPPurposeStepUp2FA, intervalArg(cooldown)).Scan(&recent); err != nil {
		return fmt.Errorf("step-up otp cooldown: %w", err)
	}
	if recent {
		return ErrTooSoon
	}

	// Supersede outstanding codes for the same reason enrollment does: pressing
	// the button repeatedly must not accumulate live credentials, each of which
	// is an independent chance for whoever can read the mailbox.
	if _, err := tx.Exec(ctx, `
		UPDATE email_otp SET consumed_at = now()
		WHERE user_id = $1 AND purpose = $2 AND consumed_at IS NULL`,
		int64(uid), OTPPurposeStepUp2FA); err != nil {
		return fmt.Errorf("supersede step-up codes: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO email_otp (user_id, purpose, code_hash, expires_at)
		VALUES ($1, $2, $3, now() + $4::interval)`,
		int64(uid), OTPPurposeStepUp2FA, codeHash, intervalArg(ttl)); err != nil {
		return fmt.Errorf("insert step-up code: %w", err)
	}
	if err := r.enqueueDraft(ctx, tx, draft, ""); err != nil {
		return fmt.Errorf("queue step-up mail: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("step-up otp commit: %w", err)
	}
	return nil
}

// StepUpEmailOTPIsLive reports whether an unspent, unexpired step-up code with
// this digest exists.
//
// A read, not a spend. The UPDATE that consumes it runs in the transaction of
// the operation it authorizes — see SecondFactorProof — so that a failure there
// does not cost the user a code they will now have to request again, past a
// cooldown, to retry an operation that never happened.
func (r *Repository) StepUpEmailOTPIsLive(ctx context.Context, uid authctx.UserID,
	codeHash []byte) (bool, error) {

	var live bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM email_otp
			WHERE user_id = $1 AND purpose = $2 AND code_hash = $3
			  AND consumed_at IS NULL AND expires_at > now()
		)`, int64(uid), OTPPurposeStepUp2FA, codeHash).Scan(&live); err != nil {
		return false, fmt.Errorf("check step-up code: %w", err)
	}
	return live, nil
}

// RecoveryCodeIsLive reports whether an unused recovery code with this digest
// exists. Same read-then-spend-in-tx split as StepUpEmailOTPIsLive.
func (r *Repository) RecoveryCodeIsLive(ctx context.Context, uid authctx.UserID,
	codeHash []byte) (bool, error) {

	var live bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM recovery_code
			WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL
		)`, int64(uid), codeHash).Scan(&live); err != nil {
		return false, fmt.Errorf("check recovery code: %w", err)
	}
	return live, nil
}
