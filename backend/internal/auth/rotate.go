package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/secrets"
)

// SessionTTL groups the three lifetimes a session juggles.
type SessionTTL struct {
	// Access is short because the access cookie is the one sent on every
	// request; a stolen one should expire before it is useful.
	Access time.Duration
	// Refresh slides forward on each rotation.
	Refresh time.Duration
	// Absolute caps the sliding window. Without it a stolen refresh token that
	// is rotated forever is immortal — the sliding expiry alone can never
	// retire it.
	Absolute time.Duration
	// Grace is the window in which re-presenting an already-consumed refresh
	// token is treated as a racing tab rather than an attack. See RotateResult.
	Grace time.Duration
}

// maxLiveSessionsPerFamily caps how many sessions one login may hold at once.
//
// A family is one login on one device. Siblings only appear when requests race
// inside the grace window, and a handful covers every legitimate case (React
// StrictMode's double mount, a few tabs reloading together). The cap is what
// stops one consumed refresh token from minting rows for the whole 10 seconds.
const maxLiveSessionsPerFamily = 5

// DefaultTTL matches the cookie matrix in SDD §5.1.
func DefaultTTL() SessionTTL {
	return SessionTTL{
		Access:   15 * time.Minute,
		Refresh:  30 * 24 * time.Hour,
		Absolute: 90 * 24 * time.Hour,
		Grace:    10 * time.Second,
	}
}

// RotateResult reports what a refresh attempt did.
type RotateResult struct {
	Tokens  issuedTokens
	UserID  authctx.UserID
	Session int64
	// Replayed is true when the presented token had already been consumed
	// inside the grace window. The caller still gets working cookies — the
	// point of the grace window — but the event is worth logging.
	Replayed bool
}

