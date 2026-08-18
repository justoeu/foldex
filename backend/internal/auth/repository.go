package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/mailer"
	"foldex/internal/mailoutbox"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/pgerr"
	"foldex/internal/pkg/pwhash"
	"foldex/internal/pkg/secrets"
)

// Repository owns every table in the identity half of the schema.
//
// Unlike the content repositories, its methods do NOT take a `uid` first
// parameter: these queries either resolve WHO the caller is (so there is no
// principal yet) or operate on the identity tables as an administrator. That
// is exactly why internal/security's TestNoUnscopedTenantQueries only inspects
// the CONTENT tables — an auth query without a user_id predicate is normal,
// while a link query without one is a leak.
type Repository struct {
	pool   *pgxpool.Pool
	outbox *mailoutbox.Outbox
}

// RepositoryOption wires an optional dependency.
//
// Optional rather than a constructor parameter because the outbox is only
// needed by the handful of methods that mint a credential someone has to be
// told about; every other caller — and every test that exercises one — would
// otherwise have to build a cipher to look up a session.
type RepositoryOption func(*Repository)

// WithOutbox makes credential-minting methods queue their e-mail in the SAME
// transaction as the credential. Without it those methods still work and simply
// send nothing, which is what keeps a repository built for a session lookup from
// needing an encryption key.
func WithOutbox(o *mailoutbox.Outbox) RepositoryOption {
	return func(r *Repository) { r.outbox = o }
}

