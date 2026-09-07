package notes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/entityrefs"
	"foldex/internal/folders"
	"foldex/internal/notemedia"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/crudupdate"
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
	logger  *slog.Logger
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) WithStorage(storage ports.Uploader) *Repository {
	r.storage = storage
	return r
}

func (r *Repository) WithLogger(logger *slog.Logger) *Repository {
	r.logger = logger
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
			if err := tags.SetEntityTagsWithPending(ctx, tx, uid, "note", id, in.TagIDs, in.PendingTags); err != nil {
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

	b := &crudupdate.SetBuilder{}
	mediaKeys, err := applyNoteColumns(ctx, tx, uid, id, b, in)
	if err != nil {
		return Note{}, err
	}
	if !b.Empty() {
		if err := crudupdate.Exec(ctx, tx, crudupdate.Request{
			Entity:   "note",
			UID:      uid,
			ID:       id,
			IfMatch:  in.IfMatchUpdatedAt,
			StaleErr: ErrStaleWrite,
			Translate: func(err error) error {
				if isSlugUniqueViolation(err) {
					return ErrSlugTaken
				}
				return nil
			},
		}, b); err != nil {
			return Note{}, err
		}
	}
	if err := crudupdate.SetEntityTags(ctx, tx, "note", uid, id, crudupdate.TagChanges{
		TagIDs:      in.TagIDs,
		PendingTags: in.PendingTags,
	}); err != nil {
		return Note{}, err
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

// applyNoteColumns writes the caller-supplied column assignments of a PATCH
// onto b, returning the media keys referenced by the new body (for the
// post-commit reference sync).
func applyNoteColumns(ctx context.Context, tx pgx.Tx, uid authctx.UserID, id int64, b *crudupdate.SetBuilder, in UpdateInput) ([]string, error) {
	var mediaKeys []string
	if in.Title != nil {
		b.Set("title", *in.Title)
	}
	if in.BodyHTML != nil {
		// Defense in depth — see the matching comment in Create.
		bodyHTML := htmlsanitize.Sanitize(*in.BodyHTML)
		mediaKeys = notemedia.Keys(bodyHTML)
		b.Set("body_html", bodyHTML)
		b.Set("body_text", htmlsanitize.PlainText(bodyHTML))
	}
	if in.Pinned != nil {
		b.Set("pinned", *in.Pinned)
	}
	if in.FolderIDSet {
		b.Set("folder_id", in.FolderID)
	}
	if in.SlugSet {
		newSlug, err := slug.ResolveUpdate(ctx, tx, uid, "note", id, in.Slug, in.Title, "note")
		if err != nil {
			return nil, err
		}
		b.Set("slug", newSlug)
	}
	return mediaKeys, nil
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

	if err := crudupdate.AssertOwned(ctx, tx, "note", uid, id); err != nil {
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

func (r *Repository) cleanupMedia(ctx context.Context, uid authctx.UserID, keys []string, storage ports.Uploader) {
	if storage == nil || len(keys) == 0 {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	deleted, err := notemedia.DeleteOwnedUnreferenced(cleanupCtx, r.pool, uid, keys, asObjectDeleter(storage))
	if err != nil {
		r.loggerOrDefault().Warn("note media cleanup failed", "deleted", deleted, "err", err)
	}
}

func (r *Repository) loggerOrDefault() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}

type uploaderObjectDeleter struct{ ports.Uploader }

func (d uploaderObjectDeleter) DeleteObject(ctx context.Context, key string) error {
	return d.Uploader.DeleteObject(ctx, key)
}

func (d uploaderObjectDeleter) DeleteObjects(ctx context.Context, keys []string) error {
	for _, key := range keys {
		if err := d.DeleteObject(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func asObjectDeleter(storage ports.Uploader) notemedia.ObjectDeleter {
	if d, ok := any(storage).(notemedia.ObjectDeleter); ok {
		return d
	}
	return uploaderObjectDeleter{storage}
}

func (r *Repository) tagsFor(ctx context.Context, uid authctx.UserID, noteIDs []int64) (map[int64][]tags.Chip, error) {
	return tags.TagsForEntities(ctx, r.pool, uid, "note", noteIDs)
}
