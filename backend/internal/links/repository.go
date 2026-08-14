package links

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
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/listquery"
	"foldex/internal/pkg/pgerr"
	sharedslug "foldex/internal/pkg/slug"
	"foldex/internal/tags"
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
        WHERE entity_kind = 'link' AND entity_id = l.id
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

func (r *Repository) Create(ctx context.Context, uid authctx.UserID, in CreateInput) (Link, error) {
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

	id, err := sharedslug.CreateWithRetry(ctx, r.pool, slug, userSupplied, isSlugUniqueViolation,
		func(ctx context.Context, tx pgx.Tx, candidate string) (int64, error) {
			var id int64
			err := tx.QueryRow(ctx, `
            INSERT INTO link (user_id, url, title, slug, description, pinned, folder_id, check_interval)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
            RETURNING id
        `, int64(uid), in.URL, in.Title, candidate, in.Description, in.Pinned, in.FolderID, in.CheckInterval).Scan(&id)
			if err != nil && !isURLUniqueViolation(err) && !isSlugUniqueViolation(err) {
				return 0, fmt.Errorf("insert link: %w", err)
			}
			return id, err
		},
		func(ctx context.Context, tx pgx.Tx, id int64) error {
			return setLinkTags(ctx, tx, uid, id, in.TagIDs, in.PendingTags)
		},
	)
	if isURLUniqueViolation(err) {
		return Link{}, ErrURLTaken
	}
	if userSupplied && isSlugUniqueViolation(err) {
		return Link{}, ErrSlugTaken
	}
	if err != nil {
		return Link{}, err
	}
	return r.Get(ctx, uid, id)
}

// link_slug_unique stays GLOBAL after migration 000017: /go/{slug} resolves
// with no session, so the slug namespace cannot be per-user. A slug already
// taken by another tenant still forces the -2/-3 suffix.
func isSlugUniqueViolation(err error) bool {
	return pgerr.UniqueConstraint(err) == "link_slug_unique"
}

func isURLUniqueViolation(err error) bool {
	return pgerr.UniqueConstraint(err) == "link_user_url_unique"
}

