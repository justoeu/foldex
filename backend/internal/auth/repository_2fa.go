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

// ChallengePurpose is the closed purpose domain persisted in auth_challenge.
type ChallengePurpose string

// Challenge purposes, mirroring auth_challenge_purpose_check.
const (
	PurposeTOTP          ChallengePurpose = "totp"
	PurposeEnroll2FA     ChallengePurpose = "enroll_2fa"
	PurposeConvertGoogle ChallengePurpose = "convert_google"
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
	// Floor for the configurable resend cooldown (ADR-35). Policy may raise it,
	// never lower it: this is the value every release shipped with.
	otpResendInterval = 60 * time.Second
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
	ErrTooSoon              = errors.New("auth: too soon")
	ErrEmailAlreadyVerified = errors.New("auth: e-mail already verified")
	// ErrSendsExhausted marks a challenge whose send budget is spent.
	ErrSendsExhausted = errors.New("auth: challenge sends exhausted")
	// ErrNoTOTP marks an account with no enrollment to act on.
	ErrNoTOTP = errors.New("auth: no TOTP secret")
	// ErrTOTPEnrollmentChanged means the pending seed no longer matches the one
	// whose code was verified.
	ErrTOTPEnrollmentChanged = errors.New("auth: TOTP enrollment changed")
	// ErrResetInvalid covers absent, expired and consumed reset tokens.
	ErrResetInvalid = errors.New("auth: password reset token invalid")
	// ErrRecoveryUnavailable means an administrator targeted an account that is
	// not active or whose mailbox has not been verified.
	ErrRecoveryUnavailable = errors.New("auth: recovery requires an active account with verified e-mail")
)

// Challenge is the pre-auth state between "password OK" and "second factor OK".
type Challenge struct {
	ID           int64
	UserID       authctx.UserID
	Purpose      ChallengePurpose
	TokenVersion int
	Attempts     int
	Sends        int
	// MailboxAlreadyProven marks a challenge whose FIRST factor was a
	// password-reset link. The e-mail OTP is refused on those: the code would
	// go to the same inbox the link came from, so both steps would be
	// satisfiable by one compromised mailbox.
	MailboxAlreadyProven bool
	// OAuthSubject / OAuthEmail describe the provider account a
	// convert_google challenge is about, empty for every other purpose. They
	// are read from the row rather than from the request so the conversion
	// attaches the identity Google authenticated, not one the client names.
	OAuthSubject string
	OAuthEmail   string
}

// ─────────────────────────────────────────────────────────────────────
// Challenges
// ─────────────────────────────────────────────────────────────────────

// NewChallenge is everything a pre-auth challenge is born with.
//
// A struct rather than a parameter list because the list had already reached
// seven, and the two booleans in it — mailbox_already_proven and, at the call
// site, whichever flag comes next — are exactly the arguments that get swapped
// silently. Named fields make that a visible mistake.
type NewChallenge struct {
	UserID       authctx.UserID
	Purpose      ChallengePurpose
	TokenVersion int
	TTL          time.Duration
	IP           string
	// UserAgent is recorded for the "a code was requested from…" mail.
	UserAgent string
	// MailboxAlreadyProven marks a challenge whose FIRST factor was a link sent
	// to the account's address, which disqualifies the e-mail second factor —
	// otherwise one channel would satisfy both steps.
	MailboxAlreadyProven bool
	// Identity is the provider account a convert_google challenge is about. It
	// is stored server-side precisely so the convert request cannot name a
	// different subject than the one Google authenticated.
	Identity *linkedIdentity
}

