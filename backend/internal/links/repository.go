package links

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/httperr"
)

type PreviewStatus string

const (
	StatusPending PreviewStatus = "pending"
	StatusOK      PreviewStatus = "ok"
	StatusFailed  PreviewStatus = "failed"
)

func (s PreviewStatus) Valid() bool {
	return s == StatusPending || s == StatusOK || s == StatusFailed
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// `click_count` and `last_clicked_at` are derived from click_log via the
// LATERAL join — there is no longer a denormalized counter on the link row.
// Use `linkColumns` together with `linkFrom` in every SELECT.
const linkColumns = `
    l.id, l.url, l.title, l.slug, l.description, l.favicon_url, l.og_image_url,
    COALESCE(cl.cnt, 0) AS click_count,
    l.preview_status, l.preview_error,
    cl.last_at AS last_clicked_at,
    l.pinned, l.folder_id, l.created_at, l.updated_at,
    l.check_interval, l.last_checked_at, l.last_fingerprint,
    l.last_change_detected_at, l.change_seen_at, l.last_check_error
`

const linkFrom = `
    FROM link l
    LEFT JOIN LATERAL (
        SELECT count(*) AS cnt, max(clicked_at) AS last_at
        FROM click_log
        WHERE link_id = l.id
    ) cl ON TRUE
`

// rowScanner is the Scan method shared by pgx.Row and pgx.Rows. Letting one
// helper read into a *Link from either single-row or multi-row results keeps
// the 21-column scan list in exactly one place — adding a column used to mean
// editing four near-identical Scan(...) blocks (Get, GetBySlug, List,
// ListRecentChanges) and silently dropping one was a latent bug.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanLink reads one link row (in linkColumns order) into l.
func scanLink(s rowScanner, l *Link) error {
	return s.Scan(
		&l.ID, &l.URL, &l.Title, &l.Slug, &l.Description, &l.FaviconURL, &l.OGImageURL,
		&l.ClickCount, &l.PreviewStatus, &l.PreviewError, &l.LastClickedAt,
		&l.Pinned, &l.FolderID, &l.CreatedAt, &l.UpdatedAt,
		&l.CheckInterval, &l.LastCheckedAt, &l.LastFingerprint,
		&l.LastChangeDetectedAt, &l.ChangeSeenAt, &l.LastCheckError,
	)
}

func (r *Repository) Create(ctx context.Context, in CreateInput) (Link, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Link{}, err
	}
	defer tx.Rollback(ctx)

	// Slug strategy:
	//   - User-supplied: use as-is, surface UNIQUE violations as ErrConflict.
	//   - Auto-derived from title: try the bare slug first, fall back to
	//     "-2", "-3", … on collision (capped at 999 to avoid pathological
	//     loops). Empty Slugify output → "link-<placeholder>" pre-INSERT;
	//     since we don't have the id yet we use a UUID-ish marker, but the
	//     simpler path is "link-untitled" + suffix-on-collision.
	userSupplied := in.Slug != nil
	var slug string
	if userSupplied {
		slug = *in.Slug
	} else {
		slug = Slugify(in.Title)
		if slug == "" {
			slug = "link-untitled"
		}
	}

	var id int64
	for attempt := 0; attempt < 1000; attempt++ {
		candidate := slug
		if attempt > 0 {
			candidate = fmt.Sprintf("%s-%d", slug, attempt+1)
		}
		err = tx.QueryRow(ctx, `
            INSERT INTO link (url, title, slug, description, pinned, folder_id, check_interval)
            VALUES ($1, $2, $3, $4, $5, $6, $7)
            RETURNING id
        `, in.URL, in.Title, candidate, in.Description, in.Pinned, in.FolderID, in.CheckInterval).Scan(&id)
		if err == nil {
			break
		}
		if isURLUniqueViolation(err) {
			return Link{}, httperr.New(409, "url_taken", "url already in use")
		}
		if isSlugUniqueViolation(err) {
			if userSupplied {
				return Link{}, httperr.New(409, "slug_taken", "slug already in use")
			}
			// Roll back the failed INSERT — Postgres aborts the tx on a
			// constraint violation, so reopening is required for the next
			// attempt.
			_ = tx.Rollback(ctx)
			tx, err = r.pool.Begin(ctx)
			if err != nil {
				return Link{}, fmt.Errorf("retry begin tx: %w", err)
			}
			continue
		}
		return Link{}, fmt.Errorf("insert link: %w", err)
	}
	if id == 0 {
		return Link{}, fmt.Errorf("could not allocate a unique slug after 1000 attempts")
	}

	if err := setLinkTags(ctx, tx, id, in.TagIDs); err != nil {
		return Link{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Link{}, err
	}
	return r.Get(ctx, id)
}

// uniqueConstraint returns the violated constraint name when err is a Postgres
// 23505 unique-violation, or "" otherwise. errors.As survives `%w` wrapping —
// the older string-match approach worked because the constraint name landed in
// the formatted message, but it would silently break if any wrapping layer
// ever omitted Unwrap or changed the format.
func uniqueConstraint(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return ""
	}
	return pgErr.ConstraintName
}