// AssertOwned reports domainerr.ErrNotFound unless uid owns link id. It is the
// ownership check for callers that need the ANSWER but not the ROW.
//
// Get is the wrong tool for that: it runs a LEFT JOIN LATERAL over click_log
// plus a second query for tags, and ProxyFile — which serves every card image
// on every grid page load — would discard both. Three round-trips per image
// against the same pool that /api/entries is competing for is a real cost on a
// cold cache; this is one indexed hit on the (user_id, id) unique index.
func (r *Repository) AssertOwned(ctx context.Context, uid authctx.UserID, id int64) error {
	var ok bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM link WHERE user_id = $1 AND id = $2)`,
		int64(uid), id).Scan(&ok); err != nil {
		return fmt.Errorf("assert link owned: %w", err)
	}
	if !ok {
		return domainerr.ErrNotFound
	}
	return nil
}

// Get returns the link owned by uid. A link belonging to another user reports
// domainerr.ErrNotFound, never a permission error, so the id does not become a
// cross-tenant enumeration oracle.
func (r *Repository) Get(ctx context.Context, uid authctx.UserID, id int64) (Link, error) {
	var l Link
	err := scanLink(r.pool.QueryRow(ctx, `SELECT `+linkColumns+linkFrom+` WHERE l.user_id = $1 AND l.id = $2`, int64(uid), id), &l)
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, domainerr.ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("get link: %w", err)
	}
	tags, err := r.tagsFor(ctx, uid, []int64{id})
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
func (r *Repository) GetBySlug(ctx context.Context, uid authctx.UserID, slug string) (Link, error) {
	var l Link
	err := scanLink(r.pool.QueryRow(ctx, `SELECT `+linkColumns+linkFrom+` WHERE l.user_id = $1 AND l.slug = $2`, int64(uid), slug), &l)
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, domainerr.ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("get link by slug: %w", err)
	}
	tags, err := r.tagsFor(ctx, uid, []int64{l.ID})
	if err != nil {
		return Link{}, err
	}
	l.Tags = tags[l.ID]
	if l.Tags == nil {
		l.Tags = []Tag{}
	}
	return l, nil
}

func (r *Repository) List(ctx context.Context, uid authctx.UserID, q ListQuery) ([]Link, error) {
	planner := listquery.NewPlanner(q)
	scope := planner.AddScope(uid, listquery.LinkEntity(folders.SQLNotInLockedFolder("l")))
	page := planner.AddPage(listquery.LinkOrder())
	sql := `SELECT ` + linkColumns + linkFrom + " WHERE " + strings.Join(scope.Where, " AND ")
	sql += fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", page.OrderBy, page.LimitArg, page.OffsetArg)

	rows, err := r.pool.Query(ctx, sql, planner.Args()...)
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
	tagsByLink, err := r.tagsFor(ctx, uid, ids)
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

func (r *Repository) Update(ctx context.Context, uid authctx.UserID, id int64, in UpdateInput) (Link, error) {
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
		newSlug, err := resolveUpdateSlug(ctx, tx, uid, "link", id, in.Slug, in.Title)
		if err != nil {
			return Link{}, err
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
		args = append(args, int64(uid), id)
		where := fmt.Sprintf(`WHERE user_id = $%d AND id = $%d`, i, i+1)
		i++
		if in.IfMatchUpdatedAt != nil {
			i++
			args = append(args, *in.IfMatchUpdatedAt)
			where += fmt.Sprintf(` AND updated_at = $%d`, i)
		}
		q := fmt.Sprintf(`UPDATE link SET %s %s`, strings.Join(sets, ", "), where)
		ct, err := tx.Exec(ctx, q, args...)
		if err != nil {
			if isURLUniqueViolation(err) {
				return Link{}, ErrURLTaken
			}
			if isSlugUniqueViolation(err) {
				return Link{}, ErrSlugTaken
			}
			return Link{}, fmt.Errorf("update link: %w", err)
		}
		if ct.RowsAffected() == 0 {
			if in.IfMatchUpdatedAt != nil {
				var exists bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM link WHERE user_id = $1 AND id = $2)`, int64(uid), id).Scan(&exists); err != nil {
					return Link{}, fmt.Errorf("check link exists: %w", err)
				}
				if exists {
					return Link{}, ErrStaleWrite
				}
			}
			return Link{}, domainerr.ErrNotFound
		}
	}
	if in.TagIDs != nil || len(in.PendingTags) > 0 {
		// A tag-only PATCH still must not touch a link the caller does not own:
		// nothing above ran a WHERE user_id when `sets` was empty.
		if err := assertLinkOwned(ctx, tx, uid, id); err != nil {
			return Link{}, err
		}
		tagIDs := []int64(nil)
		if in.TagIDs != nil {
			tagIDs = *in.TagIDs
		}
		if err := setLinkTags(ctx, tx, uid, id, tagIDs, in.PendingTags); err != nil {
			return Link{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Link{}, err
	}
	return r.Get(ctx, uid, id)
}

// assertLinkOwned reports ErrNotFound unless the link belongs to uid.
func assertLinkOwned(ctx context.Context, tx pgx.Tx, uid authctx.UserID, id int64) error {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM link WHERE user_id = $1 AND id = $2)`,
		int64(uid), id).Scan(&exists); err != nil {
		return fmt.Errorf("check link owner: %w", err)
	}
	if !exists {
		return domainerr.ErrNotFound
	}
	return nil
}

// Delete removes a link and its dependent link_tag/click_log rows. The
// migration 000014 FK CASCADE (link_tag/click_log → link) was dropped when
// those tables were polymorphized for notes, so the cascade is now app-level:
// both child tables are cleared inside the same tx as the link row delete.
func (r *Repository) Delete(ctx context.Context, uid authctx.UserID, id int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Ownership is proven first: link_tag and click_log carry no user_id, so
	// their DELETEs cannot be scoped and would otherwise purge another tenant's
	// rows for a colliding entity_id before the link DELETE reported 404.
	if err := assertLinkOwned(ctx, tx, uid, id); err != nil {
		return err
	}
	if err := entityrefs.PurgeOne(ctx, tx, "link", id); err != nil {
		return err
	}
	ct, err := tx.Exec(ctx, `DELETE FROM link WHERE user_id = $1 AND id = $2`, int64(uid), id)
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domainerr.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete tx: %w", err)
	}
	return nil
}

// UpdateOGImage sets the og_image_url field for the given link. Side effect:
// preview_status is also forced to 'ok' (and preview_error cleared) — a manual
// upload means "the user supplied the image, the preview pipeline is done".
// This both stops the worker from auto-screenshotting later AND removes the
// "capturando…" label in the UI immediately.
func (r *Repository) UpdateOGImage(ctx context.Context, uid authctx.UserID, id int64, imageURL string) error {
	ct, err := r.pool.Exec(ctx, `
        UPDATE link
        SET og_image_url   = $1,
            preview_status = 'ok',
            preview_error  = NULL,
            updated_at     = now()
        WHERE user_id = $2 AND id = $3
    `, imageURL, int64(uid), id)
	if err != nil {
		return fmt.Errorf("update og_image_url: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domainerr.ErrNotFound
	}
	return nil
}

// ReplaceOGImage publishes a manual image and returns the exact local URL it
// superseded so the caller can safely clean up after the row lock is released.
func (r *Repository) ReplaceOGImage(ctx context.Context, uid authctx.UserID, id int64, imageURL string) (*string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin replace og_image_url: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var previous *string
	err = tx.QueryRow(ctx, `
		SELECT og_image_url
		FROM link
		WHERE user_id = $1 AND id = $2
		FOR UPDATE
	`, int64(uid), id).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domainerr.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock link image: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE link
		SET og_image_url   = $1,
		    preview_status = 'ok',
		    preview_error  = NULL,
		    updated_at     = now()
		WHERE user_id = $2 AND id = $3
	`, imageURL, int64(uid), id); err != nil {
		return nil, fmt.Errorf("replace og_image_url: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit replace og_image_url: %w", err)
	}
	return previous, nil
}

