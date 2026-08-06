package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/pwhash"
	"foldex/internal/pkg/secrets"
)

// Challenge purposes, mirroring auth_challenge_purpose_check.
const (
	PurposeTOTP          = "totp"
	PurposeEnroll2FA     = "enroll_2fa"
	PurposeConvertGoogle = "convert_google"
)

// E-mail OTP purposes, mirroring email_otp_purpose_check.
const (
	OTPPurposeLogin2FA    = "login_2fa"
	OTPPurposeVerifyEmail = "verify_email"
)

// Budgets that live in the DATABASE rather than in the in-memory limiter.
//
// ADR-28 accepts that a restart clears the folder-unlock counters, because
// bcrypt's per-attempt cost is the real floor there. A second factor has no
// such floor: verifying a 6-digit code is a hash comparison, so a restart that
// zeroed the budget would hand an attacker a fresh set of guesses for the price
// of crashing the process. These counters are columns for that reason.
const (
	maxChallengeAttempts = 5
	maxChallengeSends    = 3
	otpResendInterval    = 60 * time.Second
)

var (
	// ErrChallengeInvalid covers absent, expired, consumed and wrong-purpose
	// challenges as ONE error — a caller holding a bad pre-auth token learns
	// only that it does not work.
	ErrChallengeInvalid = errors.New("auth: challenge invalid")
	// ErrChallengeWrongPurpose marks a LIVE challenge addressed at the wrong
	// endpoint — distinct from ErrChallengeInvalid precisely so the caller does
	// not clear the cookie and destroy a usable credential.
	ErrChallengeWrongPurpose = errors.New("auth: challenge is at a different step")
	// ErrChallengeExhausted marks a challenge whose attempt budget is spent.
	ErrChallengeExhausted = errors.New("auth: challenge attempts exhausted")
	// ErrTooSoon marks a resend inside the cooldown.
	ErrTooSoon = errors.New("auth: too soon")
	// ErrSendsExhausted marks a challenge whose send budget is spent.
	ErrSendsExhausted = errors.New("auth: challenge sends exhausted")
	// ErrNoTOTP marks an account with no enrollment to act on.
	ErrNoTOTP = errors.New("auth: no TOTP secret")
	// ErrResetInvalid covers absent, expired and consumed reset tokens.
	ErrResetInvalid = errors.New("auth: password reset token invalid")
)

// Challenge is the pre-auth state between "password OK" and "second factor OK".
type Challenge struct {
	ID       int64
	UserID   authctx.UserID
	Purpose  string
	Attempts int
	Sends    int
	// MailboxAlreadyProven marks a challenge whose FIRST factor was a
	// password-reset link. The e-mail OTP is refused on those: the code would
	// go to the same inbox the link came from, so both steps would be
	// satisfiable by one compromised mailbox.
	MailboxAlreadyProven bool
}

// ─────────────────────────────────────────────────────────────────────
// Challenges
// ─────────────────────────────────────────────────────────────────────

