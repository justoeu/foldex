package links

// System-scoped repository methods: queries that legitimately run WITHOUT a
// user_id predicate. Everything in this file is either a public, session-less
// route (/go/{id-or-slug}) or a background worker that sweeps across every
// tenant (preview, change-check).
//
// The convention exists so review is cheap: a `FROM link` with no user_id
// predicate ANYWHERE ELSE is a bug. A CI grep enforces it. See
// docs/SDD-AUTH-RBAC.md §8.2.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"foldex/internal/folders"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/domainerr"
)

// ClickAndResolve appends a row to click_log and returns the destination URL.
// Used by /go/{id}; returns domainerr.ErrNotFound when no link matches.
//
// click_log is the only writer for click data — there's no longer a
// denormalized counter on `link`, so this is a single INSERT (counter views
// are derived in SELECT via a LATERAL join). The two statements still share
// a transaction so a missing link returns 404 instead of producing an
// orphan click_log row via FK violation.
func (r *Repository) ClickAndResolve(ctx context.Context, id int64) (string, error) {
	return r.clickAndResolveWhere(ctx, "l.id = $1", id)
}

// ClickAndResolveBySlug is the slug-keyed sibling of ClickAndResolve. Same
// invariants — atomic resolve + click insert in one tx.
func (r *Repository) ClickAndResolveBySlug(ctx context.Context, slug string) (string, error) {
	return r.clickAndResolveWhere(ctx, "l.slug = $1", slug)
}

func (r *Repository) clickAndResolveWhere(ctx context.Context, where string, arg any) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin click tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var id int64
	var owner int64
	var u string
	// 404 for links inside password-protected folders — public /go must not
	// leak destinations (or inflate click_log) without unlock.
	err = tx.QueryRow(ctx, `
        SELECT l.id, l.user_id, l.url FROM link l
        WHERE `+where+` AND `+folders.SQLNotInLockedFolder("l"), arg).Scan(&id, &owner, &u)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domainerr.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve link: %w", err)
	}

	// user_id comes from the row we just resolved, not from a session — /go/ is
	// public. It is a denormalized accelerator (migration 000018); entity_kind /
	// entity_id remain authoritative for WHAT was clicked.
	if _, err := tx.Exec(ctx,
		`INSERT INTO click_log (entity_kind, entity_id, user_id) VALUES ('link', $1, $2)`,
		id, owner); err != nil {
		return "", fmt.Errorf("insert click_log: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit click tx: %w", err)
	}
	return u, nil
}

// SystemUpdatePreview starts a new generation when status is pending. Terminal
// writes use the conditional methods below and never advance the generation.
// Manual OG upload wins: never overwrite a non-empty og_image_url (CAS vs
// concurrent UpdateOGImage).
//
// Terminal statuses (ok/failed) only apply while preview_status is still
// 'pending'. A concurrent refreshPreview that already
// flipped back to pending keeps its turn; a stale worker finishing after a
// newer job was re-enqueued cannot overwrite a non-pending status. Setting
// pending itself is unconditional so refresh/retry always restarts the poll.
func (r *Repository) SystemUpdatePreview(ctx context.Context, id int64, status PreviewStatus, favicon, ogImage, description, errMsg *string) error {
	if !status.Valid() {
		return fmt.Errorf("invalid preview status %q", status)
	}
	q := `
        UPDATE link
        SET preview_status = $1,
            favicon_url    = COALESCE($2, favicon_url),
            og_image_url   = COALESCE(NULLIF(og_image_url, ''), $3),
            description    = COALESCE($4, description),
            preview_error  = $5,
            preview_generation = CASE WHEN $1 = 'pending' THEN preview_generation + 1 ELSE preview_generation END,
            updated_at     = now()
        WHERE id = $6`
	if status != StatusPending {
		q += ` AND preview_status = 'pending'`
	}
	_, err := r.pool.Exec(ctx, q, status, favicon, ogImage, description, errMsg, id)
	return err
}