// CreateChallenge mints a pre-auth token and returns its raw value.
//
// Any earlier live challenge for the same user and purpose is replaced while
// preserving its counters, expiry and first-factor provenance. Without that, a
// user who retries the password form receives a fresh attempt budget, turning
// the 5-guess cap into "5 guesses per password entry".
func (r *Repository) CreateChallenge(ctx context.Context, in NewChallenge) (string, int64, error) {
	raw, hash, err := secrets.NewToken()
	if err != nil {
		return "", 0, fmt.Errorf("challenge token: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("challenge begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Challenge creation is serialized per user. Locking only the previous
	// challenge cannot stop two first challenges from being inserted together.
	var lockedUser int64
	var liveTokenVersion int
	if err := tx.QueryRow(ctx, `
		SELECT id, token_version FROM app_user
		WHERE id = $1 AND status = 'active'
		FOR NO KEY UPDATE`, int64(in.UserID)).Scan(&lockedUser, &liveTokenVersion); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", 0, ErrChallengeInvalid
		}
		return "", 0, fmt.Errorf("lock challenge user: %w", err)
	}
	if liveTokenVersion != in.TokenVersion {
		return "", 0, ErrChallengeInvalid
	}

	var previousID int64
	var attempts, sends int
	var expiresAt time.Time
	var mailboxAlreadyProven bool
	err = tx.QueryRow(ctx, `
		SELECT id, attempts, sends, expires_at, mailbox_already_proven
		FROM auth_challenge
		WHERE user_id = $1 AND purpose = $2
		  AND token_version = $3
		  AND consumed_at IS NULL AND expires_at > now()
		ORDER BY created_at DESC, id DESC
		LIMIT 1
		FOR UPDATE`, int64(in.UserID), in.Purpose, in.TokenVersion).
		Scan(&previousID, &attempts, &sends, &expiresAt, &mailboxAlreadyProven)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", 0, fmt.Errorf("load previous challenge: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth_challenge SET consumed_at = now()
		WHERE user_id = $1 AND purpose = $2 AND token_version IS NOT NULL
		  AND consumed_at IS NULL`,
		int64(in.UserID), in.Purpose); err != nil {
		return "", 0, fmt.Errorf("supersede challenges: %w", err)
	}

	var provider, subject, email *string
	if in.Identity != nil {
		provider, subject = &in.Identity.provider, &in.Identity.subject
		email = nullString(in.Identity.email)
	}

	var inheritedExpiry *time.Time
	if previousID != 0 {
		inheritedExpiry = &expiresAt
	}
	mailboxAlreadyProven = mailboxAlreadyProven || in.MailboxAlreadyProven

	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO auth_challenge (user_id, token_hash, purpose, token_version, expires_at, ip, user_agent,
		                            attempts, sends, mailbox_already_proven,
		                            oauth_provider, oauth_subject, oauth_email)
		VALUES ($1, $2, $3, $4, COALESCE($14::timestamptz, now() + $5::interval), $6, $7,
		        $8, $9, $10, $11, $12, $13)
		RETURNING id`,
		int64(in.UserID), hash, in.Purpose, in.TokenVersion, intervalArg(in.TTL), nullIP(in.IP), in.UserAgent,
		attempts, sends, mailboxAlreadyProven, provider, subject, email, inheritedExpiry).Scan(&id); err != nil {
		return "", 0, fmt.Errorf("insert challenge: %w", err)
	}
	if previousID != 0 {
		// The code MAC is bound to previousID and therefore cannot move to the
		// replacement challenge. Move only its timestamp for resend-cooldown
		// accounting and mark it spent so it can never be presented there.
		if _, err := tx.Exec(ctx, `
			UPDATE email_otp
			SET challenge_id = $2, consumed_at = COALESCE(consumed_at, now())
			WHERE challenge_id = $1`,
			previousID, id); err != nil {
			return "", 0, fmt.Errorf("invalidate moved challenge codes: %w", err)
		}
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
func (r *Repository) ResolveChallenge(ctx context.Context, rawToken string, purposes ...ChallengePurpose) (Challenge, error) {
	if rawToken == "" {
		return Challenge{}, ErrChallengeInvalid
	}
	var c Challenge
	var uid int64
	err := r.pool.QueryRow(ctx, `
		SELECT c.id, c.user_id, c.purpose, c.token_version, c.attempts, c.sends, c.mailbox_already_proven,
		       COALESCE(oauth_subject, ''), COALESCE(oauth_email, '')
		FROM auth_challenge c
		JOIN app_user u ON u.id = c.user_id
		WHERE c.token_hash = $1 AND c.consumed_at IS NULL AND c.expires_at > now()
		  AND c.token_version = u.token_version AND u.status = 'active'`,
		secrets.Hash(rawToken)).Scan(&c.ID, &uid, &c.Purpose, &c.TokenVersion, &c.Attempts, &c.Sends,
		&c.MailboxAlreadyProven, &c.OAuthSubject, &c.OAuthEmail)
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
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("bump challenge attempt begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockChallengeUser(ctx, tx, id); err != nil {
		return 0, err
	}

	var attempts int
	err = tx.QueryRow(ctx, `
		UPDATE auth_challenge c SET attempts = c.attempts + 1
		FROM app_user u
		WHERE c.id = $1 AND u.id = c.user_id
		  AND c.token_version = u.token_version AND u.status = 'active'
		  AND c.consumed_at IS NULL
		  AND c.expires_at > now()
		  AND c.attempts < $2
		RETURNING c.attempts`, id, maxChallengeAttempts).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		var live bool
		if diagErr := tx.QueryRow(ctx, `
			SELECT c.attempts, c.consumed_at IS NULL AND c.expires_at > now()
			       AND c.token_version = u.token_version AND u.status = 'active'
			FROM auth_challenge c JOIN app_user u ON u.id = c.user_id
			WHERE c.id = $1`, id).Scan(&attempts, &live); diagErr != nil {
			if errors.Is(diagErr, pgx.ErrNoRows) {
				return 0, ErrChallengeInvalid
			}
			return 0, fmt.Errorf("diagnose challenge attempt: %w", diagErr)
		}
		if live && attempts >= maxChallengeAttempts {
			return attempts, ErrChallengeExhausted
		}
		return 0, ErrChallengeInvalid
	}
	if err != nil {
		return 0, fmt.Errorf("bump challenge attempt: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("bump challenge attempt commit: %w", err)
	}
	return attempts, nil
}

// ConsumeChallenge marks a live challenge spent. Exactly one caller can win;
// every other caller receives ErrChallengeInvalid and must not issue a session.
func (r *Repository) ConsumeChallenge(ctx context.Context, id int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("consume challenge begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockChallengeUser(ctx, tx, id); err != nil {
		return err
	}

	ct, err := tx.Exec(ctx, `
		UPDATE auth_challenge c SET consumed_at = now()
		FROM app_user u
		WHERE c.id = $1 AND u.id = c.user_id
		  AND c.token_version = u.token_version AND u.status = 'active'
		  AND c.consumed_at IS NULL AND c.expires_at > now()`, id)
	if err != nil {
		return fmt.Errorf("consume challenge: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrChallengeInvalid
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("consume challenge commit: %w", err)
	}
	return nil
}

// CreateChallengeEmailOTP charges one send and publishes its code atomically,
// while enforcing both the total cap and cooldown in one transaction.
//
// Doing the check in SQL rather than reading-then-writing is what makes the
// cooldown hold when a user double-clicks "resend": both requests would
// otherwise read the same last-send timestamp and both would send.
//
// The mail joins the same transaction, so a send that was charged against the
// challenge's budget of three always produces a message: charging the budget
// and losing the code would spend a scarce resource on nothing.
func (r *Repository) CreateChallengeEmailOTP(ctx context.Context, id int64, codeHash []byte,
	ttl, cooldown time.Duration, draft MailDraft) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("reserve challenge send begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockChallengeUser(ctx, tx, id); err != nil {
		return 0, err
	}

	var sends int
	err = tx.QueryRow(ctx, `
		WITH last AS (
			SELECT max(created_at) AS at FROM email_otp WHERE challenge_id = $1
		)
		UPDATE auth_challenge c SET sends = c.sends + 1
		FROM last
		WHERE c.id = $1
		  AND EXISTS (SELECT 1 FROM app_user u
		              WHERE u.id = c.user_id AND u.status = 'active'
		                AND u.token_version = c.token_version)
		  AND c.consumed_at IS NULL
		  AND c.expires_at > now()
		  AND c.sends < $2
		  AND (last.at IS NULL OR last.at < now() - $3::interval)
		RETURNING c.sends`, id, maxChallengeSends, intervalArg(cooldown)).Scan(&sends)
	if errors.Is(err, pgx.ErrNoRows) {
		// The UPDATE matched nothing. Separate the two refusals so the handler
		// can tell the user whether to wait or to use another factor.
		var total int
		var recent, live bool
		if diagErr := tx.QueryRow(ctx, `
			SELECT c.sends,
			       EXISTS (SELECT 1 FROM email_otp o
			               WHERE o.challenge_id = c.id AND o.created_at >= now() - $2::interval),
			       c.consumed_at IS NULL AND c.expires_at > now()
			       AND c.token_version = u.token_version AND u.status = 'active'
			FROM auth_challenge c JOIN app_user u ON u.id = c.user_id
			WHERE c.id = $1`,
			id, intervalArg(cooldown)).Scan(&total, &recent, &live); diagErr != nil {
			if !errors.Is(diagErr, pgx.ErrNoRows) {
				return 0, fmt.Errorf("diagnose challenge send: %w", diagErr)
			}
			return 0, ErrChallengeInvalid
		}
		if !live {
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
	if _, err := tx.Exec(ctx, `
		UPDATE email_otp SET consumed_at = now()
		WHERE user_id = (SELECT user_id FROM auth_challenge WHERE id = $1)
		  AND purpose = $2 AND consumed_at IS NULL`, id, OTPPurposeLogin2FA); err != nil {
		return 0, fmt.Errorf("supersede challenge otp: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO email_otp (user_id, challenge_id, purpose, code_hash, expires_at)
		SELECT user_id, id, $2, $3, now() + $4::interval
		FROM auth_challenge WHERE id = $1`, id, OTPPurposeLogin2FA, codeHash, intervalArg(ttl)); err != nil {
		return 0, fmt.Errorf("insert challenge otp: %w", err)
	}
	if err := r.enqueueDraft(ctx, tx, draft, ""); err != nil {
		return 0, fmt.Errorf("queue login otp mail: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("reserve challenge send commit: %w", err)
	}
	return sends, nil
}

// lockChallengeUser establishes the package-wide lock order: app_user first,
// then the challenge row mutated by the caller. Credential epoch changes use
// the same order, so a stale challenge can never commit a mutation after them.
func lockChallengeUser(ctx context.Context, tx pgx.Tx, challengeID int64) error {
	var uid int64
	if err := tx.QueryRow(ctx,
		`SELECT user_id FROM auth_challenge WHERE id = $1`, challengeID).Scan(&uid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrChallengeInvalid
		}
		return fmt.Errorf("load challenge user: %w", err)
	}
	var lockedUser int64
	if err := tx.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, uid).Scan(&lockedUser); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrChallengeInvalid
		}
		return fmt.Errorf("lock challenge user: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────
// TOTP
// ─────────────────────────────────────────────────────────────────────

// TOTPRow is one stored enrollment, secret still encrypted.
type TOTPRow struct {
	Ciphertext             []byte
	Nonce                  []byte
	Params                 totpParams
	Confirmed              bool
	EnrollmentTokenVersion *int
	EnrollmentSessionID    *int64
	LastUsedCounter        *int64
}

// TOTPProof identifies the exact encrypted seed and time-step verified by a
// handler so the repository can consume that proof with the protected write.
type TOTPProof struct {
	Counter    int64
	Ciphertext []byte
	Nonce      []byte
}

// challengeProof is the LOGIN-path proof: what Verify2FA resolved from a code
// submitted against an auth_challenge, which carries its own attempt budget.
//
// Distinct from the exported SecondFactorProof, which serves the
// session-authenticated step-up paths that have no challenge. The two were
// briefly named the same word in different cases, which is a distinction no
// reader should have to make.
type challengeProof struct {
	totp           *TOTPProof
	emailDigest    []byte
	recoveryDigest []byte
}

// StartTOTPEnrollment stores a NEW, unconfirmed secret, replacing any previous
// unconfirmed one.
//
// It refuses to overwrite a CONFIRMED enrollment. Allowing that would make
// "start enrollment" a silent way to swap the second factor of an account whose
// session was stolen — the attacker would enrol their own authenticator and the
// owner's would simply stop working. Replacing a confirmed factor goes through
// disable, which demands the current password and a current code.
func (r *Repository) StartTOTPEnrollment(ctx context.Context, uid authctx.UserID, tokenVersion int,
	sessionID int64, ciphertext, nonce []byte) error {

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("start totp enrollment begin: %w", err)
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
		return fmt.Errorf("start totp enrollment lock user: %w", err)
	}
	if sessionID != 0 {
		if err := requireLiveSessionTx(ctx, tx, uid, sessionID); err != nil {
			return err
		}
	}

	ct, err := tx.Exec(ctx, `
		INSERT INTO totp_secret (
			user_id, secret_ciphertext, secret_nonce, algorithm, digits, period_seconds,
			enrollment_token_version, enrollment_session_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, 0))
		ON CONFLICT (user_id) DO UPDATE
		SET secret_ciphertext = EXCLUDED.secret_ciphertext,
		    secret_nonce      = EXCLUDED.secret_nonce,
		    algorithm         = EXCLUDED.algorithm,
		    digits            = EXCLUDED.digits,
		    period_seconds    = EXCLUDED.period_seconds,
		    created_at        = now(),
		    confirmed_at      = NULL,
		    enrollment_token_version = EXCLUDED.enrollment_token_version,
		    enrollment_session_id = EXCLUDED.enrollment_session_id,
		    last_used_counter = NULL
		WHERE totp_secret.confirmed_at IS NULL`,
		int64(uid), ciphertext, nonce, totpAlgorithm, totpDigits, totpPeriodSeconds, tokenVersion, sessionID)
	if err != nil {
		return fmt.Errorf("start totp enrollment: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrTOTPAlreadyConfirmed
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("start totp enrollment commit: %w", err)
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
		       confirmed_at, enrollment_token_version, enrollment_session_id, last_used_counter
		FROM totp_secret WHERE user_id = $1`, int64(uid)).
		Scan(&row.Ciphertext, &row.Nonce, &row.Params.Algorithm, &row.Params.Digits,
			&row.Params.Period, &confirmedAt, &row.EnrollmentTokenVersion,
			&row.EnrollmentSessionID, &row.LastUsedCounter)
	if errors.Is(err, pgx.ErrNoRows) {
		return TOTPRow{}, ErrNoTOTP
	}
	if err != nil {
		return TOTPRow{}, fmt.Errorf("load totp secret: %w", err)
	}
	row.Confirmed = confirmedAt != nil
	return row, nil
}

// HasConfirmedSecondFactor is the authorization-time source of truth for
// mandatory administrator 2FA. It intentionally reads the current rows rather
// than any session-cached claim, so deleting or replacing a factor fails closed
// on the next admin request.
//
// totpOnly comes from instance policy (ADR-37 §7.5). When it is false an
// enrolled e-mail factor satisfies the gate; when true only an authenticator
// does. It is a PARAMETER rather than something this method reads for itself
// because the repository has no business knowing about policy — and because a
// caller that forgets to pass it gets the permissive floor, which is the
// direction that cannot lock an owner out of their own instance.
func (r *Repository) HasConfirmedSecondFactor(ctx context.Context, uid authctx.UserID, totpOnly bool) (bool, error) {
	var confirmed bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM totp_secret
			WHERE user_id = $1 AND confirmed_at IS NOT NULL
		) OR (NOT $2 AND EXISTS (
			SELECT 1 FROM email_factor
			WHERE user_id = $1 AND confirmed_at IS NOT NULL
		))`, int64(uid), totpOnly).Scan(&confirmed); err != nil {
		return false, fmt.Errorf("check confirmed second factor: %w", err)
	}
	return confirmed, nil
}

// CompleteTOTPEnrollment activates the exact seed that was verified, replaces
// recovery codes and, for mandatory pre-auth enrollment, consumes the challenge
// and issues the first session in one transaction.
func (r *Repository) CompleteTOTPEnrollment(ctx context.Context, uid authctx.UserID, tokenVersion int,
	proof TOTPProof, recoveryHashes [][]byte, sessionID int64, challenge *Challenge, ttl SessionTTL,
	ip, ua string) (User, issuedTokens, error) {

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
		return User{}, issuedTokens{}, fmt.Errorf("complete totp enrollment begin: %w", err)
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
		return User{}, issuedTokens{}, fmt.Errorf("complete totp enrollment lock user: %w", err)
	}
	if challenge == nil {
		if err := requireLiveSessionTx(ctx, tx, uid, sessionID); err != nil {
			return User{}, issuedTokens{}, err
		}
	}
	if err := confirmTOTPRowTx(ctx, tx, uid, tokenVersion, sessionID, proof); err != nil {
		return User{}, issuedTokens{}, err
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
			return User{}, issuedTokens{}, fmt.Errorf("complete enrollment consume challenge: %w", err)
		}
		if ct.RowsAffected() == 0 {
			return User{}, issuedTokens{}, ErrChallengeInvalid
		}
		if _, err := issueSessionTx(ctx, tx, uid, issue, ip, ua); err != nil {
			return User{}, issuedTokens{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE app_user SET last_login_at = now() WHERE id = $1`, int64(uid)); err != nil {
			return User{}, issuedTokens{}, fmt.Errorf("complete enrollment touch user: %w", err)
		}
	}

	user, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM app_user WHERE id = $1`, int64(uid)))
	if err != nil {
		return User{}, issuedTokens{}, fmt.Errorf("complete enrollment load user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, issuedTokens{}, fmt.Errorf("complete totp enrollment commit: %w", err)
	}
	return user, issue.tokens, nil
}

func confirmTOTPRowTx(ctx context.Context, tx pgx.Tx, uid authctx.UserID,
	tokenVersion int, sessionID int64, proof TOTPProof) error {

	ct, err := tx.Exec(ctx, `
		UPDATE totp_secret SET confirmed_at = now(), last_used_counter = $2,
		                       enrollment_session_id = NULL
		WHERE user_id = $1 AND confirmed_at IS NULL
		  AND enrollment_token_version = $3
		  AND secret_ciphertext = $4 AND secret_nonce = $5
		  AND (($6 = 0 AND enrollment_session_id IS NULL) OR enrollment_session_id = $6)`,
		int64(uid), proof.Counter, tokenVersion, proof.Ciphertext, proof.Nonce, sessionID)
	if err != nil {
		return fmt.Errorf("confirm totp: %w", err)
	}
	if ct.RowsAffected() != 0 {
		return nil
	}
	var exists bool
	var enrollmentTokenVersion *int
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM totp_secret WHERE user_id = $1),
		       (SELECT enrollment_token_version FROM totp_secret WHERE user_id = $1)`,
		int64(uid)).Scan(&exists, &enrollmentTokenVersion); err != nil {
		return fmt.Errorf("diagnose totp confirmation: %w", err)
	}
	if !exists {
		return ErrNoTOTP
	}
	if enrollmentTokenVersion == nil {
		return ErrChallengeInvalid
	}
	return ErrTOTPEnrollmentChanged
}