func NewRepository(pool *pgxpool.Pool, opts ...RepositoryOption) *Repository {
	r := &Repository{pool: pool}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// MailDraft is the message a credential-minting method queues alongside the
// credential itself.
//
// Build receives the raw token the method just minted — the value that exists
// nowhere else, since the table stores only its hash — which is why the message
// cannot be built by the caller beforehand. A zero MailDraft queues nothing.
type MailDraft struct {
	Locale string
	Build  func(rawToken string) mailer.Envelope
}

// enqueueDraft queues the draft inside the caller's transaction.
//
// The transaction is the whole mechanism: a message written here commits with
// the credential it describes, so neither a crash nor a deploy can leave a live
// reset token whose e-mail was never sent.
func (r *Repository) enqueueDraft(ctx context.Context, tx pgx.Tx, d MailDraft, rawToken string) error {
	if d.Build == nil {
		return nil
	}
	if r.outbox == nil {
		return errors.New("auth: mail draft supplied to a repository built without an outbox")
	}
	return r.outbox.EnqueueTx(ctx, tx, d.Build(rawToken), d.Locale)
}

// EnqueueMail queues a message that stands on its own — a warning about a
// replayed session, a notice that a recovery code was spent.
//
// These are not tied to a credential, so they open their own short transaction.
// They still go through the outbox rather than a fire-and-forget goroutine
// because "your sessions were signed out" is exactly the message a restart must
// not eat.
func (r *Repository) EnqueueMail(ctx context.Context, env mailer.Envelope, locale string) error {
	if r.outbox == nil {
		return errors.New("auth: repository built without an outbox")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("enqueue mail begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := r.outbox.EnqueueTx(ctx, tx, env, locale); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Sentinel errors. Handlers map these onto responses; the mapping is
// deliberately many-to-one on the login path (see handler.go).
var (
	ErrNoUser         = errors.New("auth: user not found")
	ErrBadCredentials = errors.New("auth: invalid credentials")
	ErrSessionInvalid = errors.New("auth: session invalid")
	ErrSessionReuse   = errors.New("auth: refresh token reuse detected")
	ErrInviteInvalid  = errors.New("auth: invite invalid")
	ErrAlreadySetUp   = errors.New("auth: instance already has an active account")
	ErrEmailTaken     = errors.New("auth: e-mail already registered")
	ErrLastAdmin      = errors.New("auth: cannot remove the last active administrator")
	// ErrOwnerImmutable guards the one account that must always be able to
	// administer the instance. The owner's role and status move only through
	// TransferOwnership, which hands the seat to someone else in the same
	// statement — so there is no instant with zero owners, and no sequence of
	// ordinary edits that reaches one.
	ErrOwnerImmutable = errors.New("auth: the owner's role and status change only by transfer")
	// ErrNotTransferable marks a transfer target that cannot hold the seat.
	ErrNotTransferable = errors.New("auth: ownership can only pass to another active account")
	ErrSelfTarget      = errors.New("auth: cannot perform this action on your own account")
	ErrUserNotActive   = errors.New("auth: account is not active")
	ErrPasswordMissing = errors.New("auth: account has no password credential")
	ErrPasswordExists  = errors.New("auth: account already has a password credential")
	ErrInviteNotFound  = errors.New("auth: invite not found")
	ErrSessionNotFound = errors.New("auth: session not found")
	// ErrInviteEmailMismatch means a provider account tried to claim an
	// invitation issued to a different address.
	ErrInviteEmailMismatch = errors.New("auth: provider address does not match the invitation")
)

// userColumns is the projection behind every User the API returns.
//
// totp_enabled is derived with EXISTS rather than cached on app_user so there
// is one source of truth: an account has a second factor exactly when it has a
// CONFIRMED totp_secret row. A boolean column would need updating in four
// places (enroll, confirm, disable, admin reset) and would silently disagree
// with reality the first time one was missed — and the direction it disagrees
// in decides whether a login demands a code the user cannot produce.
//
// EVERY column is qualified with app_user, and that is load-bearing rather than
// tidy: UserByIdentity selects this projection from `app_user JOIN
// user_identity`, and user_identity carries its own `created_at` and
// `last_login_at`. Unqualified, those two are ambiguous and Postgres refuses
// the whole query — which surfaced as an opaque "server error" on the Google
// login path, in a branch no single-table test could reach.
const userColumns = `app_user.id, app_user.email, app_user.name, app_user.role, app_user.status,
	app_user.email_verified_at, app_user.last_login_at, app_user.created_at,
	(app_user.password_hash IS NOT NULL) AS has_password,
	EXISTS (SELECT 1 FROM totp_secret ts
	         WHERE ts.user_id = app_user.id AND ts.confirmed_at IS NOT NULL) AS totp_enabled,
	coalesce(app_user.locale, ''),
	app_user.token_version`

// userDest is the ONE destination list for userColumns, and the only reason it
// is a function rather than inlined into scanUser.
//
// verifyPassword selects `userColumns, password_hash` and therefore cannot call
// scanUser. It used to carry its own hand-written destination list, which meant
// every column added to userColumns had to be mirrored there by memory —
// exactly the mirror that was missed when `locale` landed, turning every single
// login into a 500 that said only "13 and 12". A trailing variadic keeps the
// shared prefix in one place, so a new column is one edit and the compiler's
// silence is no longer load-bearing.
func userDest(u *User, id *int64, extra ...any) []any {
	return append([]any{id, &u.Email, &u.Name, &u.Role, &u.Status,
		&u.EmailVerifiedAt, &u.LastLoginAt, &u.CreatedAt, &u.HasPassword, &u.TOTPEnabled,
		&u.Locale, &u.TokenVersion}, extra...)
}

func scanUser(row pgx.Row) (User, error) {
	var u User
	var id int64
	err := row.Scan(userDest(&u, &id)...)
	u.ID = authctx.UserID(id)
	return u, err
}

// ─────────────────────────────────────────────────────────────────────
// Users
// ─────────────────────────────────────────────────────────────────────

// GetUser loads one account by id.
func (r *Repository) GetUser(ctx context.Context, id authctx.UserID) (User, error) {
	u, err := scanUser(r.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM app_user WHERE id = $1`, int64(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNoUser
	}
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

// ListUsers returns every account, oldest first. Admin-only surface.
func (r *Repository) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+userColumns+` FROM app_user ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// verifyPassword loads the stored hash and compares it, returning the account
// on success.
//
// This is the ONLY place password_hash is read, and it never leaves the
// function. The `found` return exists so the caller can run bcrypt against a
// dummy hash when the e-mail does not exist: skipping the hash for an unknown
// address is the classic ~80 ms enumeration oracle (SDD §9.2).
func (r *Repository) verifyPassword(ctx context.Context, email, password string) (u User, found bool, err error) {
	var hash *string
	var id int64
	row := r.pool.QueryRow(ctx, `
		SELECT `+userColumns+`, password_hash
		FROM app_user WHERE email_normalized = $1`, NormalizeEmail(email))
	err = row.Scan(userDest(&u, &id, &hash)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("verify password: %w", err)
	}
	u.ID = authctx.UserID(id)
	if hash == nil || !pwhash.Verify(*hash, password) {
		return User{}, true, ErrBadCredentials
	}
	return u, true, nil
}

// NeedsBootstrap reports whether the instance still has no active account, in
// which case /api/auth/bootstrap will claim the placeholder admin.
func (r *Repository) NeedsBootstrap(ctx context.Context) (bool, error) {
	var any bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app_user WHERE status = 'active')`).Scan(&any); err != nil {
		return false, fmt.Errorf("needs bootstrap: %w", err)
	}
	return !any, nil
}

// bootstrapLockKey serializes the bootstrap path.
//
// A transaction-level advisory lock rather than a row lock, because the row the
// operation targets may not exist: on a database whose app_user table is empty
// there is nothing to SELECT ... FOR UPDATE, and two concurrent requests would
// each see "no active account" and each INSERT an administrator. The lock
// releases with the transaction, so a crashed request cannot wedge setup.
const bootstrapLockKey = 0x666F6C6465785F62 // "foldex_b"

// Bootstrap turns the instance's first account into an active administrator.
//
// It prefers to CLAIM the placeholder row migration 000017 inserts, so that on
// an upgraded install the new admin inherits every pre-existing link, note,
// folder and tag — that adoption is the entire reason the placeholder exists.
// When there is no placeholder it inserts a fresh admin instead: a database
// restored from a dump that predates the migration, or one whose placeholder
// was deleted by hand, must still be recoverable through the setup screen
// rather than requiring direct SQL.
//
// The "is anyone active yet" guard runs INSIDE the transaction, under the
// advisory lock. Checking it outside would be a textbook check-then-act on the
// most privileged operation in the product.
func (r *Repository) Bootstrap(ctx context.Context, email, name, password string) (User, error) {
	hash, err := pwhash.Hash(password)
	if err != nil {
		return User{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("bootstrap begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(bootstrapLockKey)); err != nil {
		return User{}, fmt.Errorf("bootstrap lock: %w", err)
	}

	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app_user WHERE status = 'active')`).Scan(&exists); err != nil {
		return User{}, fmt.Errorf("bootstrap check: %w", err)
	}
	if exists {
		return User{}, ErrAlreadySetUp
	}

	norm := NormalizeEmail(email)
	trimmed := strings.TrimSpace(email)

	var placeholderID int64
	// Matches 'owner' as well as 'admin': migration 000032 promotes the oldest
	// administrator, and on an instance that never completed setup that IS the
	// pending placeholder. Looking only for 'admin' would miss it and fall
	// through to the INSERT below, which the single-owner index then rejects —
	// a setup screen that fails permanently on exactly the installs this
	// placeholder exists to serve.
	err = tx.QueryRow(ctx, `
		SELECT id FROM app_user
		WHERE role IN ('owner', 'admin') AND status = 'pending'
		ORDER BY id ASC LIMIT 1`).Scan(&placeholderID)
	switch {
	case err == nil:
		// Claim it — this is the path that adopts the pre-migration content.
	case errors.Is(err, pgx.ErrNoRows):
		placeholderID = 0
	default:
		return User{}, fmt.Errorf("bootstrap find placeholder: %w", err)
	}

	var u User
	if placeholderID != 0 {
		u, err = scanUser(tx.QueryRow(ctx, `
			UPDATE app_user
			SET email = $2, email_normalized = $3, name = $4, password_hash = $5,
			    status = 'active', role = 'owner', email_verified_at = now(), updated_at = now()
			WHERE id = $1
			RETURNING `+userColumns, placeholderID, trimmed, norm, name, hash))
	} else {
		u, err = scanUser(tx.QueryRow(ctx, `
			INSERT INTO app_user (email, email_normalized, name, password_hash, role, status, email_verified_at)
			VALUES ($1, $2, $3, $4, 'owner', 'active', now())
			RETURNING `+userColumns, trimmed, norm, name, hash))
	}
	if err != nil {
		if pgerr.UniqueConstraint(err) == "app_user_email_norm_uniq" {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("bootstrap claim: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("bootstrap commit: %w", err)
	}
	return u, nil
}

// SetPassword adds a password only while the caller's session, credential epoch
// and optional TOTP proof are still current. The credential write and required
// revocation of every other session commit together.
func (r *Repository) SetPassword(ctx context.Context, id authctx.UserID, keepSession int64,
	tokenVersion int, password string, proof *TOTPProof) error {

	hash, err := pwhash.Hash(password)
	if err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("set password begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentHash *string
	var liveVersion int
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT password_hash, token_version, status
		FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(id)).
		Scan(&currentHash, &liveVersion, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoUser
		}
		return fmt.Errorf("set password lock user: %w", err)
	}
	if status != StatusActive || liveVersion != tokenVersion {
		return ErrSessionInvalid
	}
	if currentHash != nil {
		return ErrPasswordExists
	}
	if err := requireLiveSessionTx(ctx, tx, id, keepSession); err != nil {
		return err
	}

	var totpEnabled bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM totp_secret
		               WHERE user_id = $1 AND confirmed_at IS NOT NULL)`, int64(id)).Scan(&totpEnabled); err != nil {
		return fmt.Errorf("set password check totp: %w", err)
	}
	if totpEnabled {
		if proof == nil {
			return ErrTOTPReplay
		}
		if err := consumeTOTPProofTx(ctx, tx, id, *proof); err != nil {
			return err
		}
	}

	ct, err := tx.Exec(ctx, `
		UPDATE app_user
		SET password_hash = $2, token_version = token_version + 1, updated_at = now()
		WHERE id = $1 AND status = 'active' AND token_version = $3 AND password_hash IS NULL`,
		int64(id), hash, tokenVersion)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrSessionInvalid
	}
	if _, err := tx.Exec(ctx, `
		UPDATE session SET revoked_at = now(), revoked_reason = $3
		WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`,
		int64(id), keepSession, ReasonPasswordChanged); err != nil {
		return fmt.Errorf("set password revoke: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("set password commit: %w", err)
	}
	return nil
}

// ChangePassword locks the credential row before verifying and replacing it,
// then revokes every other session in the same transaction. Two requests with
// the same old password serialize at the lock, so the second verifies against
// the winner's new hash and cannot overwrite it.
func (r *Repository) ChangePassword(ctx context.Context, id authctx.UserID, keepSession int64,
	currentPassword, newPassword string) error {

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("change password begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentHash *string
	if err := tx.QueryRow(ctx, `
		SELECT password_hash FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(id)).
		Scan(&currentHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoUser
		}
		return fmt.Errorf("change password load: %w", err)
	}
	if currentHash == nil {
		return ErrPasswordMissing
	}
	if !pwhash.Verify(*currentHash, currentPassword) {
		return ErrBadCredentials
	}
	newHash, err := pwhash.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("change password hash: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE app_user
		SET password_hash = $2, token_version = token_version + 1, updated_at = now()
		WHERE id = $1`, int64(id), newHash); err != nil {
		return fmt.Errorf("change password update: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE session SET revoked_at = now(), revoked_reason = $3
		WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`,
		int64(id), keepSession, ReasonPasswordChanged); err != nil {
		return fmt.Errorf("change password revoke: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("change password commit: %w", err)
	}
	return nil
}

// VerifyUserPasswordEpoch verifies the password and returns the credential
// epoch read with its hash. A caller can require the epoch to remain unchanged
// before persisting a proof derived from this check.
func (r *Repository) VerifyUserPasswordEpoch(ctx context.Context, id authctx.UserID, password string) (int, error) {
	var hash *string
	var tokenVersion int
	if err := r.pool.QueryRow(ctx,
		`SELECT password_hash, token_version FROM app_user WHERE id = $1`, int64(id)).Scan(&hash, &tokenVersion); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNoUser
		}
		return 0, fmt.Errorf("load password: %w", err)
	}
	if hash == nil {
		return 0, ErrPasswordMissing
	}
	if !pwhash.Verify(*hash, password) {
		return 0, ErrBadCredentials
	}
	return tokenVersion, nil
}

// adminGuardLockKey serializes every operation that can change the number of
// active administrators.
//
// The guard is read-then-write — count the admins, then demote one — so under
// concurrency two requests targeting the last two admins can both observe "2"
// and both proceed, leaving zero. That state is unrecoverable through the API;
// only a direct database edit fixes it. A transaction-level advisory lock is
// the cheapest correct answer: an aggregate cannot be SELECT ... FOR UPDATE'd,
// and locking every admin row would still race with an INSERT.
const adminGuardLockKey = 0x666F6C6465785F61 // "foldex_a"

// guardLastAdminTx refuses, inside tx, an operation that would leave the
// instance with no active administrator.
//
// Callers must already hold the advisory lock. `target` is the row about to be
// demoted, disabled or deleted.
func guardLastAdminTx(ctx context.Context, tx pgx.Tx, target authctx.UserID) error {
	var role, status string
	err := tx.QueryRow(ctx,
		`SELECT role, status FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(target)).Scan(&role, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoUser
	}
	if err != nil {
		return fmt.Errorf("guard last admin: %w", err)
	}
	// Only removing an ACTIVE administrator can reduce the count. IsAdmin, not
	// an equality test, because owner administers too — and it is the row most
	// likely to be the last one standing.
	if !authctx.Role(role).IsAdmin() || status != StatusActive {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM app_user WHERE role IN ('owner', 'admin') AND status = 'active'`).Scan(&n); err != nil {
		return fmt.Errorf("guard count admins: %w", err)
	}
	if n <= 1 {
		return ErrLastAdmin
	}
	return nil
}

// UpdateUser applies an admin's edits, refusing any that would remove the last
// active administrator. Nil fields are left untouched.
//
// The last-admin check runs INSIDE this method's transaction, under the
// advisory lock, because it is only meaningful when it cannot interleave with
// another admin-mutating request. The self-target guard stays in the handler:
// it needs the CALLER's identity and cannot race, since it compares the caller
// against themselves. A promotion revokes every existing session before this
// transaction commits, so the new role is never inherited by an old login.
// UpdateOwnProfile writes the two fields an account controls about itself.
//
// Separate from UpdateUser, which is the ADMINISTRATIVE path: it carries the
// instance-wide advisory lock, the last-administrator guard and the owner
// immutability rule, none of which a self-service edit can trigger. Threading
// locale through there would also put it on a surface admins reach, and an
// administrator has no business choosing someone else's reading language.
//
// locale is TRI-STATE, the same shape the master-password hint uses: nil keeps
// the stored value, "" clears it back to "no preference", and a value sets it.
// Clearing has to be expressible — without it, a user who picked a language once
// could never go back to following their browser.
func (r *Repository) UpdateOwnProfile(ctx context.Context, id authctx.UserID, name string, locale *string) (User, error) {
	u, err := scanUser(r.pool.QueryRow(ctx, `
		UPDATE app_user SET
			name       = $2,
			locale     = CASE WHEN $3::bool THEN nullif($4, '') ELSE locale END,
			updated_at = now()
		WHERE id = $1
		RETURNING `+userColumns, int64(id), name, locale != nil, derefOr(locale, "")))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNoUser
	}
	if err != nil {
		return User{}, fmt.Errorf("update own profile: %w", err)
	}
	return u, nil
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

func (r *Repository) UpdateUser(ctx context.Context, id authctx.UserID, name *string, role *authctx.Role, status *string) (User, error) {
	// Pure rename fast path: with no role/status to change, the last-admin
	// guard can never fire, so the instance-wide admin-guard advisory lock —
	// which serializes every admin user-edit — buys nothing while letting a
	// rename-happy account contend with real administration. A plain UPDATE
	// is also the correct isolation: nothing below reads-then-writes.
	if role == nil && status == nil {
		// name is also optional in the DTO, so `PATCH {}` reaches here with all
		// three nil. Dereferencing it would panic — recovered as a 500, but a
		// crash path is not an input-validation answer. An edit that changes
		// nothing returns the row unchanged.
		if name == nil {
			return r.GetUser(ctx, id)
		}
		u, err := scanUser(r.pool.QueryRow(ctx, `
			UPDATE app_user SET name = $2, updated_at = now()
			WHERE id = $1
			RETURNING `+userColumns, int64(id), *name))
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNoUser
		}
		if err != nil {
			return User{}, fmt.Errorf("update user rename: %w", err)
		}
		return u, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("update user begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(adminGuardLockKey)); err != nil {
		return User{}, fmt.Errorf("update user lock: %w", err)
	}
	var previousRole authctx.Role
	if err := tx.QueryRow(ctx, `SELECT role FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(id)).Scan(&previousRole); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNoUser
		}
		return User{}, fmt.Errorf("update user role: %w", err)
	}
	// The owner is out of reach of ordinary edits. Checked here, inside the
	// advisory lock, rather than in the handler: the handler would have to read
	// the row first, and between that read and this write a transfer could move
	// the seat — leaving the edit applied to whoever now holds it.
	if previousRole == authctx.RoleOwner && (role != nil || status != nil) {
		return User{}, ErrOwnerImmutable
	}
	demoting := role != nil && !role.IsAdmin()
	disabling := status != nil && *status == StatusDisabled
	if demoting || disabling {
		if err := guardLastAdminTx(ctx, tx, id); err != nil {
			return User{}, err
		}
	}

	u, err := scanUser(tx.QueryRow(ctx, `
		UPDATE app_user SET
			name   = COALESCE($2, name),
			role   = COALESCE($3, role),
			status = COALESCE($4, status),
			-- Any role or status change invalidates cached authorization, so the
			-- token version moves and every resolved principal is re-derived.
			token_version = token_version + CASE WHEN $3 IS NOT NULL OR $4 IS NOT NULL THEN 1 ELSE 0 END,
			updated_at = now()
		WHERE id = $1
		RETURNING `+userColumns, int64(id), name, role, status))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNoUser
	}
	if err != nil {
		return User{}, fmt.Errorf("update user: %w", err)
	}
	if role != nil && !previousRole.IsAdmin() && role.IsAdmin() {
		if _, err := tx.Exec(ctx, `
			UPDATE session SET revoked_at = now(), revoked_reason = $2
			WHERE user_id = $1 AND revoked_at IS NULL`, int64(id), ReasonAdminRevoked); err != nil {
			return User{}, fmt.Errorf("revoke sessions on admin promotion: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("update user commit: %w", err)
	}
	return u, nil
}

// DeleteUser removes an account. Every content row cascades away with it via
// the ON DELETE CASCADE that migration 000017 put on user_id — deleting a user
// is a schema concern, not Go code, which is why internal/auth never imports
// links/notes/folders/tags.
func (r *Repository) DeleteUser(ctx context.Context, id authctx.UserID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete user begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(adminGuardLockKey)); err != nil {
		return fmt.Errorf("delete user lock: %w", err)
	}
	var targetRole authctx.Role
	switch err := tx.QueryRow(ctx,
		`SELECT role FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(id)).Scan(&targetRole); {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNoUser
	case err != nil:
		return fmt.Errorf("delete user role: %w", err)
	case targetRole == authctx.RoleOwner:
		// Deleting the owner would take every row it owns with it by cascade AND
		// leave the seat empty, which no API call could then fill.
		return ErrOwnerImmutable
	}
	if err := guardLastAdminTx(ctx, tx, id); err != nil {
		return err
	}
	ct, err := tx.Exec(ctx, `DELETE FROM app_user WHERE id = $1`, int64(id))
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNoUser
	}
	return tx.Commit(ctx)
}

