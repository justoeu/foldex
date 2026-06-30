package notes

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/links"
	"foldex/internal/pkg/htmlsanitize"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/slug"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// click_count/last_clicked_at are derived from click_log via the LATERAL
// join, mirroring links — there is no denormalized counter on `note` either.
const noteColumns = `
    n.id, n.title, n.slug, n.body_html, n.body_text,
    COALESCE(cl.cnt, 0) AS click_count,
    cl.last_at AS last_clicked_at,
    n.pinned, n.folder_id, n.cover_url, n.created_at, n.updated_at
`

const noteFrom = `
    FROM note n
    LEFT JOIN LATERAL (
        SELECT count(*) AS cnt, max(clicked_at) AS last_at
        FROM click_log
        WHERE entity_kind = 'note' AND entity_id = n.id
    ) cl ON TRUE
`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanNote(s rowScanner, n *Note) error {
	return s.Scan(
		&n.ID, &n.Title, &n.Slug, &n.BodyHTML, &n.BodyText,
		&n.ClickCount, &n.LastClickedAt,
		&n.Pinned, &n.FolderID, &n.CoverURL, &n.CreatedAt, &n.UpdatedAt,
	)
}

func (r *Repository) Create(ctx context.Context, in CreateInput) (Note, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Note{}, err
	}
	defer tx.Rollback(ctx)

	userSupplied := in.Slug != nil
	var baseSlug string
	if userSupplied {
		baseSlug = *in.Slug
	} else {
		baseSlug = slug.Slugify(in.Title)
		if baseSlug == "" {
			baseSlug = "note-untitled"
		}
	}

	// Defense in depth: CreateInput.Normalize() (called by the handler) already
	// sanitizes BodyHTML, but sanitizing again here — idempotent, cheap — means
	// the repository is safe even if a future caller (a script, a new
	// endpoint) reaches it directly without going through the DTO layer.
	bodyHTML := htmlsanitize.Sanitize(in.BodyHTML)
	bodyText := htmlsanitize.PlainText(bodyHTML)

	var id int64
	for attempt := 0; attempt < 100; attempt++ {
		candidate := baseSlug
		if attempt > 0 {
			candidate = fmt.Sprintf("%s-%d", baseSlug, attempt+1)
		}
		err = tx.QueryRow(ctx, `
            INSERT INTO note (title, slug, body_html, body_text, pinned, folder_id)
            VALUES ($1, $2, $3, $4, $5, $6)
            RETURNING id
        `, in.Title, candidate, bodyHTML, bodyText, in.Pinned, in.FolderID).Scan(&id)
		if err == nil {
			break
		}
		if isSlugUniqueViolation(err) {
			if userSupplied {
				return Note{}, httperr.New(409, "slug_taken", "slug already in use")
			}
			_ = tx.Rollback(ctx)
			tx, err = r.pool.Begin(ctx)
			if err != nil {
				return Note{}, fmt.Errorf("retry begin tx: %w", err)
			}
			continue
		}
		return Note{}, fmt.Errorf("insert note: %w", err)
	}
	if id == 0 {
		return Note{}, fmt.Errorf("could not allocate a unique slug after 100 attempts")
	}

	if err := setNoteTags(ctx, tx, id, in.TagIDs); err != nil {
		return Note{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Note{}, err
	}
	return r.Get(ctx, id)
}

func uniqueConstraint(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return ""
	}
	return pgErr.ConstraintName
}

func isSlugUniqueViolation(err error) bool {
	return uniqueConstraint(err) == "note_slug_unique"
}

func (r *Repository) Get(ctx context.Context, id int64) (Note, error) {
	var n Note
	err := scanNote(r.pool.QueryRow(ctx, `SELECT `+noteColumns+noteFrom+` WHERE n.id = $1`, id), &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return Note{}, httperr.ErrNotFound
	}
	if err != nil {
		return Note{}, fmt.Errorf("get note: %w", err)
	}
	tags, err := r.tagsFor(ctx, []int64{id})
	if err != nil {
		return Note{}, err
	}
	n.Tags = tags[id]
	if n.Tags == nil {
		n.Tags = []links.Tag{}
	}
	return n, nil
}

// GetBySlug is the slug-keyed sibling of Get, used by the public /n/{slug} route.
func (r *Repository) GetBySlug(ctx context.Context, s string) (Note, error) {
	var n Note
	err := scanNote(r.pool.QueryRow(ctx, `SELECT `+noteColumns+noteFrom+` WHERE n.slug = $1`, s), &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return Note{}, httperr.ErrNotFound
	}
	if err != nil {
		return Note{}, fmt.Errorf("get note by slug: %w", err)
	}
	tags, err := r.tagsFor(ctx, []int64{n.ID})
	if err != nil {
		return Note{}, err
	}
	n.Tags = tags[n.ID]
	if n.Tags == nil {
		n.Tags = []links.Tag{}
	}
	return n, nil
}