// CreateChallenge mints a pre-auth token and returns its raw value.
//
// Any earlier live challenge for the same user and purpose is consumed first.
// Without that, a user who retries the password form accumulates challenges,
// and each one carries its own fresh attempt budget — turning the 5-guess cap
// into "5 guesses per password entry", which is no cap at all.
func (r *Repository) CreateChallenge(ctx context.Context, uid authctx.UserID, purpose string, ttl time.Duration, ip, ua string, mailboxAlreadyProven bool) (string, int64, error) {
	raw, hash, err := secrets.NewToken()
	if err != nil {
		return "", 0, fmt.Errorf("challenge token: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("challenge begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE auth_challenge SET consumed_at = now()
		WHERE user_id = $1 AND purpose = $2 AND consumed_at IS NULL`,
		int64(uid), purpose); err != nil {
		return "", 0, fmt.Errorf("supersede challenges: %w", err)
	}

	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO auth_challenge (user_id, token_hash, purpose, expires_at, ip, user_agent,
		                            mailbox_already_proven)
		VALUES ($1, $2, $3, now() + $4::interval, $5, $6, $7)
		RETURNING id`,
		int64(uid), hash, purpose, intervalArg(ttl), nullIP(ip), ua, mailboxAlreadyProven).Scan(&id); err != nil {
		return "", 0, fmt.Errorf("insert challenge: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", 0, fmt.Errorf("challenge commit: %w", err)
	}
	return raw, id, nil
}

// ResolveChallenge turns a raw pre-auth token into its live row.
//
// It refuses a challenge whose attempts are already spent, so an exhausted
// pre-auth token cannot be used to keep probing: the caller gets the same
// answer whether the budget ran out a second ago or the token never existed.
func (r *Repository) ResolveChallenge(ctx context.Context, rawToken string, purposes ...string) (Challenge, error) {
	if rawToken == "" {
		return Challenge{}, ErrChallengeInvalid
	}
	var c Challenge
	var uid int64
	err := r.pool.QueryRow(ctx, `
		SELECT id, user_id, purpose, attempts, sends, mailbox_already_proven
		FROM auth_challenge
		WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()`,
		secrets.Hash(rawToken)).Scan(&c.ID, &uid, &c.Purpose, &c.Attempts, &c.Sends,
		&c.MailboxAlreadyProven)
	if errors.Is(err, pgx.ErrNoRows) {
		return Challenge{}, ErrChallengeInvalid
	}
	if err != nil {
		return Challenge{}, fmt.Errorf("resolve challenge: %w", err)
	}
	c.UserID = authctx.UserID(uid)

	if len(purposes) > 0 {
		match := false
		for _, p := range purposes {
			if c.Purpose == p {
				match = true
				break
			}
		}
		if !match {
			return Challenge{}, ErrChallengeWrongPurpose
		}
	}
	if c.Attempts >= maxChallengeAttempts {
		return Challenge{}, ErrChallengeExhausted
	}
	return c, nil
}

// BumpChallengeAttempt charges one guess and returns the new count.
//
// The increment is done by the database in a single statement, so N parallel
// guesses serialise on the row lock and the cap holds. A read-then-write in Go
// would let every concurrent request observe the same pre-cap value — the same
// class of bug attemptlimit's reserve-then-commit API exists to prevent, solved
// here by the row lock instead of a mutex.
func (r *Repository) BumpChallengeAttempt(ctx context.Context, id int64) (int, error) {
	var attempts int
	err := r.pool.QueryRow(ctx, `
		UPDATE auth_challenge SET attempts = attempts + 1
		WHERE id = $1 AND consumed_at IS NULL
		RETURNING attempts`, id).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrChallengeInvalid
	}
	if err != nil {
		return 0, fmt.Errorf("bump challenge attempt: %w", err)
	}
	return attempts, nil
}

// ConsumeChallenge marks a challenge spent. Idempotent by construction: the
// WHERE clause makes a second call a no-op rather than an error.
func (r *Repository) ConsumeChallenge(ctx context.Context, id int64) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE auth_challenge SET consumed_at = now()
		WHERE id = $1 AND consumed_at IS NULL`, id); err != nil {
		return fmt.Errorf("consume challenge: %w", err)
	}
	return nil
}

// ReserveChallengeSend charges one e-mail send against the challenge, enforcing
// both the total cap and the cooldown in a single statement.
//
// Doing the check in SQL rather than reading-then-writing is what makes the
// cooldown hold when a user double-clicks "resend": both requests would
// otherwise read the same last-send timestamp and both would send.
func (r *Repository) ReserveChallengeSend(ctx context.Context, id int64) (int, error) {
	var sends int
	err := r.pool.QueryRow(ctx, `
		WITH last AS (
			SELECT max(created_at) AS at FROM email_otp WHERE challenge_id = $1
		)
		UPDATE auth_challenge c SET sends = c.sends + 1
		FROM last
		WHERE c.id = $1
		  AND c.consumed_at IS NULL
		  AND c.sends < $2
		  AND (last.at IS NULL OR last.at < now() - $3::interval)
		RETURNING c.sends`, id, maxChallengeSends, intervalArg(otpResendInterval)).Scan(&sends)
	if errors.Is(err, pgx.ErrNoRows) {
		// The UPDATE matched nothing. Separate the two refusals so the handler
		// can tell the user whether to wait or to use another factor.
		var total int
		var recent bool
		if err := r.pool.QueryRow(ctx, `
			SELECT c.sends,
			       EXISTS (SELECT 1 FROM email_otp o
			               WHERE o.challenge_id = c.id AND o.created_at >= now() - $2::interval)
			FROM auth_challenge c WHERE c.id = $1`,
			id, intervalArg(otpResendInterval)).Scan(&total, &recent); err != nil {
			return 0, ErrChallengeInvalid
		}
		if total >= maxChallengeSends {
			return total, ErrSendsExhausted
		}
		if recent {
			return total, ErrTooSoon
		}
		return total, ErrChallengeInvalid
	}
	if err != nil {
		return 0, fmt.Errorf("reserve challenge send: %w", err)
	}
	return sends, nil
}

// ─────────────────────────────────────────────────────────────────────
// TOTP
// ─────────────────────────────────────────────────────────────────────

// TOTPRow is one stored enrollment, secret still encrypted.
type TOTPRow struct {
	Ciphertext      []byte
	Nonce           []byte
	Params          totpParams
	Confirmed       bool
	LastUsedCounter *int64
}

// StartTOTPEnrollment stores a NEW, unconfirmed secret, replacing any previous
// unconfirmed one.
//
// It refuses to overwrite a CONFIRMED enrollment. Allowing that would make
// "start enrollment" a silent way to swap the second factor of an account whose
// session was stolen — the attacker would enrol their own authenticator and the
// owner's would simply stop working. Replacing a confirmed factor goes through
// disable, which demands the current password and a current code.
func (r *Repository) StartTOTPEnrollment(ctx context.Context, uid authctx.UserID, ciphertext, nonce []byte) error {
	ct, err := r.pool.Exec(ctx, `
		INSERT INTO totp_secret (user_id, secret_ciphertext, secret_nonce, algorithm, digits, period_seconds)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id) DO UPDATE
		SET secret_ciphertext = EXCLUDED.secret_ciphertext,
		    secret_nonce      = EXCLUDED.secret_nonce,
		    algorithm         = EXCLUDED.algorithm,
		    digits            = EXCLUDED.digits,
		    period_seconds    = EXCLUDED.period_seconds,
		    created_at        = now(),
		    confirmed_at      = NULL,
		    last_used_counter = NULL
		WHERE totp_secret.confirmed_at IS NULL`,
		int64(uid), ciphertext, nonce, totpAlgorithm, totpDigits, totpPeriodSeconds)
	if err != nil {
		return fmt.Errorf("start totp enrollment: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrTOTPAlreadyConfirmed
	}
	return nil
}

// ErrTOTPAlreadyConfirmed marks an attempt to re-enrol over a live factor.
var ErrTOTPAlreadyConfirmed = errors.New("auth: TOTP already confirmed")

// LoadTOTPSecret returns the stored enrollment, or ErrNoTOTP.
func (r *Repository) LoadTOTPSecret(ctx context.Context, uid authctx.UserID) (TOTPRow, error) {
	var row TOTPRow
	var confirmedAt *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT secret_ciphertext, secret_nonce, algorithm, digits, period_seconds,
		       confirmed_at, last_used_counter
		FROM totp_secret WHERE user_id = $1`, int64(uid)).
		Scan(&row.Ciphertext, &row.Nonce, &row.Params.Algorithm, &row.Params.Digits,
			&row.Params.Period, &confirmedAt, &row.LastUsedCounter)
	if errors.Is(err, pgx.ErrNoRows) {
		return TOTPRow{}, ErrNoTOTP
	}
	if err != nil {
		return TOTPRow{}, fmt.Errorf("load totp secret: %w", err)
	}
	row.Confirmed = confirmedAt != nil
	return row, nil
}