func (r *Repository) ConsumeTOTPProof(ctx context.Context, uid authctx.UserID, proof TOTPProof) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("consume totp proof begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := consumeTOTPProofTx(ctx, tx, uid, proof); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("consume totp proof commit: %w", err)
	}
	return nil
}

// Complete2FA spends exactly one accepted proof and its challenge, then creates
// the session. A failure in any later write restores both bearer credentials.
func (r *Repository) Complete2FA(ctx context.Context, ch Challenge, proof challengeProof,
	ttl SessionTTL, ip, ua string) (issuedTokens, string, error) {

	issue, err := newSessionIssue(ttl)
	if err != nil {
		return issuedTokens{}, "", err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return issuedTokens{}, "", fmt.Errorf("complete 2fa begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedUser int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM app_user
		WHERE id = $1 AND status = 'active' AND token_version = $2
		FOR NO KEY UPDATE`, int64(ch.UserID), ch.TokenVersion).Scan(&lockedUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return issuedTokens{}, "", ErrChallengeInvalid
	}
	if err != nil {
		return issuedTokens{}, "", fmt.Errorf("complete 2fa lock user: %w", err)
	}

	var liveChallenge int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM auth_challenge
		WHERE id = $1 AND user_id = $2 AND purpose = 'totp' AND token_version = $3
		  AND consumed_at IS NULL AND expires_at > now() AND attempts <= $4
		FOR UPDATE`, ch.ID, int64(ch.UserID), ch.TokenVersion, maxChallengeAttempts).Scan(&liveChallenge)
	if errors.Is(err, pgx.ErrNoRows) {
		return issuedTokens{}, "", ErrChallengeInvalid
	}
	if err != nil {
		return issuedTokens{}, "", fmt.Errorf("complete 2fa lock challenge: %w", err)
	}

	method, err := consumeChallengeProofTx(ctx, tx, ch, proof)
	if err != nil {
		return issuedTokens{}, "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_challenge SET consumed_at = now() WHERE id = $1`, ch.ID); err != nil {
		return issuedTokens{}, "", fmt.Errorf("complete 2fa consume challenge: %w", err)
	}
	if _, err := issueSessionTx(ctx, tx, ch.UserID, issue, ip, ua); err != nil {
		return issuedTokens{}, "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE app_user SET last_login_at = now() WHERE id = $1`, int64(ch.UserID)); err != nil {
		return issuedTokens{}, "", fmt.Errorf("complete 2fa touch user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return issuedTokens{}, "", fmt.Errorf("complete 2fa commit: %w", err)
	}
	return issue.tokens, method, nil
}

func consumeChallengeProofTx(ctx context.Context, tx pgx.Tx, ch Challenge,
	proof challengeProof) (string, error) {

	if proof.totp != nil {
		ok, err := consumeTOTPProofIfCurrentTx(ctx, tx, ch.UserID, *proof.totp)
		if err != nil {
			return "", err
		}
		if ok {
			return MethodTOTP, nil
		}
	}
	if len(proof.emailDigest) != 0 {
		ct, err := tx.Exec(ctx, `
			UPDATE email_otp SET consumed_at = now()
			WHERE user_id = $1 AND challenge_id = $2 AND purpose = $3
			  AND code_hash = $4 AND consumed_at IS NULL AND expires_at > now()`,
			int64(ch.UserID), ch.ID, OTPPurposeLogin2FA, proof.emailDigest)
		// A numeric submission may be either TOTP or e-mail OTP. Keep a broken
		// optional e-mail path indistinguishable from a miss so authenticator
		// login still works while that table is unavailable.
		if err == nil && ct.RowsAffected() != 0 {
			return MethodEmailOTP, nil
		}
	}
	if len(proof.recoveryDigest) != 0 {
		ct, err := tx.Exec(ctx, `
			UPDATE recovery_code SET used_at = now()
			WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`,
			int64(ch.UserID), proof.recoveryDigest)
		if err != nil {
			return "", fmt.Errorf("consume recovery proof: %w", err)
		}
		if ct.RowsAffected() != 0 {
			return MethodRecovery, nil
		}
	}
	return "", ErrBadCredentials
}

func consumeTOTPProofTx(ctx context.Context, tx pgx.Tx, uid authctx.UserID, proof TOTPProof) error {
	ok, err := consumeTOTPProofIfCurrentTx(ctx, tx, uid, proof)
	if err != nil {
		return err
	}
	if !ok {
		return ErrTOTPReplay
	}
	return nil
}

func consumeTOTPProofIfCurrentTx(ctx context.Context, tx pgx.Tx, uid authctx.UserID,
	proof TOTPProof) (bool, error) {

	ct, err := tx.Exec(ctx, `
		UPDATE totp_secret SET last_used_counter = $2
		WHERE user_id = $1 AND confirmed_at IS NOT NULL
		  AND secret_ciphertext = $3 AND secret_nonce = $4
		  AND (last_used_counter IS NULL OR last_used_counter < $2)`,
		int64(uid), proof.Counter, proof.Ciphertext, proof.Nonce)
	if err != nil {
		return false, fmt.Errorf("consume totp proof: %w", err)
	}
	return ct.RowsAffected() != 0, nil
}

// SecondFactorProof is a proof that has been VERIFIED but not yet spent.
//
// Verification and consumption are separated so that spending happens in the
// SAME transaction as the operation the proof authorizes. CLAUDE.md §4 states
// the property for TOTP, and it matters at least as much for the other two: a
// recovery code is a LOCKOUT credential, so burning one for a password change
// that then failed on a constraint costs the user a way back into their account
// for an operation that never happened.
//
// Exactly one field is populated. The method is what selects the statement, and
// an unknown one consumes nothing and fails closed.
type SecondFactorProof struct {
	Method string
	// TOTP carries the matched time-step counter and the exact seed ciphertext
	// that produced it, so the conditional UPDATE can reject a replay.
	TOTP *TOTPProof
	// Digest is the keyed MAC of a recovery code or a mailed step-up code.
	Digest []byte
}

// consumeSecondFactorTx spends the proof, inside the caller's transaction.
func consumeSecondFactorTx(ctx context.Context, tx pgx.Tx, uid authctx.UserID,
	p SecondFactorProof) error {

	switch p.Method {
	case MethodTOTP:
		if p.TOTP == nil {
			return ErrBadCredentials
		}
		return consumeTOTPProofTx(ctx, tx, uid, *p.TOTP)
	case MethodRecovery:
		return consumeSingleUseTx(ctx, tx, `
			UPDATE recovery_code SET used_at = now()
			WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`,
			int64(uid), p.Digest)
	case MethodEmailOTP:
		return consumeSingleUseTx(ctx, tx, `
			UPDATE email_otp SET consumed_at = now()
			WHERE user_id = $1 AND code_hash = $2 AND purpose = $3
			  AND consumed_at IS NULL AND expires_at > now()`,
			int64(uid), p.Digest, OTPPurposeStepUp2FA)
	default:
		// An empty or unrecognised method reaching here means a caller obtained
		// a proof it never verified. Spending nothing and refusing is the only
		// safe reading.
		return ErrBadCredentials
	}
}

func consumeSingleUseTx(ctx context.Context, tx pgx.Tx, sql string, args ...any) error {
	ct, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("consume second factor: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrBadCredentials
	}
	return nil
}

// ConsumeSecondFactor spends a proof outside any caller transaction.
//
// The OAuth link path is the one caller: what the proof authorizes is the
// oauth_state row it mints, and that row is itself the capability, so the code
// must not stay live long enough to mint a second one.
func (r *Repository) ConsumeSecondFactor(ctx context.Context, uid authctx.UserID,
	proof SecondFactorProof) error {

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("consume second factor begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := consumeSecondFactorTx(ctx, tx, uid, proof); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("consume second factor commit: %w", err)
	}
	return nil
}

// DisableTOTP removes the enrollment and every recovery code with it.
//
// The two must go together: recovery codes exist only to get past a second
// factor, so leaving them behind after the factor is gone would keep a set of
// long-lived bearer credentials alive for an account that no longer has 2FA.
func (r *Repository) DisableTOTP(ctx context.Context, uid authctx.UserID, sessionID int64,
	tokenVersion int, password string, proof SecondFactorProof) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("disable totp begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var passwordHash *string
	var liveVersion int
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT password_hash, token_version, status
		FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(uid)).
		Scan(&passwordHash, &liveVersion, &status); err != nil {
		return fmt.Errorf("disable totp lock user: %w", err)
	}
	if status != StatusActive || liveVersion != tokenVersion {
		return ErrSessionInvalid
	}
	if passwordHash == nil {
		return ErrPasswordMissing
	}
	if !pwhash.Verify(*passwordHash, password) {
		return ErrBadCredentials
	}
	if err := requireLiveSessionTx(ctx, tx, uid, sessionID); err != nil {
		return err
	}
	if err := consumeSecondFactorTx(ctx, tx, uid, proof); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM totp_secret WHERE user_id = $1`, int64(uid)); err != nil {
		return fmt.Errorf("delete totp secret: %w", err)
	}
	// Recovery codes guard the FACTORS, not the authenticator specifically. This
	// used to delete them unconditionally, which was correct while TOTP was the
	// only factor there could be — and became a lockout the moment e-mail could
	// be enrolled too (ADR-37): disabling TOTP would leave an account holding an
	// e-mail factor with no way past the reset-link guard that deliberately
	// refuses it. Read under the user lock already held, so a concurrent e-mail
	// disable cannot have both paths conclude the other factor survives.
	var emailLeft bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM email_factor WHERE user_id = $1 AND confirmed_at IS NOT NULL
		)`, int64(uid)).Scan(&emailLeft); err != nil {
		return fmt.Errorf("disable totp check email factor: %w", err)
	}
	if !emailLeft {
		if _, err := tx.Exec(ctx, `DELETE FROM recovery_code WHERE user_id = $1`, int64(uid)); err != nil {
			return fmt.Errorf("delete recovery codes: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE app_user SET token_version = token_version + 1, updated_at = now()
		WHERE id = $1 AND token_version = $2`, int64(uid), tokenVersion); err != nil {
		return fmt.Errorf("disable totp bump epoch: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE session SET revoked_at = now(), revoked_reason = $3
		WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`,
		int64(uid), sessionID, ReasonPasswordChanged); err != nil {
		return fmt.Errorf("disable totp revoke sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("disable totp commit: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────
// Recovery codes
// ─────────────────────────────────────────────────────────────────────

func (r *Repository) RegenerateRecoveryCodes(ctx context.Context, uid authctx.UserID, sessionID int64,
	tokenVersion int, password string, proof SecondFactorProof, hashes [][]byte) error {

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("regenerate recovery codes begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var passwordHash *string
	var liveVersion int
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT password_hash, token_version, status
		FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(uid)).
		Scan(&passwordHash, &liveVersion, &status); err != nil {
		return fmt.Errorf("regenerate recovery codes lock user: %w", err)
	}
	if status != StatusActive || liveVersion != tokenVersion {
		return ErrSessionInvalid
	}
	if passwordHash == nil {
		return ErrPasswordMissing
	}
	if !pwhash.Verify(*passwordHash, password) {
		return ErrBadCredentials
	}
	if err := requireLiveSessionTx(ctx, tx, uid, sessionID); err != nil {
		return err
	}
	if err := consumeSecondFactorTx(ctx, tx, uid, proof); err != nil {
		return err
	}
	if err := replaceRecoveryCodesTx(ctx, tx, uid, hashes); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("regenerate recovery codes commit: %w", err)
	}
	return nil
}

func replaceRecoveryCodesTx(ctx context.Context, tx pgx.Tx, uid authctx.UserID, hashes [][]byte) error {
	if _, err := tx.Exec(ctx, `DELETE FROM recovery_code WHERE user_id = $1`, int64(uid)); err != nil {
		return fmt.Errorf("clear recovery codes: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO recovery_code (user_id, code_hash)
		SELECT $1, code_hash FROM unnest($2::bytea[]) AS code_hash`, int64(uid), hashes); err != nil {
		return fmt.Errorf("insert recovery codes: %w", err)
	}
	return nil
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

// CreateEmailOTP stores a one-time code digest. Six-digit login codes use a
// keyed, context-bound MAC; high-entropy e-mail verification links use SHA-256.
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

// CreateEmailVerification coalesces rapid authenticated resends while keeping
// token superseding and publication in one transaction.
func (r *Repository) CreateEmailVerification(ctx context.Context, uid authctx.UserID,
	ttl, cooldown time.Duration, draft MailDraft) (string, error) {
	raw, hash, err := secrets.NewToken()
	if err != nil {
		return "", fmt.Errorf("verification token: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("create verification begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var verified bool
	if err := tx.QueryRow(ctx, `
		SELECT email_verified_at IS NOT NULL FROM app_user
		WHERE id = $1 FOR NO KEY UPDATE`, int64(uid)).Scan(&verified); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNoUser
		}
		return "", fmt.Errorf("lock verification user: %w", err)
	}
	if verified {
		return "", ErrEmailAlreadyVerified
	}
	var recent bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM email_otp
			WHERE user_id = $1 AND purpose = $2
			  AND created_at >= now() - $3::interval
		)`, int64(uid), OTPPurposeVerifyEmail, intervalArg(cooldown)).Scan(&recent); err != nil {
		return "", fmt.Errorf("verification cooldown: %w", err)
	}
	if recent {
		return "", ErrTooSoon
	}
	if _, err := tx.Exec(ctx, `
		UPDATE email_otp SET consumed_at = now()
		WHERE user_id = $1 AND purpose = $2 AND consumed_at IS NULL`,
		int64(uid), OTPPurposeVerifyEmail); err != nil {
		return "", fmt.Errorf("supersede verification tokens: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO email_otp (user_id, purpose, code_hash, expires_at)
		VALUES ($1, $2, $3, now() + $4::interval)`,
		int64(uid), OTPPurposeVerifyEmail, hash, intervalArg(ttl)); err != nil {
		return "", fmt.Errorf("insert verification token: %w", err)
	}
	if err := r.enqueueDraft(ctx, tx, draft, raw); err != nil {
		return "", fmt.Errorf("queue verification mail: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("create verification commit: %w", err)
	}
	return raw, nil
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

// CreatePasswordReset mints a reset token, superseding any outstanding one, and
// queues its e-mail in the same transaction.
//
// Same transaction, not "right after": the link exists nowhere but in that
// message, and the 60-second cooldown this method enforces means a user whose
// mail was lost between the commit and the send cannot simply ask again.
func (r *Repository) CreatePasswordReset(ctx context.Context, uid authctx.UserID, ttl time.Duration,
	ip string, draft MailDraft) (string, error) {
	raw, hash, err := secrets.NewToken()
	if err != nil {
		return "", fmt.Errorf("reset token: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("create reset begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var eligible bool
	var tokenVersion int
	if err := tx.QueryRow(ctx, `
		SELECT status = 'active' AND password_hash IS NOT NULL, token_version
		FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(uid)).Scan(&eligible, &tokenVersion); err != nil {
		return "", fmt.Errorf("create reset lock user: %w", err)
	}
	if !eligible {
		return "", ErrResetInvalid
	}

	// Superseding keeps exactly one link live. A user who clicks "forgot
	// password" three times should not leave three usable tokens in three
	// e-mails, each an independent chance for one to be intercepted.
	if _, err := tx.Exec(ctx, `
		UPDATE password_reset SET consumed_at = now()
		WHERE user_id = $1 AND token_version IS NOT NULL AND consumed_at IS NULL`, int64(uid)); err != nil {
		return "", fmt.Errorf("supersede resets: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO password_reset (user_id, token_hash, token_version, expires_at, requested_ip)
		VALUES ($1, $2, $3, now() + $4::interval, $5)`,
		int64(uid), hash, tokenVersion, intervalArg(ttl), nullIP(ip)); err != nil {
		return "", fmt.Errorf("insert reset: %w", err)
	}
	if err := r.enqueueDraft(ctx, tx, draft, raw); err != nil {
		return "", fmt.Errorf("queue reset mail: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("create reset commit: %w", err)
	}
	return raw, nil
}

// CreateAdminPasswordRecovery mints a reset token for the target's verified
// mailbox and queues the message carrying it in the SAME transaction, so the
// token and the mail that delivers it cannot exist without each other.
//
// This used to block on SMTP inside the transaction, and rolled the token back
// when the transport refused. The property that arrangement protected — an
// administrator never installs a credential the target does not receive — now
// comes from DURABILITY instead: the message is in the outbox, so "committed"
// implies "will be delivered or recorded as failed". What changed is that a
// transient provider blip no longer denies the administrator an operation they
// are entitled to perform.
//
// It does not touch the password, sessions, token epoch or second factor; those
// change only when the target consumes the token through ConsumePasswordReset.
func (r *Repository) CreateAdminPasswordRecovery(ctx context.Context, uid authctx.UserID,
	ttl time.Duration, draftFor func(email, storedLocale string) MailDraft) error {

	raw, hash, err := secrets.NewToken()
	if err != nil {
		return fmt.Errorf("admin recovery token: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("admin recovery begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var email, status, storedLocale string
	var verifiedAt *time.Time
	var tokenVersion int
	err = tx.QueryRow(ctx, `
		SELECT email, status, email_verified_at, token_version, coalesce(locale, '')
		FROM app_user WHERE id = $1
		FOR NO KEY UPDATE`, int64(uid)).Scan(&email, &status, &verifiedAt, &tokenVersion, &storedLocale)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoUser
	}
	if err != nil {
		return fmt.Errorf("admin recovery target: %w", err)
	}
	if status != StatusActive || verifiedAt == nil {
		return ErrRecoveryUnavailable
	}

	if _, err := tx.Exec(ctx, `
		UPDATE password_reset SET consumed_at = now()
		WHERE user_id = $1 AND token_version IS NOT NULL AND consumed_at IS NULL`, int64(uid)); err != nil {
		return fmt.Errorf("admin recovery supersede: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO password_reset (user_id, token_hash, token_version, expires_at)
		VALUES ($1, $2, $3, now() + $4::interval)`, int64(uid), hash, tokenVersion, intervalArg(ttl)); err != nil {
		return fmt.Errorf("admin recovery insert: %w", err)
	}
	if err := r.enqueueDraft(ctx, tx, draftFor(email, storedLocale), raw); err != nil {
		return fmt.Errorf("queue admin recovery mail: %w", err)
	}
	// The post-delivery re-check that used to sit here is GONE, and its absence
	// is the point rather than an oversight. It existed because the send blocked
	// on the SMTP timeout INSIDE this transaction, opening a window in which the
	// account could be disabled or its address changed while we waited. Queuing
	// is instant and the row stays locked FOR NO KEY UPDATE from the read above
	// to the commit below, so there is no longer a window to re-check.
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("admin recovery commit: %w", err)
	}
	return nil
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
	var resetVersion int
	err = tx.QueryRow(ctx, `
		SELECT user_id, token_version FROM password_reset
		WHERE token_hash = $1 AND token_version IS NOT NULL
		  AND consumed_at IS NULL AND expires_at > now()`,
		secrets.Hash(rawToken)).Scan(&uid, &resetVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrResetInvalid
	}
	if err != nil {
		return User{}, fmt.Errorf("resolve reset: %w", err)
	}

	var status string
	var liveVersion int
	if err := tx.QueryRow(ctx, `
		SELECT status, token_version FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, uid).
		Scan(&status, &liveVersion); err != nil {
		return User{}, fmt.Errorf("lock reset user: %w", err)
	}
	if status != StatusActive || liveVersion != resetVersion {
		return User{}, ErrResetInvalid
	}
	ct, err := tx.Exec(ctx, `
		UPDATE password_reset SET consumed_at = now()
		WHERE token_hash = $1 AND user_id = $2
		  AND token_version = $3
		  AND consumed_at IS NULL AND expires_at > now()`,
		secrets.Hash(rawToken), uid, liveVersion)
	if err != nil {
		return User{}, fmt.Errorf("consume reset: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return User{}, ErrResetInvalid
	}

	// A reset also VERIFIES the address: the user just proved they read mail
	// sent to it. Leaving email_verified_at null after that would ask them to
	// prove the same fact twice.
	u, err := scanUser(tx.QueryRow(ctx, `
		UPDATE app_user
		SET password_hash = $2,
		    token_version = token_version + 1,
		    email_verified_at = COALESCE(email_verified_at, now()),
		    updated_at = now()
		WHERE id = $1 AND status = 'active' AND token_version = $3
		RETURNING `+userColumns, uid, hash, liveVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrResetInvalid
	}
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