func isSlugUniqueViolation(err error) bool {
	return uniqueConstraint(err) == "link_slug_unique"
}

func isURLUniqueViolation(err error) bool {
	return uniqueConstraint(err) == "link_url_unique"
}

func (r *Repository) Get(ctx context.Context, id int64) (Link, error) {
	var l Link
	err := scanLink(r.pool.QueryRow(ctx, `SELECT `+linkColumns+linkFrom+` WHERE l.id = $1`, id), &l)
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, httperr.ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("get link: %w", err)
	}
	tags, err := r.tagsFor(ctx, []int64{id})
	if err != nil {
		return Link{}, err
	}
	l.Tags = tags[id]
	if l.Tags == nil {
		l.Tags = []Tag{}
	}
	return l, nil
}

// GetBySlug is the slug-keyed sibling of Get. Used by the redirect handler's
// fallback path (ID-first → slug fallback) and by anywhere that needs to
// resolve a public-facing slug back to the full link row.
func (r *Repository) GetBySlug(ctx context.Context, slug string) (Link, error) {
	var l Link
	err := scanLink(r.pool.QueryRow(ctx, `SELECT `+linkColumns+linkFrom+` WHERE l.slug = $1`, slug), &l)
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, httperr.ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("get link by slug: %w", err)
	}
	tags, err := r.tagsFor(ctx, []int64{l.ID})
	if err != nil {
		return Link{}, err
	}
	l.Tags = tags[l.ID]
	if l.Tags == nil {
		l.Tags = []Tag{}
	}
	return l, nil
}

func (r *Repository) List(ctx context.Context, q ListQuery) ([]Link, error) {
	args := []any{}
	where := []string{}

	if q.Q != "" {
		args = append(args, "%"+q.Q+"%")
		idx := len(args)
		where = append(where, fmt.Sprintf("(l.title ILIKE $%d OR l.url ILIKE $%d OR COALESCE(l.description,'') ILIKE $%d)", idx, idx, idx))
	}
	if len(q.TagIDs) > 0 {
		args = append(args, q.TagIDs)
		idx := len(args)
		where = append(where, fmt.Sprintf(`l.id IN (
            SELECT link_id FROM link_tag
            WHERE tag_id = ANY($%d)
            GROUP BY link_id
            HAVING count(DISTINCT tag_id) = %d
        )`, idx, len(q.TagIDs)))
	}
	// Folder filter: explicit FolderID wins over Ungrouped if both are set.
	if q.FolderID != nil {
		args = append(args, *q.FolderID)
		where = append(where, fmt.Sprintf("l.folder_id = $%d", len(args)))
	} else if q.Ungrouped {
		where = append(where, "l.folder_id IS NULL")
	}

	// Pinned links always come first regardless of the requested sort.
	// Click-related ordering references the derived columns (cl.cnt /
	// cl.last_at) since they don't live on `link` anymore.
	order := "l.pinned DESC, l.created_at DESC"
	switch q.Sort {
	case "clicks":
		order = "l.pinned DESC, COALESCE(cl.cnt, 0) DESC, l.created_at DESC"
	case "recent":
		order = "l.pinned DESC, COALESCE(cl.last_at, l.created_at) DESC"
	case "alpha":
		order = "l.pinned DESC, lower(l.title) ASC, l.created_at DESC"
	case "alpha_desc":
		order = "l.pinned DESC, lower(l.title) DESC, l.created_at DESC"
	}

	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)

	sql := `SELECT ` + linkColumns + linkFrom
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", order, len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	defer rows.Close()

	links := make([]Link, 0)
	ids := []int64{}
	for rows.Next() {
		var l Link
		if err := scanLink(rows, &l); err != nil {
			return nil, err
		}
		l.Tags = []Tag{}
		links = append(links, l)
		ids = append(ids, l.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return links, nil
	}
	tagsByLink, err := r.tagsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range links {
		if t, ok := tagsByLink[links[i].ID]; ok {
			links[i].Tags = t
		}
	}
	return links, nil
}

