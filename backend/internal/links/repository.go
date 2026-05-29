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
    l.pinned, l.folder_id, l.created_at, l.updated_at
`

const linkFrom = `
    FROM link l
    LEFT JOIN LATERAL (
        SELECT count(*) AS cnt, max(clicked_at) AS last_at
        FROM click_log
        WHERE link_id = l.id
    ) cl ON TRUE
`

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
            INSERT INTO link (url, title, slug, description, pinned, folder_id)
            VALUES ($1, $2, $3, $4, $5, $6)
            RETURNING id
        `, in.URL, in.Title, candidate, in.Description, in.Pinned, in.FolderID).Scan(&id)
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
	err := r.pool.QueryRow(ctx, `SELECT `+linkColumns+linkFrom+` WHERE l.id = $1`, id).Scan(
		&l.ID, &l.URL, &l.Title, &l.Slug, &l.Description, &l.FaviconURL, &l.OGImageURL,
		&l.ClickCount, &l.PreviewStatus, &l.PreviewError, &l.LastClickedAt,
		&l.Pinned, &l.FolderID, &l.CreatedAt, &l.UpdatedAt,
	)
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
	err := r.pool.QueryRow(ctx, `SELECT `+linkColumns+linkFrom+` WHERE l.slug = $1`, slug).Scan(
		&l.ID, &l.URL, &l.Title, &l.Slug, &l.Description, &l.FaviconURL, &l.OGImageURL,
		&l.ClickCount, &l.PreviewStatus, &l.PreviewError, &l.LastClickedAt,
		&l.Pinned, &l.FolderID, &l.CreatedAt, &l.UpdatedAt,
	)
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
		if err := rows.Scan(
			&l.ID, &l.URL, &l.Title, &l.Slug, &l.Description, &l.FaviconURL, &l.OGImageURL,
			&l.ClickCount, &l.PreviewStatus, &l.PreviewError, &l.LastClickedAt,
			&l.Pinned, &l.FolderID, &l.CreatedAt, &l.UpdatedAt,
		); err != nil {
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