// UpdateOGImageIfUnchanged publishes a captured image only while the exact row
// observed before the expensive capture still exists unchanged.
func (r *Repository) UpdateOGImageIfUnchanged(ctx context.Context, uid authctx.UserID, id int64, imageURL string, expectedUpdatedAt time.Time) (bool, error) {
	ct, err := r.pool.Exec(ctx, `
		UPDATE link
		SET og_image_url   = $1,
		    preview_status = 'ok',
		    preview_error  = NULL,
		    updated_at     = now()
		WHERE user_id = $2 AND id = $3 AND updated_at = $4
	`, imageURL, int64(uid), id, expectedUpdatedAt)
	if err != nil {
		return false, fmt.Errorf("conditionally update og_image_url: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}

// ClearOGImage sets og_image_url to NULL for the given link.
func (r *Repository) ClearOGImage(ctx context.Context, uid authctx.UserID, id int64) error {
	ct, err := r.pool.Exec(ctx, `
        UPDATE link
        SET og_image_url = NULL,
            updated_at   = now()
        WHERE user_id = $1 AND id = $2
    `, int64(uid), id)
	if err != nil {
		return fmt.Errorf("clear og_image_url: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domainerr.ErrNotFound
	}
	return nil
}

// MarkChangeSeen flips `change_seen_at` to now() so the unseen-badge in the
// UI clears. No-op (404) if the link has no detected change yet — without
// that guard a stale `change_seen_at > last_change_detected_at` row could
// suppress the badge forever once the next detection fires.
func (r *Repository) MarkChangeSeen(ctx context.Context, uid authctx.UserID, id int64) error {
	ct, err := r.pool.Exec(ctx, `
        UPDATE link
        SET change_seen_at = now()
        WHERE user_id = $1 AND id = $2 AND last_change_detected_at IS NOT NULL
    `, int64(uid), id)
	if err != nil {
		return fmt.Errorf("mark change seen: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domainerr.ErrNotFound
	}
	return nil
}

// ListRecentChanges feeds the sidebar's "Recent updates" section. Returns the
// most recently changed links within the given window (capped at limit).
// Pinned ordering does NOT apply here — sort is purely by detection time, so
// the user sees the freshest update first.
func (r *Repository) ListRecentChanges(ctx context.Context, uid authctx.UserID, sinceSeconds, limit int) ([]Link, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if sinceSeconds <= 0 {
		sinceSeconds = 7 * 24 * 60 * 60
	}
	sql := `SELECT ` + linkColumns + linkFrom + `
        WHERE l.user_id = $1
          AND l.last_change_detected_at IS NOT NULL
          AND l.last_change_detected_at > now() - make_interval(secs => $2::int)
          AND ` + folders.SQLNotInLockedFolder("l") + `
        ORDER BY l.last_change_detected_at DESC
        LIMIT $3`
	rows, err := r.pool.Query(ctx, sql, int64(uid), sinceSeconds, limit)
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
	tagsByLink, err := r.tagsFor(ctx, uid, ids)
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

func (r *Repository) tagsFor(ctx context.Context, uid authctx.UserID, linkIDs []int64) (map[int64][]Tag, error) {
	return tags.TagsForEntities(ctx, r.pool, uid, "link", linkIDs)
}

func setLinkTags(ctx context.Context, tx pgx.Tx, uid authctx.UserID, linkID int64, tagIDs []int64, pending []tags.CreateInput) error {
	return tags.SetEntityTagsWithPending(ctx, tx, uid, "link", linkID, tagIDs, pending)
}