// SystemUpdatePreviewIfUnchanged prevents an older fetch from overwriting a
// refresh or edit that landed while network work was in progress.
func (r *Repository) SystemUpdatePreviewIfUnchanged(ctx context.Context, id int64, expectedUpdatedAt time.Time, expectedGeneration int64, status PreviewStatus, favicon, ogImage, description, errMsg *string) (bool, error) {
	if !status.Valid() {
		return false, fmt.Errorf("invalid conditional preview status %q", status)
	}
	ct, err := r.pool.Exec(ctx, `
		UPDATE link
		SET preview_status = $1,
		    favicon_url    = COALESCE($2, favicon_url),
		    og_image_url   = COALESCE(NULLIF(og_image_url, ''), $3),
		    description    = COALESCE($4, description),
		    preview_error  = $5,
		    updated_at     = now()
		WHERE id = $6
		  AND updated_at = $7
		  AND preview_generation = $8
		  AND preview_status = 'pending'
	`, status, favicon, ogImage, description, errMsg, id, expectedUpdatedAt, expectedGeneration)
	if err != nil {
		return false, fmt.Errorf("conditionally update preview: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}

// SystemFindDueForCheck claims links whose check interval elapsed and returns
// the narrow projection needed by the change-check worker.
func (r *Repository) SystemFindDueForCheck(ctx context.Context, limit int) ([]DueLink, error) {
	if limit <= 0 || limit > 1000 {
		limit = 256
	}
	// Claim due rows by bumping last_checked_at under SKIP LOCKED so two
	// worker ticks (or replicas) cannot process the same link concurrently.
	//
	// user_id rides along so the resulting Web Push reaches only the link's
	// owner. link_check_due_idx is deliberately NOT user-scoped (see migration
	// 000017 §10) because this sweep spans every tenant by design.
	//
	// Spanning every tenant is not the same as spanning every ACCOUNT STATE.
	// Disabling an account revokes its sessions and kills its API tokens, but
	// a Web Push subscription is a browser channel that outlives both — so
	// without this predicate a disabled owner keeps being scanned and keeps
	// getting "this page changed" notifications on a device that can no longer
	// sign in. The claim is also where the cost is: filtering here means their
	// links stop consuming fetch budget too, not merely stop notifying.
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
        UPDATE link
        SET last_checked_at = now()
        WHERE id IN (
            SELECT l.id FROM link l
            JOIN app_user u ON u.id = l.user_id AND u.status = 'active'
            WHERE l.check_interval IS NOT NULL
              AND %s
              AND (
                  l.last_checked_at IS NULL
                  OR l.last_checked_at < now() - CASE l.check_interval
                      WHEN 'hourly' THEN interval '1 hour'
                      WHEN 'daily'  THEN interval '1 day'
                      WHEN 'weekly' THEN interval '7 days'
                  END
              )
            ORDER BY COALESCE(l.last_checked_at, 'epoch'::timestamptz) ASC, l.id ASC
            LIMIT $1
            FOR UPDATE SKIP LOCKED
        )
		RETURNING id, user_id, url, title, check_interval, last_fingerprint, last_checked_at
    `, folders.SQLNotInLockedFolder("l")), limit)
	if err != nil {
		return nil, fmt.Errorf("find due for check: %w", err)
	}
	defer rows.Close()
	out := make([]DueLink, 0)
	for rows.Next() {
		var d DueLink
		var owner int64
		if err := rows.Scan(
			&d.ID,
			&owner,
			&d.URL,
			&d.Title,
			&d.CheckInterval,
			&d.LastFingerprint,
			&d.ClaimedAt,
		); err != nil {
			return nil, err
		}
		d.UserID = authctx.UserID(owner)
		out = append(out, d)
	}
	return out, rows.Err()
}

// DueLink is the immutable configuration snapshot reserved by one sweep claim.
type DueLink struct {
	ID              int64
	UserID          authctx.UserID
	URL             string
	Title           string
	CheckInterval   string
	LastFingerprint *string
	ClaimedAt       time.Time
}

// PreviewWork is the narrow projection required by the preview worker. Keeping
// it separate avoids the click-log aggregate in the full Link projection.
type PreviewWork struct {
	ID            int64
	URL           string
	OGImageURL    *string
	PreviewStatus PreviewStatus
	UpdatedAt     time.Time
	Generation    int64
}

func (r *Repository) SystemGetPreview(ctx context.Context, id int64) (PreviewWork, error) {
	var work PreviewWork
	err := r.pool.QueryRow(ctx, `
		SELECT id, url, og_image_url, preview_status, updated_at, preview_generation
		FROM link
		WHERE id = $1
	`, id).Scan(&work.ID, &work.URL, &work.OGImageURL, &work.PreviewStatus, &work.UpdatedAt, &work.Generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return PreviewWork{}, domainerr.ErrNotFound
	}
	if err != nil {
		return PreviewWork{}, fmt.Errorf("system get preview: %w", err)
	}
	return work, nil
}

// SystemPendingPreviews returns the slim recovery projection in one query so
// requeueing pending work does not hydrate each link separately.
func (r *Repository) SystemPendingPreviews(ctx context.Context, limit int) ([]PreviewWork, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, url, og_image_url, preview_status, updated_at, preview_generation
		FROM link
		WHERE preview_status = 'pending'
		ORDER BY id ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("system list pending previews: %w", err)
	}
	defer rows.Close()
	previews := make([]PreviewWork, 0)
	for rows.Next() {
		var work PreviewWork
		if err := rows.Scan(&work.ID, &work.URL, &work.OGImageURL, &work.PreviewStatus, &work.UpdatedAt, &work.Generation); err != nil {
			return nil, fmt.Errorf("system scan pending preview: %w", err)
		}
		previews = append(previews, work)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("system list pending previews: %w", err)
	}
	return previews, nil
}

// SystemUpdateOGImage is the preview worker's screenshot-fallback writer. It
// applies only if the row is unchanged since the worker's final pre-capture
// read and still has no image, so a manual upload or newer preview job wins.
func (r *Repository) SystemUpdateOGImage(ctx context.Context, id int64, imageURL string, expectedUpdatedAt time.Time, expectedGeneration int64) (bool, error) {
	ct, err := r.pool.Exec(ctx, `
        UPDATE link
        SET og_image_url   = $1,
            preview_status = 'ok',
            preview_error  = NULL,
            updated_at     = now()
        WHERE id = $2
	      AND updated_at = $3
	      AND preview_generation = $4
	      AND preview_status = 'pending'
	      AND COALESCE(og_image_url, '') = ''
    `, imageURL, id, expectedUpdatedAt, expectedGeneration)
	if err != nil {
		return false, fmt.Errorf("system update og_image_url: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}

// SystemFinishScreenshotFallback releases pending UI state only if no newer
// write occurred after the fallback's final pre-capture read.
func (r *Repository) SystemFinishScreenshotFallback(ctx context.Context, id int64, expectedUpdatedAt time.Time, expectedGeneration int64) (bool, error) {
	ct, err := r.pool.Exec(ctx, `
        UPDATE link
        SET preview_status = 'ok',
            preview_error  = NULL,
            updated_at     = now()
        WHERE id = $1
          AND updated_at = $2
		  AND preview_generation = $3
          AND preview_status = 'pending'
          AND COALESCE(og_image_url, '') = ''
	`, id, expectedUpdatedAt, expectedGeneration)
	if err != nil {
		return false, fmt.Errorf("finish screenshot fallback: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}

// CheckResult is the outcome of a single worker run against a link.
type CheckResult struct {
	Fingerprint string // "feed:<hex>" or "content:<hex>" — empty when err non-nil
	Changed     bool   // true only when previous fingerprint existed AND differs
	FetchErr    string // free-form; nil-safe, "" means success
}

// SystemRecordCheckResult writes last_fingerprint when available and
// last_change_detected_at only when Changed is true. The "first
// observation never counts as a change" rule lives here — the caller passes
// `Changed=false` when the previous fingerprint was empty, so opt-in alone
// doesn't trigger a spurious push on the very first scan.
//
// last_check_error is set to the FetchErr message on failure and cleared on
// success. Importantly we do NOT touch preview_error — that column belongs
// to the preview worker (CLAUDE.md §4 invariant: "Worker is the only writer
// of preview_status"). Cross-writing would confuse LinkCard's preview
// failure surface the next time someone renders preview_error.
// A false, nil result means a newer claim or monitoring reconfiguration won.
func (r *Repository) SystemRecordCheckResult(ctx context.Context, id int64, expectedClaimedAt time.Time, res CheckResult) (bool, error) {
	var fp any = nil
	if res.Fingerprint != "" {
		fp = res.Fingerprint
	}
	// Empty FetchErr → clear last_check_error so a recovering link drops
	// the error message. Non-empty → stamp it.
	var checkErr any = nil
	if res.FetchErr != "" {
		checkErr = res.FetchErr
	}
	sql := `
        UPDATE link AS l
		SET last_fingerprint = COALESCE($1, last_fingerprint),
            last_check_error = $2`
	if res.Changed {
		sql += `,
            last_change_detected_at = now(),
            change_seen_at = NULL`
	}
	// A newer claim or URL/interval update changes/nulls this exact token.
	sql += ` WHERE l.id = $3 AND l.last_checked_at = $4 AND l.check_interval IS NOT NULL AND ` +
		folders.SQLNotInLockedFolder("l")
	ct, err := r.pool.Exec(ctx, sql, fp, checkErr, id, expectedClaimedAt)
	if err != nil {
		return false, fmt.Errorf("record check result: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}