// Rotate exchanges a refresh token for a fresh triple, detecting replay.
//
// The whole decision runs in ONE SERIALIZABLE transaction. That isolation level
// is not decoration: the sequence is read-then-write on rows another concurrent
// refresh is reading, and under READ COMMITTED two racing requests can both
// pass the "not yet consumed" check and both rotate, leaving two live tokens
// where the design guarantees one. Serialization failures surface to the caller
// as a retryable error rather than being silently swallowed.
//
// The three outcomes:
//
//   - token already consumed, INSIDE the grace window → a second tab (or React
//     StrictMode's double mount) racing on the same cookie. Re-issue the
//     family's current tokens without rotating. Without this window, any SPA
//     that mounts twice signs the user out at random, which is the single most
//     common way a correct rotation scheme becomes unusable in practice.
//
//   - token already consumed, OUTSIDE the window → replay of a token that was
//     legitimately rotated away. Either it leaked or the client is broken; both
//     warrant killing the whole FAMILY, not just this token. Revoking only the
//     presented token would leave the thief's freshly rotated one alive.
//
//   - token live → consume it, rotate, slide the expiry.
func (r *Repository) Rotate(ctx context.Context, rawRefresh string, ttl SessionTTL, ip, ua string) (RotateResult, error) {
	hash := secrets.Hash(rawRefresh)

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return RotateResult{}, fmt.Errorf("rotate begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var usedFamily string
	var usedSession int64
	var usedAt time.Time
	err = tx.QueryRow(ctx,
		`SELECT family_id::text, session_id, used_at FROM session_used_token WHERE token_hash = $1`, hash,
	).Scan(&usedFamily, &usedSession, &usedAt)

	switch {
	case err == nil:
		return r.handleConsumed(ctx, tx, usedFamily, usedSession, usedAt, ttl)
	case errors.Is(err, pgx.ErrNoRows):
		// Fresh token — fall through to the normal rotation below.
	default:
		return RotateResult{}, fmt.Errorf("rotate used lookup: %w", err)
	}

	var sid, uid int64
	var familyID string
	var createdAt time.Time
	var status string
	err = tx.QueryRow(ctx, `
		SELECT s.id, s.user_id, s.family_id::text, s.created_at, u.status
		FROM session s
		JOIN app_user u ON u.id = s.user_id
		WHERE s.refresh_token_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.refresh_expires_at > now()
		FOR UPDATE OF s`, hash).Scan(&sid, &uid, &familyID, &createdAt, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return RotateResult{}, ErrSessionInvalid
	}
	if err != nil {
		return RotateResult{}, fmt.Errorf("rotate session lookup: %w", err)
	}

	// The absolute ceiling is measured from the family's birth, not from the
	// last rotation, which is the entire point of having it.
	if time.Since(createdAt) > ttl.Absolute {
		if err := revokeFamily(ctx, tx, familyID, "expired"); err != nil {
			return RotateResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return RotateResult{}, fmt.Errorf("rotate expire commit: %w", err)
		}
		return RotateResult{}, ErrSessionInvalid
	}
	// A user disabled mid-session must not be able to refresh their way back in.
	if status != StatusActive {
		if err := revokeFamily(ctx, tx, familyID, ReasonUserDisabled); err != nil {
			return RotateResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return RotateResult{}, fmt.Errorf("rotate disabled commit: %w", err)
		}
		return RotateResult{}, ErrUserNotActive
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO session_used_token (token_hash, family_id, session_id) VALUES ($1, $2::uuid, $3)`,
		hash, familyID, sid); err != nil {
		return RotateResult{}, fmt.Errorf("rotate mark used: %w", err)
	}

	tok, err := writeNewTokens(ctx, tx, sid, ttl, ip, ua)
	if err != nil {
		return RotateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RotateResult{}, fmt.Errorf("rotate commit: %w", err)
	}
	return RotateResult{Tokens: tok, UserID: authctx.UserID(uid), Session: sid}, nil
}

// handleConsumed decides between "racing tab" and "replay attack".
func (r *Repository) handleConsumed(ctx context.Context, tx pgx.Tx, familyID string, sessionID int64, usedAt time.Time, ttl SessionTTL) (RotateResult, error) {
	if time.Since(usedAt) > ttl.Grace {
		// Outside the window: revoke the family and wipe its consumed-token
		// trail, so the attacker's rotated token dies with it.
		//
		// The owner is read BEFORE the purge. The handler needs it to send the
		// "your sessions were signed out" warning, and after the DELETE below
		// there is no row left that ties this token to a user.
		var owner int64
		if err := tx.QueryRow(ctx,
			`SELECT user_id FROM session WHERE id = $1`, sessionID).Scan(&owner); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return RotateResult{}, fmt.Errorf("rotate reuse owner: %w", err)
		}
		if err := revokeAndPurgeFamily(ctx, tx, familyID); err != nil {
			return RotateResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return RotateResult{}, fmt.Errorf("rotate reuse commit: %w", err)
		}
		return RotateResult{UserID: authctx.UserID(owner)}, ErrSessionReuse
	}

	// Inside the window. The session the token belonged to must still be live —
	// a grace hit on an already-revoked family is not a racing tab, it is a
	// replay against a session someone deliberately killed.
	var uid int64
	var revoked *time.Time
	var bornAt time.Time
	var ip *string
	var ua *string
	err := tx.QueryRow(ctx,
		`SELECT user_id, revoked_at, created_at, host(ip), user_agent
		 FROM session WHERE id = $1 FOR UPDATE`, sessionID,
	).Scan(&uid, &revoked, &bornAt, &ip, &ua)
	if errors.Is(err, pgx.ErrNoRows) {
		return RotateResult{}, ErrSessionInvalid
	}
	if err != nil {
		return RotateResult{}, fmt.Errorf("rotate grace lookup: %w", err)
	}
	if revoked != nil {
		return RotateResult{}, ErrSessionInvalid
	}

	// Cap how many live sessions one family may hold.
	//
	// Without this, a single consumed refresh token can be replayed as fast as
	// the network allows for the whole grace window, and each replay mints
	// another sibling — unbounded row creation from one stolen token, and a
	// storage-amplification vector even when it is not stolen. A family is ONE
	// login on one device; a handful of siblings covers every legitimate race
	// (a double mount, a few tabs reloading together), and anything past that is
	// not a race, so it is treated as the replay it looks like.
	var liveInFamily int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM session WHERE family_id = $1::uuid AND revoked_at IS NULL`,
		familyID).Scan(&liveInFamily); err != nil {
		return RotateResult{}, fmt.Errorf("rotate grace family size: %w", err)
	}
	if liveInFamily >= maxLiveSessionsPerFamily {
		if err := revokeAndPurgeFamily(ctx, tx, familyID); err != nil {
			return RotateResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return RotateResult{}, fmt.Errorf("rotate grace cap commit: %w", err)
		}
		return RotateResult{UserID: authctx.UserID(uid)}, ErrSessionReuse
	}

	// A SIBLING session in the same family, rather than new tokens on the
	// existing row.
	//
	// Overwriting the row would defeat the purpose of the grace window. The
	// server stores only hashes, so it cannot hand back the triple the winning
	// request just installed; re-minting on the same row therefore invalidates
	// whatever the winner is holding. Both requests come from the SAME cookie
	// jar, so the browser ends up with whichever response landed last while the
	// row holds the other — and the tab that loses that race gets signed out.
	// That is precisely the random logout this window exists to prevent.
	//
	// A sibling row makes both triples independently valid. It inherits the
	// family (so reuse detection still kills everything at once) AND the
	// original created_at, so the 90-day absolute ceiling is measured from the
	// family's birth. Without inheriting created_at, a client could ride the
	// grace window on every rotation and reset the ceiling forever, which is
	// exactly the immortality the ceiling exists to prevent.
	issue, err := newSessionIssue(ttl)
	if err != nil {
		return RotateResult{}, err
	}
	siblingID, err := insertSessionTx(ctx, tx, authctx.UserID(uid), issue, familyID, &bornAt, ip, ua)
	if err != nil {
		return RotateResult{}, fmt.Errorf("rotate grace sibling: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RotateResult{}, fmt.Errorf("rotate grace commit: %w", err)
	}
	return RotateResult{
		Tokens:   issue.tokens,
		UserID:   authctx.UserID(uid),
		Session:  siblingID,
		Replayed: true,
	}, nil
}

// writeNewTokens mints and installs a fresh access/refresh/CSRF triple on an
// existing session row.
func writeNewTokens(ctx context.Context, tx pgx.Tx, sessionID int64, ttl SessionTTL, ip, ua string) (issuedTokens, error) {
	issue, err := newSessionIssue(ttl)
	if err != nil {
		return issuedTokens{}, err
	}
	// ip/user_agent are only overwritten when supplied, so the grace path (which
	// passes empty strings) does not blank out the device fingerprint the
	// original login recorded.
	if _, err := tx.Exec(ctx, `
		UPDATE session SET
			access_token_hash = $2, access_expires_at = $3,
			refresh_token_hash = $4, refresh_expires_at = $5,
			csrf_token_hash = $6,
			ip = COALESCE($7, ip), user_agent = COALESCE(NULLIF($8, ''), user_agent),
			rotated_at = now(), last_seen_at = now()
		WHERE id = $1`,
		sessionID, issue.hashes.access, issue.tokens.AccessExpiry,
		issue.hashes.refresh, issue.tokens.RefreshExpiry,
		issue.hashes.csrf, nullIP(ip), truncate(ua, 512)); err != nil {
		return issuedTokens{}, fmt.Errorf("rotate write tokens: %w", err)
	}
	return issue.tokens, nil
}

func revokeAndPurgeFamily(ctx context.Context, tx pgx.Tx, familyID string) error {
	if err := revokeFamily(ctx, tx, familyID, ReasonReuseDetected); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM session_used_token WHERE family_id = $1::uuid`, familyID); err != nil {
		return fmt.Errorf("rotate purge family: %w", err)
	}
	return nil
}

func revokeFamily(ctx context.Context, tx pgx.Tx, familyID, reason string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE session SET revoked_at = now(), revoked_reason = $2
		WHERE family_id = $1::uuid AND revoked_at IS NULL`, familyID, reason); err != nil {
		return fmt.Errorf("revoke family: %w", err)
	}
	return nil
}

// EmailForUser fetches an address for the reuse-detection warning mail.
func (r *Repository) EmailForUser(ctx context.Context, uid authctx.UserID) (string, error) {
	var email string
	err := r.pool.QueryRow(ctx, `SELECT email FROM app_user WHERE id = $1`, int64(uid)).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoUser
	}
	if err != nil {
		return "", fmt.Errorf("email for user: %w", err)
	}
	return email, nil
}