func (r *Repository) List(ctx context.Context, q ListQuery) ([]Note, error) {
	args := []any{}
	where := []string{}

	if q.Q != "" {
		args = append(args, "%"+q.Q+"%")
		idx := len(args)
		where = append(where, fmt.Sprintf("(n.title ILIKE $%d OR n.body_text ILIKE $%d)", idx, idx))
	}
	if len(q.TagIDs) > 0 {
		args = append(args, q.TagIDs)
		idx := len(args)
		where = append(where, fmt.Sprintf(`n.id IN (
            SELECT entity_id FROM link_tag
            WHERE entity_kind = 'note' AND tag_id = ANY($%d)
            GROUP BY entity_id
            HAVING count(DISTINCT tag_id) = %d
        )`, idx, len(q.TagIDs)))
	}
	if q.FolderID != nil {
		args = append(args, *q.FolderID)
		where = append(where, fmt.Sprintf("n.folder_id = $%d", len(args)))
	} else if q.Ungrouped {
		where = append(where, "n.folder_id IS NULL")
	}

	order := "n.pinned DESC, n.created_at DESC"
	switch q.Sort {
	case "clicks":
		order = "n.pinned DESC, COALESCE(cl.cnt, 0) DESC, n.created_at DESC"
	case "recent":
		order = "n.pinned DESC, COALESCE(cl.last_at, n.created_at) DESC"
	case "alpha":
		order = "n.pinned DESC, lower(n.title) ASC, n.created_at DESC"
	case "alpha_desc":
		order = "n.pinned DESC, lower(n.title) DESC, n.created_at DESC"
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

	sql := `SELECT ` + noteColumns + noteFrom
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", order, len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	defer rows.Close()

	out := make([]Note, 0)
	ids := []int64{}
	for rows.Next() {
		var n Note
		if err := scanNote(rows, &n); err != nil {
			return nil, err
		}
		n.Tags = []links.Tag{}
		out = append(out, n)
		ids = append(ids, n.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	tagsByNote, err := r.tagsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if t, ok := tagsByNote[out[i].ID]; ok {
			out[i].Tags = t
		}
	}
	return out, nil
}

func (r *Repository) Update(ctx context.Context, id int64, in UpdateInput) (Note, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Note{}, err
	}
	defer tx.Rollback(ctx)

	sets := []string{}
	args := []any{}
	i := 1
	if in.Title != nil {
		sets = append(sets, fmt.Sprintf("title = $%d", i))
		args = append(args, *in.Title)
		i++
	}
	if in.BodyHTML != nil {
		// Defense in depth — see the matching comment in Create.
		bodyHTML := htmlsanitize.Sanitize(*in.BodyHTML)
		sets = append(sets, fmt.Sprintf("body_html = $%d", i))
		args = append(args, bodyHTML)
		i++
		sets = append(sets, fmt.Sprintf("body_text = $%d", i))
		args = append(args, htmlsanitize.PlainText(bodyHTML))
		i++
	}
	if in.Pinned != nil {
		sets = append(sets, fmt.Sprintf("pinned = $%d", i))
		args = append(args, *in.Pinned)
		i++
	}
	if in.FolderIDSet {
		sets = append(sets, fmt.Sprintf("folder_id = $%d", i))
		args = append(args, in.FolderID)
		i++
	}
	if in.SlugSet {
		newSlug := ""
		if in.Slug != nil {
			newSlug = *in.Slug
		} else {
			currentTitle := ""
			if in.Title != nil {
				currentTitle = *in.Title
			} else {
				if err := tx.QueryRow(ctx, `SELECT title FROM note WHERE id = $1`, id).Scan(&currentTitle); err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						return Note{}, httperr.ErrNotFound
					}
					return Note{}, fmt.Errorf("read title for slug regen: %w", err)
				}
			}
			newSlug = slug.Slugify(currentTitle)
			if newSlug == "" {
				newSlug = fmt.Sprintf("note-%d", id)
			}
		}
		sets = append(sets, fmt.Sprintf("slug = $%d", i))
		args = append(args, newSlug)
		i++
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at = now()")
		args = append(args, id)
		q := fmt.Sprintf(`UPDATE note SET %s WHERE id = $%d`, strings.Join(sets, ", "), i)
		ct, err := tx.Exec(ctx, q, args...)
		if err != nil {
			if isSlugUniqueViolation(err) {
				return Note{}, httperr.New(409, "slug_taken", "slug already in use")
			}
			return Note{}, fmt.Errorf("update note: %w", err)
		}
		if ct.RowsAffected() == 0 {
			return Note{}, httperr.ErrNotFound
		}
	}
	if in.TagIDs != nil {
		if err := setNoteTags(ctx, tx, id, *in.TagIDs); err != nil {
			return Note{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Note{}, err
	}
	return r.Get(ctx, id)
}

// imageKeyRE extracts notes/<uuid>.<ext> object keys referenced in a note's
// body_html, used by Delete's best-effort image cleanup. Matches the proxy
// URL shape the image-upload endpoint hands back: /api/files/notes/<key>.
var imageKeyRE = regexp.MustCompile(`/api/files/(notes/[A-Za-z0-9._-]+)`)

// Delete removes a note and its dependent link_tag/click_log rows (app-level
// cascade — see migration 000014's comment block: the FK CASCADE that used
// to exist for links was dropped when these tables were polymorphized, and
// was never recreated for notes either). Inline images still referenced in
// body_html at delete time are best-effort removed from object storage;
// images inserted into the editor and then removed before save are a known
// v1 gap (no orphan sweep job).
func (r *Repository) Delete(ctx context.Context, id int64, storage links.Uploader) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var bodyHTML string
	err = tx.QueryRow(ctx, `SELECT body_html FROM note WHERE id = $1`, id).Scan(&bodyHTML)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read note body for delete: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM link_tag WHERE entity_kind = 'note' AND entity_id = $1`, id); err != nil {
		return fmt.Errorf("delete link_tag: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM click_log WHERE entity_kind = 'note' AND entity_id = $1`, id); err != nil {
		return fmt.Errorf("delete click_log: %w", err)
	}
	ct, err := tx.Exec(ctx, `DELETE FROM note WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return httperr.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete tx: %w", err)
	}

	if storage != nil {
		for _, key := range extractImageKeys(bodyHTML) {
			_ = storage.DeleteObject(context.WithoutCancel(ctx), key)
		}
	}
	return nil
}

func extractImageKeys(bodyHTML string) []string {
	matches := imageKeyRE.FindAllStringSubmatch(bodyHTML, -1)
	out := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, m := range matches {
		if len(m) < 2 || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// ViewAndResolve resolves id-or-slug and logs a click_log row in the same
// tx, mirroring links.ClickAndResolve(BySlug) — used by GET /n/{id-or-slug}.
func (r *Repository) ViewAndResolve(ctx context.Context, idOrSlug string) (Note, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Note{}, fmt.Errorf("begin view tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var id int64
	where, arg := "slug = $1", any(idOrSlug)
	if n, ok := parsePositiveID(idOrSlug); ok {
		where, arg = "id = $1", any(n)
	}
	err = tx.QueryRow(ctx, `SELECT id FROM note WHERE `+where, arg).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Note{}, httperr.ErrNotFound
	}
	if err != nil {
		return Note{}, fmt.Errorf("resolve note: %w", err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO click_log (entity_kind, entity_id) VALUES ('note', $1)`, id); err != nil {
		return Note{}, fmt.Errorf("insert click_log: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Note{}, fmt.Errorf("commit view tx: %w", err)
	}
	return r.Get(ctx, id)
}

