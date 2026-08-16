package notes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/entityrefs"
	"foldex/internal/folders"
	"foldex/internal/notemedia"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/htmlsanitize"
	"foldex/internal/pkg/listquery"
	"foldex/internal/pkg/pgerr"
	"foldex/internal/pkg/slug"
	"foldex/internal/ports"
	"foldex/internal/tags"
)

type Repository struct {
	pool    *pgxpool.Pool
	storage ports.Uploader
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) WithStorage(storage ports.Uploader) *Repository {
	r.storage = storage
	return r
}

func (r *Repository) RegisterMediaLease(ctx context.Context, uid authctx.UserID, key string) error {
	return notemedia.RegisterLease(ctx, r.pool, uid, key)
}

func (r *Repository) ForgetMediaLease(ctx context.Context, uid authctx.UserID, key string) error {
	return notemedia.ForgetLease(ctx, r.pool, uid, key)
}

// click_count/last_clicked_at are derived from click_log via the LATERAL
// join, mirroring links — there is no denormalized counter on `note` either.
// noteDetailColumns is for Get and public resolution (full body). noteListColumns omits
// body_html/body_text so List never ships up to 512 KiB of HTML per row.
const noteDetailColumns = `
    n.id, n.title, n.slug, n.body_html, n.body_text,
    COALESCE(cl.cnt, 0) AS click_count,
    cl.last_at AS last_clicked_at,
    n.pinned, n.folder_id, n.cover_url, n.created_at, n.updated_at
`