func (r *Repository) Update(ctx context.Context, id int64, in UpdateInput) (Link, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Link{}, err
	}
	defer tx.Rollback(ctx)

	sets := []string{}
	args := []any{}
	i := 1
	if in.URL != nil {
		sets = append(sets, fmt.Sprintf("url = $%d", i))
		args = append(args, strings.TrimSpace(*in.URL))
		i++
	}
	if in.Title != nil {
		sets = append(sets, fmt.Sprintf("title = $%d", i))
		args = append(args, strings.TrimSpace(*in.Title))
		i++
	}
	if in.Description != nil {
		sets = append(sets, fmt.Sprintf("description = $%d", i))
		args = append(args, *in.Description)
		i++
	}
	if in.Pinned != nil {
		sets = append(sets, fmt.Sprintf("pinned = $%d", i))
		args = append(args, *in.Pinned)
		i++
	}
	// folder_id: only writes when the JSON payload included the field
	// (FolderIDSet). FolderID == nil + FolderIDSet means "clear", which the
	// driver translates to NULL.
	if in.FolderIDSet {
		sets = append(sets, fmt.Sprintf("folder_id = $%d", i))
		args = append(args, in.FolderID)
		i++
	}
	// slug: tri-state same as folder_id, except `null` doesn't mean "clear"
	// (slug is NOT NULL) — it means "regenerate from current title". We need
	// the live title for that, so resolve it inside the same tx so we read
	// the about-to-be-updated value if `in.Title` was also set.
	if in.SlugSet {
		newSlug := ""
		if in.Slug != nil {
			newSlug = *in.Slug
		} else {
			// Use the just-staged title if present, else read current.
			currentTitle := ""
			if in.Title != nil {
				currentTitle = strings.TrimSpace(*in.Title)
			} else {
				if err := tx.QueryRow(ctx, `SELECT title FROM link WHERE id = $1`, id).Scan(&currentTitle); err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						return Link{}, httperr.ErrNotFound
					}
					return Link{}, fmt.Errorf("read title for slug regen: %w", err)
				}
			}
			newSlug = Slugify(currentTitle)
			if newSlug == "" {
				newSlug = fmt.Sprintf("link-%d", id)
			}
		}
		sets = append(sets, fmt.Sprintf("slug = $%d", i))
		args = append(args, newSlug)
		i++
	}
	// check_interval: tri-state. CheckIntervalSet=true + CheckInterval=nil means
	// "opt-out" — clearing the full change-check column group (fingerprint,
	// detection timestamps, seen marker) so re-enabling later doesn't replay
	// a stale "you have updates" badge from before. CheckInterval set to a
	// value just flips the opt-in flag; we let the worker establish a fresh
	// fingerprint on its first pass.
	if in.CheckIntervalSet {
		sets = append(sets, fmt.Sprintf("check_interval = $%d", i))
		args = append(args, in.CheckInterval)
		i++
		if in.CheckInterval == nil {
			sets = append(sets,
				"last_checked_at = NULL",
				"last_fingerprint = NULL",
				"last_change_detected_at = NULL",
				"change_seen_at = NULL",
			)
		}
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at = now()")
		args = append(args, id)
		q := fmt.Sprintf(`UPDATE link SET %s WHERE id = $%d`, strings.Join(sets, ", "), i)
		ct, err := tx.Exec(ctx, q, args...)
		if err != nil {
			if isURLUniqueViolation(err) {
				return Link{}, httperr.New(409, "url_taken", "url already in use")
			}
			if isSlugUniqueViolation(err) {
				return Link{}, httperr.New(409, "slug_taken", "slug already in use")
			}
			return Link{}, fmt.Errorf("update link: %w", err)
		}
		if ct.RowsAffected() == 0 {
			return Link{}, httperr.ErrNotFound
		}
	}
	if in.TagIDs != nil {
		if err := setLinkTags(ctx, tx, id, *in.TagIDs); err != nil {
			return Link{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Link{}, err
	}
	return r.Get(ctx, id)
}

func (r *Repository) Delete(ctx context.Context, id int64) error {
	ct, err := r.pool.Exec(ctx, `DELETE FROM link WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return httperr.ErrNotFound
	}
	return nil
}

// ClickAndResolve appends a row to click_log and returns the destination URL.
// Used by /go/{id}; returns httperr.ErrNotFound when no link matches.
//
// click_log is the only writer for click data — there's no longer a
// denormalized counter on `link`, so this is a single INSERT (counter views
// are derived in SELECT via a LATERAL join). The two statements still share
// a transaction so a missing link returns 404 instead of producing an
// orphan click_log row via FK violation.
func (r *Repository) ClickAndResolve(ctx context.Context, id int64) (string, error) {
	return r.clickAndResolveWhere(ctx, "id = $1", id)
}

// ClickAndResolveBySlug is the slug-keyed sibling of ClickAndResolve. Same
// invariants — atomic resolve + click insert in one tx.
func (r *Repository) ClickAndResolveBySlug(ctx context.Context, slug string) (string, error) {
	return r.clickAndResolveWhere(ctx, "slug = $1", slug)
}

func (r *Repository) clickAndResolveWhere(ctx context.Context, where string, arg any) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin click tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var id int64
	var u string
	err = tx.QueryRow(ctx, `SELECT id, url FROM link WHERE `+where, arg).Scan(&id, &u)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", httperr.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve link: %w", err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO click_log (link_id) VALUES ($1)`, id); err != nil {
		return "", fmt.Errorf("insert click_log: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit click tx: %w", err)
	}
	return u, nil
}

// UpdatePreview is called by the preview worker once metadata is fetched (or fails).
func (r *Repository) UpdatePreview(ctx context.Context, id int64, status PreviewStatus, favicon, ogImage, description, errMsg *string) error {
	if !status.Valid() {
		return fmt.Errorf("invalid preview status %q", status)
	}
	_, err := r.pool.Exec(ctx, `
        UPDATE link
        SET preview_status = $1,
            favicon_url    = COALESCE($2, favicon_url),
            og_image_url   = COALESCE($3, og_image_url),
            description    = COALESCE($4, description),
            preview_error  = $5,
            updated_at     = now()
        WHERE id = $6
    `, status, favicon, ogImage, description, errMsg, id)
	return err
}

// UpdateOGImage sets the og_image_url field for the given link. Side effect:
// preview_status is also forced to 'ok' (and preview_error cleared) — a manual
// upload means "the user supplied the image, the preview pipeline is done".
// This both stops the worker from auto-screenshotting later AND removes the
// "capturando…" label in the UI immediately.
func (r *Repository) UpdateOGImage(ctx context.Context, id int64, imageURL string) error {
	ct, err := r.pool.Exec(ctx, `
        UPDATE link
        SET og_image_url   = $1,
            preview_status = 'ok',
            preview_error  = NULL,
            updated_at     = now()
        WHERE id = $2
    `, imageURL, id)
	if err != nil {
		return fmt.Errorf("update og_image_url: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return httperr.ErrNotFound
	}
	return nil
}

// ClearOGImage sets og_image_url to NULL for the given link.
func (r *Repository) ClearOGImage(ctx context.Context, id int64) error {
	ct, err := r.pool.Exec(ctx, `
        UPDATE link
        SET og_image_url = NULL,
            updated_at   = now()
        WHERE id = $1
    `, id)
	if err != nil {
		return fmt.Errorf("clear og_image_url: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return httperr.ErrNotFound
	}
	return nil
}

// MarkChangeSeen flips `change_seen_at` to now() so the unseen-badge in the
// UI clears. No-op (404) if the link has no detected change yet — without
// that guard a stale `change_seen_at > last_change_detected_at` row could
// suppress the badge forever once the next detection fires.
func (r *Repository) MarkChangeSeen(ctx context.Context, id int64) error {
	ct, err := r.pool.Exec(ctx, `
        UPDATE link
        SET change_seen_at = now()
        WHERE id = $1 AND last_change_detected_at IS NOT NULL
    `, id)
	if err != nil {
		return fmt.Errorf("mark change seen: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return httperr.ErrNotFound
	}
	return nil
}

// ListRecentChanges feeds the sidebar's "Recent updates" section. Returns the
// most recently changed links within the given window (capped at limit).
// Pinned ordering does NOT apply here — sort is purely by detection time, so
// the user sees the freshest update first.
func (r *Repository) ListRecentChanges(ctx context.Context, sinceSeconds, limit int) ([]Link, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if sinceSeconds <= 0 {
		sinceSeconds = 7 * 24 * 60 * 60
	}
	sql := `SELECT ` + linkColumns + linkFrom + `
        WHERE l.last_change_detected_at IS NOT NULL
          AND l.last_change_detected_at > now() - make_interval(secs => $1::int)
        ORDER BY l.last_change_detected_at DESC
        LIMIT $2`
	rows, err := r.pool.Query(ctx, sql, sinceSeconds, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent changes: %w", err)
	}
	defer rows.Close()

	links := make([]Link, 0)
	ids := []int64{}
	for rows.Next() {
		var l Link
		if err := scanLink(rows, &l); err != nil {
			return nil, err
		}
		l.Tags = []Tag{}
		links = append(links, l)
		ids = append(ids, l.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return links, nil
	}
	tagsByLink, err := r.tagsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range links {
		if t, ok := tagsByLink[links[i].ID]; ok {
			links[i].Tags = t
		}
	}
	return links, nil
}

// FindDueForCheck returns link IDs whose check_interval has elapsed since
// the last check (or which have never been checked). Used by the changecheck
// worker's tick. Cap at limit so a single tick can't enqueue an unbounded
// backlog — anything left waits one more interval, which is fine.
func (r *Repository) FindDueForCheck(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 256
	}
	rows, err := r.pool.Query(ctx, `
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
    `, limit)
	if err != nil {
		return nil, fmt.Errorf("find due for check: %w", err)
	}
	defer rows.Close()
	out := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
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
func (r *Repository) RecordCheckResult(ctx context.Context, id int64, res CheckResult) error {
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
	sql += ` WHERE id = $3`
	ct, err := r.pool.Exec(ctx, sql, fp, checkErr, id)
	if err != nil {
		return fmt.Errorf("record check result: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return httperr.ErrNotFound
	}
	return nil
}

// tagsFor returns a map of link_id -> []Tag for the given link IDs.
func (r *Repository) tagsFor(ctx context.Context, linkIDs []int64) (map[int64][]Tag, error) {
	out := map[int64][]Tag{}
	if len(linkIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
        SELECT lt.link_id, t.id, t.name, t.color, t.icon
        FROM link_tag lt
        JOIN tag t ON t.id = lt.tag_id
        WHERE lt.link_id = ANY($1)
        ORDER BY t.name ASC
    `, linkIDs)
	if err != nil {
		return nil, fmt.Errorf("tags for links: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var linkID int64
		var t Tag
		if err := rows.Scan(&linkID, &t.ID, &t.Name, &t.Color, &t.Icon); err != nil {
			return nil, err
		}
		out[linkID] = append(out[linkID], t)
	}
	return out, rows.Err()
}

// setLinkTags replaces the tag set for a link inside a tx.
func setLinkTags(ctx context.Context, tx pgx.Tx, linkID int64, tagIDs []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM link_tag WHERE link_id = $1`, linkID); err != nil {
		return fmt.Errorf("clear link_tag: %w", err)
	}
	if len(tagIDs) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(tagIDs))
	for _, tid := range tagIDs {
		rows = append(rows, []any{linkID, tid})
	}
	_, err := tx.CopyFrom(ctx,
		pgx.Identifier{"link_tag"},
		[]string{"link_id", "tag_id"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("insert link_tag: %w", err)
	}
	return nil
}