func parsePositiveID(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, n > 0
}

func (r *Repository) tagsFor(ctx context.Context, noteIDs []int64) (map[int64][]links.Tag, error) {
	out := map[int64][]links.Tag{}
	if len(noteIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
        SELECT lt.entity_id, t.id, t.name, t.color, t.icon
        FROM link_tag lt
        JOIN tag t ON t.id = lt.tag_id
        WHERE lt.entity_kind = 'note' AND lt.entity_id = ANY($1)
        ORDER BY t.name ASC
    `, noteIDs)
	if err != nil {
		return nil, fmt.Errorf("tags for notes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var noteID int64
		var t links.Tag
		if err := rows.Scan(&noteID, &t.ID, &t.Name, &t.Color, &t.Icon); err != nil {
			return nil, err
		}
		out[noteID] = append(out[noteID], t)
	}
	return out, rows.Err()
}

func setNoteTags(ctx context.Context, tx pgx.Tx, noteID int64, tagIDs []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM link_tag WHERE entity_kind = 'note' AND entity_id = $1`, noteID); err != nil {
		return fmt.Errorf("clear note tags: %w", err)
	}
	if len(tagIDs) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(tagIDs))
	for _, tid := range tagIDs {
		rows = append(rows, []any{"note", noteID, tid})
	}
	_, err := tx.CopyFrom(ctx,
		pgx.Identifier{"link_tag"},
		[]string{"entity_kind", "entity_id", "tag_id"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("insert note tags: %w", err)
	}
	return nil
}