const noteListColumns = `
    n.id, n.title, n.slug, ''::text AS body_html, ''::text AS body_text,
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

func (r *Repository) Create(ctx context.Context, uid authctx.UserID, in CreateInput) (Note, error) {
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

	id, err := slug.CreateWithRetry(ctx, r.pool, baseSlug, userSupplied, isSlugUniqueViolation,
		func(ctx context.Context, tx pgx.Tx, candidate string) (int64, error) {
			var id int64
			err := tx.QueryRow(ctx, `
            INSERT INTO note (user_id, title, slug, body_html, body_text, pinned, folder_id)
            VALUES ($1, $2, $3, $4, $5, $6, $7)
            RETURNING id
        `, int64(uid), in.Title, candidate, bodyHTML, bodyText, in.Pinned, in.FolderID).Scan(&id)
			if err != nil && !isSlugUniqueViolation(err) {
				return 0, fmt.Errorf("insert note: %w", err)
			}
			return id, err
		},
		func(ctx context.Context, tx pgx.Tx, id int64) error {
			if err := setNoteTags(ctx, tx, uid, id, in.TagIDs, in.PendingTags); err != nil {
				return err
			}
			_, err := notemedia.SyncRefs(ctx, tx, uid, id, notemedia.Keys(bodyHTML))
			return err
		},
	)
	if userSupplied && isSlugUniqueViolation(err) {
		return Note{}, ErrSlugTaken
	}
	if err != nil {
		return Note{}, err
	}
	return r.Get(ctx, uid, id)
}

// note_slug_unique stays GLOBAL after 000017: /n/{slug} resolves with no
// session, so the slug namespace cannot be per-user.
func isSlugUniqueViolation(err error) bool {
	return pgerr.UniqueConstraint(err) == "note_slug_unique"
}

// Get returns the note owned by uid. Another user's note reports
// domainerr.ErrNotFound, never a permission error; see links.Get.
func (r *Repository) Get(ctx context.Context, uid authctx.UserID, id int64) (Note, error) {
	var n Note
	err := scanNote(r.pool.QueryRow(ctx, `SELECT `+noteDetailColumns+noteFrom+` WHERE n.user_id = $1 AND n.id = $2`, int64(uid), id), &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return Note{}, domainerr.ErrNotFound
	}
	if err != nil {
		return Note{}, fmt.Errorf("get note: %w", err)
	}
	tagsByNote, err := r.tagsFor(ctx, uid, []int64{id})
	if err != nil {
		return Note{}, err
	}
	n.Tags = tagsByNote[id]
	if n.Tags == nil {
		n.Tags = []tags.Chip{}
	}
	return n, nil
}

func (r *Repository) List(ctx context.Context, uid authctx.UserID, q ListQuery) ([]Note, error) {
	planner := listquery.NewPlanner(q)
	scope := planner.AddScope(uid, listquery.NoteEntity(folders.SQLNotInLockedFolder("n")))
	page := planner.AddPage(listquery.NoteOrder())
	sql := `SELECT ` + noteListColumns + noteFrom + " WHERE " + strings.Join(scope.Where, " AND ")
	sql += fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", page.OrderBy, page.LimitArg, page.OffsetArg)

	rows, err := r.pool.Query(ctx, sql, planner.Args()...)
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
		n.Tags = []tags.Chip{}
		out = append(out, n)
		ids = append(ids, n.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	tagsByNote, err := r.tagsFor(ctx, uid, ids)
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

func (r *Repository) Update(ctx context.Context, uid authctx.UserID, id int64, in UpdateInput) (Note, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Note{}, err
	}
	defer tx.Rollback(ctx)

	sets := []string{}
	args := []any{}
	i := 1
	var mediaKeys []string
	if in.Title != nil {
		sets = append(sets, fmt.Sprintf("title = $%d", i))
		args = append(args, *in.Title)
		i++
	}
	if in.BodyHTML != nil {
		// Defense in depth — see the matching comment in Create.
		bodyHTML := htmlsanitize.Sanitize(*in.BodyHTML)
		mediaKeys = notemedia.Keys(bodyHTML)
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
		newSlug, err := resolveUpdateSlug(ctx, tx, uid, "note", id, in.Slug, in.Title)
		if err != nil {
			return Note{}, err
		}
		sets = append(sets, fmt.Sprintf("slug = $%d", i))
		args = append(args, newSlug)
		i++
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at = now()")
		args = append(args, int64(uid), id)
		where := fmt.Sprintf(`WHERE user_id = $%d AND id = $%d`, i, i+1)
		i++
		if in.IfMatchUpdatedAt != nil {
			i++
			args = append(args, *in.IfMatchUpdatedAt)
			where += fmt.Sprintf(` AND updated_at = $%d`, i)
		}
		q := fmt.Sprintf(`UPDATE note SET %s %s`, strings.Join(sets, ", "), where)
		ct, err := tx.Exec(ctx, q, args...)
		if err != nil {
			if isSlugUniqueViolation(err) {
				return Note{}, ErrSlugTaken
			}
			return Note{}, fmt.Errorf("update note: %w", err)
		}
		if ct.RowsAffected() == 0 {
			if in.IfMatchUpdatedAt != nil {
				var exists bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM note WHERE user_id = $1 AND id = $2)`, int64(uid), id).Scan(&exists); err != nil {
					return Note{}, fmt.Errorf("check note exists: %w", err)
				}
				if exists {
					return Note{}, ErrStaleWrite
				}
			}
			return Note{}, domainerr.ErrNotFound
		}
	}
	if in.TagIDs != nil || len(in.PendingTags) > 0 {
		// A tag-only PATCH ran no owner-scoped UPDATE above; prove ownership.
		if err := assertNoteOwned(ctx, tx, uid, id); err != nil {
			return Note{}, err
		}
		tagIDs := []int64(nil)
		if in.TagIDs != nil {
			tagIDs = *in.TagIDs
		}
		if err := setNoteTags(ctx, tx, uid, id, tagIDs, in.PendingTags); err != nil {
			return Note{}, err
		}
	}
	var releasedMedia []string
	if in.BodyHTML != nil {
		releasedMedia, err = notemedia.SyncRefs(ctx, tx, uid, id, mediaKeys)
		if err != nil {
			return Note{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Note{}, err
	}
	r.cleanupMedia(context.WithoutCancel(ctx), uid, releasedMedia, r.storage)
	return r.Get(ctx, uid, id)
}

// assertNoteOwned reports ErrNotFound unless the note belongs to uid.
func assertNoteOwned(ctx context.Context, tx pgx.Tx, uid authctx.UserID, id int64) error {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM note WHERE user_id = $1 AND id = $2)`,
		int64(uid), id).Scan(&exists); err != nil {
		return fmt.Errorf("check note owner: %w", err)
	}
	if !exists {
		return domainerr.ErrNotFound
	}
	return nil
}

// Delete removes a note and its dependent link_tag/click_log rows (app-level
// cascade — see migration 000014's comment block: the FK CASCADE that used
// to exist for links was dropped when these tables were polymorphized). Media
// cleanup is authorized only by owner-scoped note_media_ref rows; body_html is
// attacker-authored and never grants delete authority.
func (r *Repository) Delete(ctx context.Context, uid authctx.UserID, id int64, storage ports.Uploader) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := assertNoteOwned(ctx, tx, uid, id); err != nil {
		return err
	}
	releasedMedia, err := notemedia.ReleaseNoteRefs(ctx, tx, uid, id)
	if err != nil {
		return err
	}
	if err := entityrefs.PurgeOne(ctx, tx, "note", id); err != nil {
		return err
	}
	ct, err := tx.Exec(ctx, `DELETE FROM note WHERE user_id = $1 AND id = $2`, int64(uid), id)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domainerr.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete tx: %w", err)
	}

	r.cleanupMedia(context.WithoutCancel(ctx), uid, releasedMedia, storage)
	return nil
}

func extractImageKeys(bodyHTML string) []string {
	return notemedia.Keys(bodyHTML)
}

func (r *Repository) cleanupMedia(ctx context.Context, uid authctx.UserID, keys []string, storage ports.Uploader) {
	if storage == nil || len(keys) == 0 {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _ = notemedia.DeleteOwnedUnreferenced(cleanupCtx, r.pool, uid, keys, storage)
}

func (r *Repository) tagsFor(ctx context.Context, uid authctx.UserID, noteIDs []int64) (map[int64][]tags.Chip, error) {
	return tags.TagsForEntities(ctx, r.pool, uid, "note", noteIDs)
}

func setNoteTags(ctx context.Context, tx pgx.Tx, uid authctx.UserID, noteID int64, tagIDs []int64, pending []tags.CreateInput) error {
	return tags.SetEntityTagsWithPending(ctx, tx, uid, "note", noteID, tagIDs, pending)
}