// TransferOwnership hands the instance to another active account, demoting the
// outgoing owner to admin.
//
// Both rows move in ONE statement. The partial unique index allows a single
// owner, and it is checked per statement, so promoting first and demoting
// second would fail on the promotion while demoting first would leave the
// instance ownerless for the width of the transaction — the exact state the
// index exists to forbid. A CASE expression over both ids sidesteps the
// ordering question entirely.
//
// The target must be ACTIVE: handing the seat to a disabled or still-pending
// account is a lockout that no remaining role could undo, since only the owner
// may transfer.
func (r *Repository) TransferOwnership(ctx context.Context, from, to authctx.UserID) (User, error) {
	if from == to {
		return User{}, ErrSelfTarget
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("transfer begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(adminGuardLockKey)); err != nil {
		return User{}, fmt.Errorf("transfer lock: %w", err)
	}

	// Lock both rows in id order. Two concurrent transfers taking them in
	// opposite orders would otherwise deadlock, and the advisory lock above
	// already serializes admin edits — this keeps the guarantee if that lock is
	// ever narrowed.
	first, second := from, to
	if second < first {
		first, second = second, first
	}
	roles := map[authctx.UserID]struct {
		role   authctx.Role
		status string
	}{}
	for _, id := range []authctx.UserID{first, second} {
		var role authctx.Role
		var status string
		err := tx.QueryRow(ctx,
			`SELECT role, status FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(id)).Scan(&role, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNoUser
		}
		if err != nil {
			return User{}, fmt.Errorf("transfer read: %w", err)
		}
		roles[id] = struct {
			role   authctx.Role
			status string
		}{role, status}
	}
	if roles[from].role != authctx.RoleOwner {
		return User{}, ErrOwnerImmutable
	}
	if roles[to].status != StatusActive {
		return User{}, ErrNotTransferable
	}

	// Not RETURNING: this statement touches two rows, so a QueryRow over it
	// would read whichever one Postgres happened to emit first. The new owner is
	// re-read explicitly below.
	if _, err := tx.Exec(ctx, `
		UPDATE app_user SET
			role = CASE WHEN id = $1 THEN 'admin' ELSE 'owner' END,
			-- Both principals' cached authorization is now wrong, so both epochs
			-- move and every resolved principal is re-derived.
			token_version = token_version + 1,
			updated_at = now()
		WHERE id IN ($1, $2)`, int64(from), int64(to)); err != nil {
		return User{}, fmt.Errorf("transfer: %w", err)
	}
	u, err := scanUser(tx.QueryRow(ctx,
		`SELECT `+userColumns+` FROM app_user WHERE app_user.id = $1`, int64(to)))
	if err != nil {
		return User{}, fmt.Errorf("transfer read new owner: %w", err)
	}

	// Neither account keeps a session: the outgoing owner's tokens carry a role
	// they no longer hold, and the incoming owner's carry one they have just
	// outgrown. Both re-authenticate, and the admin-2FA policy is re-evaluated
	// against the new roles on the way back in.
	if _, err := tx.Exec(ctx, `
		UPDATE session SET revoked_at = now(), revoked_reason = $2
		WHERE user_id IN ($1, $3) AND revoked_at IS NULL`,
		int64(from), ReasonAdminRevoked, int64(to)); err != nil {
		return User{}, fmt.Errorf("transfer revoke sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("transfer commit: %w", err)
	}
	return u, nil
}

// CreateInvite issues (or replaces) the open invitation for an e-mail and
// returns the row plus the RAW token, which exists only in this return value.
//
// Re-inviting revokes the previous open invite rather than erroring: the
// partial unique index allows one live invite per address, and an admin who
// clicks "invite" twice means "send it again", not "fail".
func (r *Repository) CreateInvite(ctx context.Context, email string, role authctx.Role,
	invitedBy authctx.UserID, ttl time.Duration, draft MailDraft) (Invite, string, error) {
	raw, hash, err := secrets.NewToken()
	if err != nil {
		return Invite{}, "", err
	}
	norm := NormalizeEmail(email)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Invite{}, "", fmt.Errorf("invite begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var taken bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app_user WHERE email_normalized = $1)`, norm).Scan(&taken); err != nil {
		return Invite{}, "", fmt.Errorf("invite email check: %w", err)
	}
	if taken {
		return Invite{}, "", ErrEmailTaken
	}
	if _, err := tx.Exec(ctx, `
		UPDATE invite SET revoked_at = now()
		WHERE email_normalized = $1 AND accepted_at IS NULL AND revoked_at IS NULL`, norm); err != nil {
		return Invite{}, "", fmt.Errorf("invite supersede: %w", err)
	}

	var inv Invite
	if err := tx.QueryRow(ctx, `
		INSERT INTO invite (email, email_normalized, role, token_hash, invited_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + $6::interval)
		RETURNING id, email, role, created_at, expires_at, accepted_at`,
		strings.TrimSpace(email), norm, role, hash, int64(invitedBy), ttl.String(),
	).Scan(&inv.ID, &inv.Email, &inv.Role, &inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt); err != nil {
		return Invite{}, "", fmt.Errorf("insert invite: %w", err)
	}
	if err := r.enqueueDraft(ctx, tx, draft, raw); err != nil {
		return Invite{}, "", fmt.Errorf("queue invite mail: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Invite{}, "", fmt.Errorf("invite commit: %w", err)
	}
	return inv, raw, nil
}

// LookupInvite resolves a raw invite token.
//
// The lookup is a sha256 index hit with no query by e-mail anywhere, so the
// endpoint cannot be turned into "does this address have a pending invite?".
// Expired, revoked and already-accepted invites all return ErrInviteInvalid —
// one indistinguishable failure.
func (r *Repository) LookupInvite(ctx context.Context, rawToken string) (Invite, error) {
	var inv Invite
	err := r.pool.QueryRow(ctx, `
		SELECT id, email, role, created_at, expires_at, accepted_at
		FROM invite
		WHERE token_hash = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()`,
		secrets.Hash(rawToken),
	).Scan(&inv.ID, &inv.Email, &inv.Role, &inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, ErrInviteInvalid
	}
	if err != nil {
		return Invite{}, fmt.Errorf("lookup invite: %w", err)
	}
	return inv, nil
}

// ListInvites returns the open invitations for the admin screen. The token
// hash is never selected — there is nothing useful an admin could do with it,
// and the raw value is unrecoverable by design.
func (r *Repository) ListInvites(ctx context.Context) ([]Invite, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, email, role, created_at, expires_at, accepted_at
		FROM invite
		WHERE accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	defer rows.Close()
	out := []Invite{}
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.ID, &inv.Email, &inv.Role, &inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt); err != nil {
			return nil, fmt.Errorf("scan invite: %w", err)
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// RevokeInvite closes an open invitation.
func (r *Repository) RevokeInvite(ctx context.Context, id int64) error {
	ct, err := r.pool.Exec(ctx,
		`UPDATE invite SET revoked_at = now() WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrInviteNotFound
	}
	return nil
}

// AcceptInvite creates the account the invitation describes, in one
// transaction with the consumption of the invite.
//
// The invite row is re-read FOR UPDATE inside the transaction, so two requests
// racing on the same token cannot both create an account: the second finds
// accepted_at already set and fails. Validating the token before the
// transaction and trusting it afterwards would make double-acceptance a matter
// of timing.
func (r *Repository) AcceptInvite(ctx context.Context, rawToken, name, password string) (User, error) {
	hash, err := pwhash.Hash(password)
	if err != nil {
		return User{}, err
	}
	return r.acceptInvite(ctx, inviteBy{tokenHash: secrets.Hash(rawToken)}, name,
		inviteCredential{passwordHash: &hash})
}

// AcceptInviteWithIdentityByID accepts an invitation using a provider account
// instead of a password, producing an account that is provider-only from birth.
//
// Located by id rather than by token because the OAuth round-trip already
// resolved the token at START time and stored the id on the state row — the raw
// token is not carried through Google and back, so it is not available here.
// That is also the safer shape: the token never appears in a redirect URL, in
// browser history, or in Google's logs.
//
// The Google address must equal the invited one. An invitation is issued TO a
// specific mailbox, so letting a leaked link be claimed by any Google account
// would turn "I sent Ana an invite" into "whoever saw the URL is now a user" —
// and silently, since the account would carry the role meant for Ana.
func (r *Repository) AcceptInviteWithIdentityByID(ctx context.Context, inviteID int64, name, provider, subject, email string) (User, error) {
	return r.acceptInvite(ctx, inviteBy{id: &inviteID}, name, inviteCredential{
		identity: &linkedIdentity{provider: provider, subject: subject, email: email},
	})
}

// inviteBy locates the invitation to claim. Exactly one field is set.
type inviteBy struct {
	tokenHash []byte
	id        *int64
}

// inviteCredential is how a newly claimed account will sign in. Exactly one of
// the two is set; the type exists so the shared transaction below cannot be
// called with neither, which would create an account nobody can ever use.
type inviteCredential struct {
	passwordHash *string
	identity     *linkedIdentity
}

type linkedIdentity struct {
	provider string
	subject  string
	email    string
}

func (r *Repository) acceptInvite(ctx context.Context, by inviteBy, name string, cred inviteCredential) (User, error) {
	if cred.passwordHash == nil && cred.identity == nil {
		return User{}, ErrPasswordMissing
	}
	// Fail closed on an empty locator. Both predicates below are written as
	// "$n IS NULL OR …", so a zero-valued inviteBy would match the OLDEST live
	// invitation on the instance and hand the caller an account they were never
	// invited to. No caller does that today; this is what keeps it that way.
	if by.tokenHash == nil && by.id == nil {
		return User{}, ErrInviteInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("accept begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var inviteID int64
	var email string
	var role authctx.Role
	// The liveness predicates are identical for both locators, and stay in ONE
	// statement: an id-located invite that skipped the accepted/revoked/expired
	// checks would let a completed OAuth round-trip claim an invitation revoked
	// while the user was on Google's consent screen.
	err = tx.QueryRow(ctx, `
		SELECT id, email, role FROM invite
		WHERE ($1::bytea IS NULL OR token_hash = $1)
		  AND ($2::bigint IS NULL OR id = $2)
		  AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
		FOR UPDATE`, by.tokenHash, by.id).Scan(&inviteID, &email, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrInviteInvalid
	}
	if err != nil {
		return User{}, fmt.Errorf("accept lookup: %w", err)
	}

	norm := NormalizeEmail(email)
	if cred.identity != nil && NormalizeEmail(cred.identity.email) != norm {
		return User{}, ErrInviteEmailMismatch
	}

	u, err := scanUser(tx.QueryRow(ctx, `
		INSERT INTO app_user (email, email_normalized, name, password_hash, role, status, email_verified_at)
		VALUES ($1, $2, $3, $4, $5, 'active', now())
		RETURNING `+userColumns, strings.TrimSpace(email), norm, name, cred.passwordHash, role))
	if err != nil {
		if pgerr.UniqueConstraint(err) == "app_user_email_norm_uniq" {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("accept insert user: %w", err)
	}
	if cred.identity != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_identity (user_id, provider, subject, email_at_link, last_login_at)
			VALUES ($1, $2, $3, $4, now())`,
			int64(u.ID), cred.identity.provider, cred.identity.subject,
			nullString(cred.identity.email)); err != nil {
			return User{}, mapIdentityConflict(err)
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE invite SET accepted_at = now(), accepted_user_id = $2 WHERE id = $1`,
		inviteID, int64(u.ID)); err != nil {
		return User{}, fmt.Errorf("accept consume: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("accept commit: %w", err)
	}
	return u, nil
}

// ─────────────────────────────────────────────────────────────────────
// Sessions
// ─────────────────────────────────────────────────────────────────────

// resolved is what ResolveAccess returns: the principal plus the CSRF hash the
// middleware needs to validate the header, all from ONE query.
type resolved struct {
	Principal authctx.Principal
	CSRFHash  []byte
	LastSeen  time.Time
	Status    string
}

// ResolveAccess turns an access-token cookie into a principal.
//
// The join onto app_user is not a convenience: it makes "is this session
// valid" and "is its owner still allowed in" a single atomic read. Resolving
// the session first and checking the account separately would leave a window
// where a just-disabled user's in-flight requests still pass.
func (r *Repository) ResolveAccess(ctx context.Context, rawToken string) (resolved, error) {
	var out resolved
	var sid, uid int64
	err := r.pool.QueryRow(ctx, `
		SELECT s.id, s.user_id, u.role, s.csrf_token_hash, s.last_seen_at, u.status
		FROM session s
		JOIN app_user u ON u.id = s.user_id
		WHERE s.access_token_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.access_expires_at > now()`,
		secrets.Hash(rawToken),
	).Scan(&sid, &uid, &out.Principal.Role, &out.CSRFHash, &out.LastSeen, &out.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return resolved{}, ErrSessionInvalid
	}
	if err != nil {
		return resolved{}, fmt.Errorf("resolve access: %w", err)
	}
	if out.Status != StatusActive {
		return resolved{}, ErrUserNotActive
	}
	out.Principal.UserID = authctx.UserID(uid)
	out.Principal.SessionID = sid
	out.Principal.Via = authctx.ViaSession
	return out, nil
}

// TouchSession refreshes last_seen_at.
//
// Called at most once per minute per session by the middleware. An UPDATE on
// every request would turn the busiest table in the schema into a write
// amplifier and generate a dead tuple per page view — the classic session-table
// mistake.
func (r *Repository) TouchSession(ctx context.Context, id int64) {
	_, _ = r.pool.Exec(ctx, `UPDATE session SET last_seen_at = now() WHERE id = $1`, id)
}

// IssueSession creates a brand-new session family for a successful login.
func (r *Repository) IssueSession(ctx context.Context, uid authctx.UserID, tokenVersion int,
	ttl SessionTTL, ip, ua string) (issuedTokens, int64, error) {
	issue, err := newSessionIssue(ttl)
	if err != nil {
		return issuedTokens{}, 0, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return issuedTokens{}, 0, fmt.Errorf("issue session begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedUser int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM app_user
		WHERE id = $1 AND status = 'active' AND token_version = $2
		FOR NO KEY UPDATE`, int64(uid), tokenVersion).Scan(&lockedUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return issuedTokens{}, 0, ErrSessionInvalid
	}
	if err != nil {
		return issuedTokens{}, 0, fmt.Errorf("issue session lock user: %w", err)
	}

	sid, err := issueSessionTx(ctx, tx, uid, issue, ip, ua)
	if err != nil {
		return issuedTokens{}, 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE app_user SET last_login_at = now() WHERE id = $1`, int64(uid)); err != nil {
		return issuedTokens{}, 0, fmt.Errorf("issue session touch user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return issuedTokens{}, 0, fmt.Errorf("issue session commit: %w", err)
	}
	return issue.tokens, sid, nil
}

type issuedSessionHashes struct {
	access  []byte
	refresh []byte
	csrf    []byte
}

type sessionIssue struct {
	tokens issuedTokens
	hashes issuedSessionHashes
}

func newSessionIssue(ttl SessionTTL) (sessionIssue, error) {
	access, accessHash, err := secrets.NewToken()
	if err != nil {
		return sessionIssue{}, err
	}
	refresh, refreshHash, err := secrets.NewToken()
	if err != nil {
		return sessionIssue{}, err
	}
	csrf, csrfHash, err := secrets.NewToken()
	if err != nil {
		return sessionIssue{}, err
	}
	now := time.Now()
	return sessionIssue{
		tokens: issuedTokens{
			Access:        access,
			Refresh:       refresh,
			CSRF:          csrf,
			AccessExpiry:  now.Add(ttl.Access),
			RefreshExpiry: now.Add(ttl.Refresh),
		},
		hashes: issuedSessionHashes{access: accessHash, refresh: refreshHash, csrf: csrfHash},
	}, nil
}

func issueSessionTx(ctx context.Context, tx pgx.Tx, uid authctx.UserID, issue sessionIssue,
	ip, ua string) (int64, error) {
	ua = truncate(ua, 512)
	sid, err := insertSessionTx(ctx, tx, uid, issue, uuid.NewString(), nil, nullIP(ip), &ua)
	if err != nil {
		return 0, fmt.Errorf("issue session: %w", err)
	}
	return sid, nil
}

func insertSessionTx(ctx context.Context, tx pgx.Tx, uid authctx.UserID, issue sessionIssue,
	familyID string, createdAt *time.Time, ip, ua *string) (int64, error) {
	var sid int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO session (user_id, family_id, access_token_hash, access_expires_at,
		                     refresh_token_hash, refresh_expires_at, csrf_token_hash,
		                     created_at, ip, user_agent)
		VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, COALESCE($8::timestamptz, now()), $9, $10)
		RETURNING id`, int64(uid), familyID, issue.hashes.access, issue.tokens.AccessExpiry,
		issue.hashes.refresh, issue.tokens.RefreshExpiry, issue.hashes.csrf, createdAt, ip, ua).Scan(&sid); err != nil {
		return 0, err
	}
	return sid, nil
}

func requireLiveSessionTx(ctx context.Context, tx pgx.Tx, uid authctx.UserID, sessionID int64) error {
	var locked int64
	err := tx.QueryRow(ctx, `
		SELECT id FROM session
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
		  AND access_expires_at > now()
		FOR UPDATE`, sessionID, int64(uid)).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSessionInvalid
	}
	if err != nil {
		return fmt.Errorf("lock live session: %w", err)
	}
	return nil
}

// ListSessions returns the caller's live sessions, newest first.
func (r *Repository) ListSessions(ctx context.Context, uid authctx.UserID, current int64) ([]SessionInfo, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, created_at, last_seen_at, COALESCE(user_agent, ''), COALESCE(host(ip), '')
		FROM session
		WHERE user_id = $1 AND revoked_at IS NULL AND refresh_expires_at > now()
		ORDER BY last_seen_at DESC`, int64(uid))
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	out := []SessionInfo{}
	for rows.Next() {
		var s SessionInfo
		if err := rows.Scan(&s.ID, &s.CreatedAt, &s.LastSeenAt, &s.UserAgent, &s.IP); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		s.Current = s.ID == current
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeSession revokes one session belonging to uid.
//
// The user_id predicate is what makes the id in the URL harmless: revoking by
// id alone would let any authenticated user sign out any other user by
// guessing a dense BIGSERIAL.
func (r *Repository) RevokeSession(ctx context.Context, uid authctx.UserID, id int64, reason string) error {
	ct, err := r.pool.Exec(ctx, `
		UPDATE session SET revoked_at = now(), revoked_reason = $3
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, id, int64(uid), reason)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// RevokeFamilyByTokens revokes every live session in the family named by either
// cookie. A stale refresh cookie can still resolve through session_used_token
// after rotation, which keeps a delayed refresh response from undoing logout.
func (r *Repository) RevokeFamilyByTokens(ctx context.Context, rawAccess, rawRefresh, reason string) error {
	var accessHash, refreshHash []byte
	if rawAccess != "" {
		accessHash = secrets.Hash(rawAccess)
	}
	if rawRefresh != "" {
		refreshHash = secrets.Hash(rawRefresh)
	}
	_, err := r.pool.Exec(ctx, `
		WITH target_family AS MATERIALIZED (
			SELECT family_id FROM session
			WHERE ($1::bytea IS NOT NULL AND access_token_hash = $1)
			   OR ($2::bytea IS NOT NULL AND refresh_token_hash = $2)
			UNION
			SELECT family_id FROM session_used_token
			WHERE $2::bytea IS NOT NULL AND token_hash = $2
		)
		UPDATE session SET revoked_at = now(), revoked_reason = $3
		WHERE family_id IN (SELECT family_id FROM target_family)
		  AND revoked_at IS NULL`, accessHash, refreshHash, reason)
	if err != nil {
		return fmt.Errorf("revoke session family: %w", err)
	}
	return nil
}

// RevokeAllForUser kills every live session and bumps token_version.
func (r *Repository) RevokeAllForUser(ctx context.Context, uid authctx.UserID, reason string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("revoke all begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ct, err := tx.Exec(ctx,
		`UPDATE app_user SET token_version = token_version + 1 WHERE id = $1`, int64(uid))
	if err != nil {
		return fmt.Errorf("revoke all bump: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNoUser
	}
	if _, err := tx.Exec(ctx, `
		UPDATE session SET revoked_at = now(), revoked_reason = $2
		WHERE user_id = $1 AND revoked_at IS NULL`, int64(uid), reason); err != nil {
		return fmt.Errorf("revoke all: %w", err)
	}
	return tx.Commit(ctx)
}

// Sweep deletes long-dead sessions and consumed refresh tokens.
//
// session_used_token entries are kept for a full retention window rather than
// deleted on rotation, because they ARE the reuse detector: forget a consumed
// token and a replay of it looks like an ordinary unknown token (401) instead
// of an attack signal that kills the family.
func (r *Repository) Sweep(ctx context.Context, retain time.Duration) (int64, error) {
	var total int64
	ct, err := r.pool.Exec(ctx,
		`DELETE FROM session WHERE refresh_expires_at < now() - $1::interval`, retain.String())
	if err != nil {
		return 0, fmt.Errorf("sweep sessions: %w", err)
	}
	total += ct.RowsAffected()

	ct, err = r.pool.Exec(ctx,
		`DELETE FROM session_used_token WHERE used_at < now() - $1::interval`, retain.String())
	if err != nil {
		return total, fmt.Errorf("sweep used tokens: %w", err)
	}
	total += ct.RowsAffected()

	ct, err = r.pool.Exec(ctx,
		`DELETE FROM invite WHERE expires_at < now() - $1::interval AND accepted_at IS NULL`, retain.String())
	if err != nil {
		return total, fmt.Errorf("sweep invites: %w", err)
	}
	return total + ct.RowsAffected(), nil
}

func nullIP(ip string) *string {
	if ip == "" {
		return nil
	}
	return &ip
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