// ConfirmTOTP marks the enrollment live and records the counter that proved it.
//
// Recording the counter at confirmation time matters: without it the very code
// the user just typed to enrol would still be replayable for the rest of its
// own window.
func (r *Repository) ConfirmTOTP(ctx context.Context, uid authctx.UserID, counter int64) error {
	ct, err := r.pool.Exec(ctx, `
		UPDATE totp_secret SET confirmed_at = now(), last_used_counter = $2
		WHERE user_id = $1 AND confirmed_at IS NULL`, int64(uid), counter)
	if err != nil {
		return fmt.Errorf("confirm totp: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNoTOTP
	}
	return nil
}

// ConsumeTOTPCounter records a successfully used time step, refusing any
// counter that is not strictly newer than the last one.
//
// This is the replay guard, and it is a CONDITIONAL UPDATE rather than a
// read-compare-write on purpose: two requests presenting the same code at the
// same instant would both pass a Go-side comparison, and one of them is the
// attacker.
func (r *Repository) ConsumeTOTPCounter(ctx context.Context, uid authctx.UserID, counter int64) error {
	ct, err := r.pool.Exec(ctx, `
		UPDATE totp_secret SET last_used_counter = $2
		WHERE user_id = $1
		  AND confirmed_at IS NOT NULL
		  AND (last_used_counter IS NULL OR last_used_counter < $2)`,
		int64(uid), counter)
	if err != nil {
		return fmt.Errorf("consume totp counter: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrTOTPReplay
	}
	return nil
}

// DisableTOTP removes the enrollment and every recovery code with it.
//
// The two must go together: recovery codes exist only to get past a second
// factor, so leaving them behind after the factor is gone would keep a set of
// long-lived bearer credentials alive for an account that no longer has 2FA.
func (r *Repository) DisableTOTP(ctx context.Context, uid authctx.UserID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("disable totp begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM totp_secret WHERE user_id = $1`, int64(uid)); err != nil {
		return fmt.Errorf("delete totp secret: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM recovery_code WHERE user_id = $1`, int64(uid)); err != nil {
		return fmt.Errorf("delete recovery codes: %w", err)
	}
	return tx.Commit(ctx)
}

// ─────────────────────────────────────────────────────────────────────
// Recovery codes
// ─────────────────────────────────────────────────────────────────────

// ReplaceRecoveryCodes swaps the whole set atomically.
//
// Delete-then-insert in ONE transaction, so a user is never left with a
// half-replaced set: the old codes stop working exactly when the new ones start.
func (r *Repository) ReplaceRecoveryCodes(ctx context.Context, uid authctx.UserID, hashes [][]byte) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("replace recovery codes begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM recovery_code WHERE user_id = $1`, int64(uid)); err != nil {
		return fmt.Errorf("clear recovery codes: %w", err)
	}
	for _, h := range hashes {
		if _, err := tx.Exec(ctx,
			`INSERT INTO recovery_code (user_id, code_hash) VALUES ($1, $2)`, int64(uid), h); err != nil {
			return fmt.Errorf("insert recovery code: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ConsumeRecoveryCode spends one code, or reports ErrBadCredentials.
//
// The user_id predicate is not redundant with the unique hash index: without
// it, a code belonging to another account would verify. The conditional UPDATE
// makes single-use atomic — two parallel requests with the same code produce
// one success and one ErrBadCredentials, never two successes.
func (r *Repository) ConsumeRecoveryCode(ctx context.Context, uid authctx.UserID, codeHash []byte) error {
	ct, err := r.pool.Exec(ctx, `
		UPDATE recovery_code SET used_at = now()
		WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`, int64(uid), codeHash)
	if err != nil {
		return fmt.Errorf("consume recovery code: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrBadCredentials
	}
	return nil
}

// CountRecoveryCodes reports how many remain unused, for the settings screen's
// "3 of 10 remaining" nudge.
func (r *Repository) CountRecoveryCodes(ctx context.Context, uid authctx.UserID) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM recovery_code WHERE user_id = $1 AND used_at IS NULL`,
		int64(uid)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count recovery codes: %w", err)
	}
	return n, nil
}

// ─────────────────────────────────────────────────────────────────────
// E-mail OTP
// ─────────────────────────────────────────────────────────────────────

// CreateEmailOTP stores a hashed one-time code.
//
// Any earlier live code for the same user and purpose is consumed, so only the
// most recently e-mailed code works. Otherwise every resend would ADD a valid
// code, and three resends would leave three simultaneously-correct guesses in a
// space of only a million.
func (r *Repository) CreateEmailOTP(ctx context.Context, uid authctx.UserID, challengeID *int64, purpose string, codeHash []byte, ttl time.Duration) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create otp begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE email_otp SET consumed_at = now()
		WHERE user_id = $1 AND purpose = $2 AND consumed_at IS NULL`,
		int64(uid), purpose); err != nil {
		return fmt.Errorf("supersede otps: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO email_otp (user_id, challenge_id, purpose, code_hash, expires_at)
		VALUES ($1, $2, $3, $4, now() + $5::interval)`,
		int64(uid), challengeID, purpose, codeHash, intervalArg(ttl)); err != nil {
		return fmt.Errorf("insert otp: %w", err)
	}
	return tx.Commit(ctx)
}

// ConsumeEmailOTP spends a code for (user, purpose), or reports
// ErrBadCredentials.
//
// Like the recovery-code path, single-use is enforced by the UPDATE's own
// WHERE clause rather than by a preceding SELECT.
func (r *Repository) ConsumeEmailOTP(ctx context.Context, uid authctx.UserID, purpose string, codeHash []byte, challengeID *int64) error {
	// challengeID binds a login code to the exact challenge that mailed it.
	// Without it a code issued for one sign-in attempt would satisfy another,
	// which quietly widens the attempt budget: each challenge carries its own
	// 5-guess cap. It is nil for verify-email, which has no challenge.
	ct, err := r.pool.Exec(ctx, `
		UPDATE email_otp SET consumed_at = now()
		WHERE user_id = $1 AND purpose = $2 AND code_hash = $3
		  AND consumed_at IS NULL AND expires_at > now()
		  AND ($4::bigint IS NULL OR challenge_id = $4)`,
		int64(uid), purpose, codeHash, challengeID)
	if err != nil {
		return fmt.Errorf("consume otp: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrBadCredentials
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────
// Password reset
// ─────────────────────────────────────────────────────────────────────

// UserForPasswordReset resolves an address to an account eligible for a reset
// link.
//
// found=false for an unknown address, a non-active account OR an account with
// no password credential (Google-only, ADR-31). The handler answers 202 in
// every case — this method's job is only to decide whether an e-mail actually
// goes out, never to shape the response.
func (r *Repository) UserForPasswordReset(ctx context.Context, email string) (User, bool, error) {
	u, err := scanUser(r.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM app_user WHERE email_normalized = $1`, NormalizeEmail(email)))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("user for reset: %w", err)
	}
	if u.Status != StatusActive || !u.HasPassword {
		return u, false, nil
	}
	return u, true, nil
}

// CreatePasswordReset mints a reset token, superseding any outstanding one.
func (r *Repository) CreatePasswordReset(ctx context.Context, uid authctx.UserID, ttl time.Duration, ip string) (string, error) {
	raw, hash, err := secrets.NewToken()
	if err != nil {
		return "", fmt.Errorf("reset token: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("create reset begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Superseding keeps exactly one link live. A user who clicks "forgot
	// password" three times should not leave three usable tokens in three
	// e-mails, each an independent chance for one to be intercepted.
	if _, err := tx.Exec(ctx, `
		UPDATE password_reset SET consumed_at = now()
		WHERE user_id = $1 AND consumed_at IS NULL`, int64(uid)); err != nil {
		return "", fmt.Errorf("supersede resets: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO password_reset (user_id, token_hash, expires_at, requested_ip)
		VALUES ($1, $2, now() + $3::interval, $4)`,
		int64(uid), hash, intervalArg(ttl), nullIP(ip)); err != nil {
		return "", fmt.Errorf("insert reset: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("create reset commit: %w", err)
	}
	return raw, nil
}

// ConsumePasswordReset applies a new password to the account behind rawToken.
//
// One transaction does all four things a password reset means: spend the token,
// set the hash, bump token_version, and revoke every session. Splitting them
// would leave windows where the token is spent but the password is not set (the
// user is locked out with no way back) or where the password is new but old
// sessions still work — and the second one matters most, because resetting a
// password is precisely how someone evicts an intruder.
func (r *Repository) ConsumePasswordReset(ctx context.Context, rawToken, newPassword string) (User, error) {
	if rawToken == "" {
		return User{}, ErrResetInvalid
	}
	hash, err := pwhash.Hash(newPassword)
	if err != nil {
		return User{}, fmt.Errorf("hash reset password: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("consume reset begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var uid int64
	err = tx.QueryRow(ctx, `
		UPDATE password_reset SET consumed_at = now()
		WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
		RETURNING user_id`, secrets.Hash(rawToken)).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrResetInvalid
	}
	if err != nil {
		return User{}, fmt.Errorf("consume reset: %w", err)
	}

	// A reset also VERIFIES the address: the user just proved they read mail
	// sent to it. Leaving email_verified_at null after that would ask them to
	// prove the same fact twice.
	u, err := scanUser(tx.QueryRow(ctx, `
		UPDATE app_user
		SET password_hash = $2,
		    token_version = token_version + 1,
		    email_verified_at = COALESCE(email_verified_at, now()),
		    status = CASE WHEN status = 'pending' THEN 'active' ELSE status END,
		    updated_at = now()
		WHERE id = $1
		RETURNING `+userColumns, uid, hash))
	if err != nil {
		return User{}, fmt.Errorf("apply reset password: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE session SET revoked_at = now(), revoked_reason = $2
		WHERE user_id = $1 AND revoked_at IS NULL`, uid, ReasonPasswordChanged); err != nil {
		return User{}, fmt.Errorf("revoke sessions on reset: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("consume reset commit: %w", err)
	}
	return u, nil
}

// ConsumeEmailVerification spends a verification token and returns its owner.
//
// Unlike the login OTP this resolves by HASH ALONE, with no user_id — the
// caller has no session yet, which is the whole point of a link. That is safe
// only because the value is 256 bits from crypto/rand rather than a six-digit
// code: the token IS the identifier. A six-digit code looked up this way would
// be guessable across the whole user base at once.
// It also MARKS the address verified, in the same statement.
//
// Spending the token and recording the result have to be one write. Split in
// two, a cancelled request or a pool blip between them burns the token while
// leaving the address unverified — and the only way to get another is
// /email/resend, which needs a session, so someone following the link on a
// device that never signed in is simply stuck.
func (r *Repository) ConsumeEmailVerification(ctx context.Context, tokenHash []byte) (authctx.UserID, error) {
	var uid int64
	err := r.pool.QueryRow(ctx, `
		WITH spent AS (
			UPDATE email_otp SET consumed_at = now()
			WHERE code_hash = $1 AND purpose = $2
			  AND consumed_at IS NULL AND expires_at > now()
			RETURNING user_id
		), verified AS (
			UPDATE app_user u
			SET email_verified_at = COALESCE(u.email_verified_at, now()), updated_at = now()
			FROM spent
			WHERE u.id = spent.user_id
			RETURNING u.id
		)
		SELECT user_id FROM spent`, tokenHash, OTPPurposeVerifyEmail).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrBadCredentials
	}
	if err != nil {
		return 0, fmt.Errorf("consume email verification: %w", err)
	}
	return authctx.UserID(uid), nil
}

// MarkEmailVerified records that the address has been proven.
func (r *Repository) MarkEmailVerified(ctx context.Context, uid authctx.UserID) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE app_user SET email_verified_at = COALESCE(email_verified_at, now()), updated_at = now()
		WHERE id = $1`, int64(uid)); err != nil {
		return fmt.Errorf("mark email verified: %w", err)
	}
	return nil
}

// SweepTwoFactor deletes expired challenges and OTPs.
//
// Both tables are written on unauthenticated paths, so without this they grow
// with every login attempt an attacker makes. Consumed rows go too: unlike
// session_used_token — which IS the reuse detector's memory and must be kept —
// a spent challenge has no evidentiary value once it is past its expiry.
func (r *Repository) SweepTwoFactor(ctx context.Context, retain time.Duration) (int64, error) {
	var total int64
	for _, q := range []string{
		`DELETE FROM auth_challenge WHERE expires_at < now() - $1::interval`,
		`DELETE FROM email_otp      WHERE expires_at < now() - $1::interval`,
		`DELETE FROM password_reset WHERE expires_at < now() - $1::interval`,
	} {
		ct, err := r.pool.Exec(ctx, q, intervalArg(retain))
		if err != nil {
			return total, fmt.Errorf("sweep 2fa: %w", err)
		}
		total += ct.RowsAffected()
	}
	return total, nil
}

// intervalArg renders a Go duration for a `$n::interval` placeholder.
//
// time.Duration.String() happens to emit a form Postgres parses ("15m0s",
// "168h0m0s", "500ms"), which is why the existing sweep queries pass it
// directly. Naming it keeps that coincidence explicit — it reads like a plain
// string otherwise, and the next person to pass a preformatted "15 minutes"
// would be equally right and inconsistent.
func intervalArg(d time.Duration) string { return d.String() }
