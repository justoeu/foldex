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

	"github.com/jackc/pgx/v5"

	"foldex/internal/folders"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

// ClickAndResolve appends a row to click_log and returns the destination URL.
// Used by /go/{id}; returns httperr.ErrNotFound when no link matches.
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
		return "", httperr.ErrNotFound
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

// UpdatePreview is called by the preview worker once metadata is fetched (or fails).
// Manual OG upload wins: never overwrite a non-empty og_image_url (CAS vs
// concurrent UpdateOGImage).
//
// Terminal statuses (ok/failed) only apply while preview_status is still
// 'pending' (CAS — RACE-HER-009). A concurrent refreshPreview that already
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
            updated_at     = now()
        WHERE id = $6`
	if status != StatusPending {
		q += ` AND preview_status = 'pending'`
	}
	_, err := r.pool.Exec(ctx, q, status, favicon, ogImage, description, errMsg, id)
	return err
}

// FindDueForCheck returns link IDs whose check_interval has elapsed since
// the last check (or which have never been checked). Used by the changecheck
// worker's tick. Cap at limit so a single tick can't enqueue an unbounded
// backlog — anything left waits one more interval, which is fine.
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
	rows, err := r.pool.Query(ctx, `
        UPDATE link
        SET last_checked_at = now()
        WHERE id IN (
            SELECT id FROM link
            WHERE check_interval IS NOT NULL
              AND (
                  last_checked_at IS NULL
                  OR last_checked_at < now() - CASE check_interval
                      WHEN 'hourly' THEN interval '1 hour'
                      WHEN 'daily'  THEN interval '1 day'
                      WHEN 'weekly' THEN interval '7 days'
                  END
              )
            ORDER BY COALESCE(last_checked_at, 'epoch'::timestamptz) ASC, id ASC
            LIMIT $1
            FOR UPDATE SKIP LOCKED
        )
        RETURNING id, user_id
    `, limit)
	if err != nil {
		return nil, fmt.Errorf("find due for check: %w", err)
	}
	defer rows.Close()
	out := make([]DueLink, 0)
	for rows.Next() {
		var d DueLink
		var owner int64
		if err := rows.Scan(&d.ID, &owner); err != nil {
			return nil, err
		}
		d.UserID = authctx.UserID(owner)
		out = append(out, d)
	}
	return out, rows.Err()
}

// DueLink is one link claimed by the change-check sweep, paired with its owner
// so the resulting notification is delivered to that user's subscriptions only.
type DueLink struct {
	ID     int64
	UserID authctx.UserID
}

// SystemGet loads a link without an ownership predicate. Only the change-check
// worker may call it, and only for an id it just claimed via
// SystemFindDueForCheck — which is what establishes the owner.
func (r *Repository) SystemGet(ctx context.Context, id int64) (Link, error) {
	var l Link
	err := scanLink(r.pool.QueryRow(ctx, `SELECT `+linkColumns+linkFrom+` WHERE l.id = $1`, id), &l)
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, httperr.ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("system get link: %w", err)
	}
	l.Tags = []Tag{}
	return l, nil
}

// SystemUpdateOGImage is the preview worker's screenshot-fallback writer. The
// worker reached this id through its own pending sweep, so there is no session
// to scope by; the user-facing sibling is UpdateOGImage.
func (r *Repository) SystemUpdateOGImage(ctx context.Context, id int64, imageURL string) error {
	ct, err := r.pool.Exec(ctx, `
        UPDATE link
        SET og_image_url   = $1,
            preview_status = 'ok',
            preview_error  = NULL,
            updated_at     = now()
        WHERE id = $2
    `, imageURL, id)
	if err != nil {
		return fmt.Errorf("system update og_image_url: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return httperr.ErrNotFound
	}
	return nil
}

// CheckResult is the outcome of a single worker run against a link.
type CheckResult struct {
	Fingerprint string // "feed:<hex>" or "content:<hex>" — empty when err non-nil
	Changed     bool   // true only when previous fingerprint existed AND differs
	FetchErr    string // free-form; nil-safe, "" means success
}

// RecordCheckResult bumps last_checked_at always, last_fingerprint when we got
// one, and last_change_detected_at only when Changed is true. The "first
// observation never counts as a change" rule lives here — the caller passes
// `Changed=false` when the previous fingerprint was empty, so opt-in alone
// doesn't trigger a spurious push on the very first scan.
//
// last_check_error is set to the FetchErr message on failure and cleared on
// success. Importantly we do NOT touch preview_error — that column belongs
// to the preview worker (CLAUDE.md §4 invariant: "Worker is the only writer
// of preview_status"). Cross-writing would confuse LinkCard's preview
// failure surface the next time someone renders preview_error.
func (r *Repository) SystemRecordCheckResult(ctx context.Context, id int64, res CheckResult) error {
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
        UPDATE link
        SET last_checked_at = now(),
            last_fingerprint = COALESCE($1, last_fingerprint),
            last_check_error = $2`
	if res.Changed {
		sql += `,
            last_change_detected_at = now(),
            change_seen_at = NULL`
	}
	// Only write when still opted-in — concurrent opt-out must not restore a
	// fingerprint after the user cleared check_interval.
	sql += ` WHERE id = $3 AND check_interval IS NOT NULL`
	ct, err := r.pool.Exec(ctx, sql, fp, checkErr, id)
	if err != nil {
		return fmt.Errorf("record check result: %w", err)
	}
	if ct.RowsAffected() == 0 {
		// Not found OR opted out mid-flight — treat as success (no work left).
		return nil
	}
	return nil
}
