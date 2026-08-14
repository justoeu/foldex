package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
)

// streamSnapshotJSON writes uid's content inside the caller-owned transaction.
// Each pgx row is scanned, encoded, and released before the next one is read.
//
// NO auth table is exported — not app_user, session, totp_secret,
// recovery_code, api_token, invite, user_identity or password_reset. The ZIP is
// a file users download and hand around; putting bcrypt hashes, TOTP seeds and
// live refresh tokens in it would turn a convenience feature into a
// credential-theft primitive. See docs/SDD-AUTH-RBAC.md §10.1.
//
// app_setting is no longer exported either: after migration 000017 the only
// keys it held (the master password + hint) live on app_user, per user.
func streamSnapshotJSON(ctx context.Context, tx pgx.Tx, uid authctx.UserID, w io.Writer) (Counts, error) {
	var counts Counts
	var ownerEmail string
	// Informational only. Restore NEVER takes user_id from the ZIP (§10.2), so
	// this cannot be used to plant rows in someone else's account — a mismatch
	// only produces a warning.
	if err := tx.QueryRow(ctx, `SELECT email FROM app_user WHERE id = $1`, int64(uid)).
		Scan(&ownerEmail); err != nil {
		return counts, fmt.Errorf("owner email: %w", err)
	}
	encoder := snapshotStreamEncoder{w: w}
	if err := encoder.raw(`{"version":`); err != nil {
		return counts, err
	}
	if err := encoder.value(DatabaseSnapshotVersion); err != nil {
		return counts, err
	}

	var err error
	counts.Tags, err = encoder.rows(ctx, tx, `,"tags":`, `
		SELECT id, name, color, icon, created_at FROM tag WHERE user_id = $1 ORDER BY id`, []any{int64(uid)}, scanTagRow)
	if err != nil {
		return counts, err
	}
	counts.Folders, err = encoder.rows(ctx, tx, `,"folders":`, `
		SELECT id, name, color, parent_id, password_hash, password_hint, created_at
		FROM folder WHERE user_id = $1 ORDER BY id`, []any{int64(uid)}, scanFolderRow)
	if err != nil {
		return counts, err
	}
	counts.Links, err = encoder.rows(ctx, tx, `,"links":`, `
        SELECT id, url, title, slug, description, favicon_url, og_image_url, pinned,
               preview_status, preview_error, folder_id, created_at, updated_at
		FROM link WHERE user_id = $1 ORDER BY id`, []any{int64(uid)}, scanLinkRow)
	if err != nil {
		return counts, err
	}
	counts.Notes, err = encoder.rows(ctx, tx, `,"notes":`, `
        SELECT id, title, slug, body_html, body_text, pinned, folder_id, cover_url, created_at, updated_at
		FROM note WHERE user_id = $1 ORDER BY id`, []any{int64(uid)}, scanNoteRow)
	if err != nil {
		return counts, err
	}
	// link_tag/click_log are polymorphized (migration 000014) — split the read
	// by entity_kind so the JSON wire shape stays one array per entity kind.
	linkTags, err := encoder.rows(ctx, tx, `,"link_tags":`, `
		SELECT entity_id, tag_id FROM link_tag
		WHERE entity_kind = 'link' AND entity_id IN (SELECT id FROM link WHERE user_id = $1)
		ORDER BY entity_id, tag_id`, []any{int64(uid)}, scanLinkTagRow)
	if err != nil {
		return counts, err
	}
	noteTags, err := encoder.rows(ctx, tx, `,"note_tags":`, `
		SELECT entity_id, tag_id FROM link_tag
		WHERE entity_kind = 'note' AND entity_id IN (SELECT id FROM note WHERE user_id = $1)
		ORDER BY entity_id, tag_id`, []any{int64(uid)}, scanNoteTagRow)
	if err != nil {
		return counts, err
	}
	counts.LinkTags = linkTags + noteTags
	linkClicks, err := encoder.rows(ctx, tx, `,"click_logs":`, `
		SELECT entity_id, clicked_at FROM click_log
		WHERE entity_kind = 'link' AND entity_id IN (SELECT id FROM link WHERE user_id = $1)
		ORDER BY id`, []any{int64(uid)}, scanClickRow)
	if err != nil {
		return counts, err
	}
	noteClicks, err := encoder.rows(ctx, tx, `,"note_clicks":`, `
		SELECT entity_id, clicked_at FROM click_log
		WHERE entity_kind = 'note' AND entity_id IN (SELECT id FROM note WHERE user_id = $1)
		ORDER BY id`, []any{int64(uid)}, scanNoteClickRow)
	if err != nil {
		return counts, err
	}
	counts.ClickLogs = linkClicks + noteClicks
	if err := encoder.raw(`,"app_settings":null,"owner_email":`); err != nil {
		return counts, err
	}
	if err := encoder.value(ownerEmail); err != nil {
		return counts, err
	}
	if err := encoder.raw(`}`); err != nil {
		return counts, err
	}
	return counts, nil
}

type snapshotStreamEncoder struct{ w io.Writer }

func (e snapshotStreamEncoder) raw(value string) error {
	_, err := io.WriteString(e.w, value)
	return err
}

func (e snapshotStreamEncoder) value(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = e.w.Write(encoded)
	return err
}

func (e snapshotStreamEncoder) rows(ctx context.Context, tx pgx.Tx, field, query string, args []any, scan func(pgx.Rows) (any, error)) (int64, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("%s query: %w", field, err)
	}
	return e.writeRows(ctx, field, rows, scan)
}

func (e snapshotStreamEncoder) writeRows(ctx context.Context, field string, rows pgx.Rows, scan func(pgx.Rows) (any, error)) (int64, error) {
	defer rows.Close()
	if err := e.raw(field + `[`); err != nil {
		return 0, err
	}
	var count int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if count > 0 {
			if err := e.raw(`,`); err != nil {
				return count, err
			}
		}
		row, err := scan(rows)
		if err != nil {
			return count, fmt.Errorf("%s scan: %w", field, err)
		}
		if err := e.value(row); err != nil {
			return count, fmt.Errorf("%s encode: %w", field, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("%s rows: %w", field, err)
	}
	if err := e.raw(`]`); err != nil {
		return count, err
	}
	return count, nil
}

func scanTagRow(rows pgx.Rows) (any, error) {
	var row TagRow
	err := rows.Scan(&row.ID, &row.Name, &row.Color, &row.Icon, &row.CreatedAt)
	return row, err
}

func scanFolderRow(rows pgx.Rows) (any, error) {
	var row FolderRow
	err := rows.Scan(&row.ID, &row.Name, &row.Color, &row.ParentID, &row.PasswordHash, &row.PasswordHint, &row.CreatedAt)
	return row, err
}

func scanLinkRow(rows pgx.Rows) (any, error) {
	var row LinkRow
	err := rows.Scan(&row.ID, &row.URL, &row.Title, &row.Slug, &row.Description, &row.FaviconURL,
		&row.OGImageURL, &row.Pinned, &row.PreviewStatus, &row.PreviewError, &row.FolderID,
		&row.CreatedAt, &row.UpdatedAt)
	return row, err
}

func scanNoteRow(rows pgx.Rows) (any, error) {
	var row NoteRow
	err := rows.Scan(&row.ID, &row.Title, &row.Slug, &row.BodyHTML, &row.BodyText, &row.Pinned,
		&row.FolderID, &row.CoverURL, &row.CreatedAt, &row.UpdatedAt)
	return row, err
}

func scanLinkTagRow(rows pgx.Rows) (any, error) {
	var row LinkTagRow
	err := rows.Scan(&row.LinkID, &row.TagID)
	return row, err
}

func scanNoteTagRow(rows pgx.Rows) (any, error) {
	var row NoteTagRow
	err := rows.Scan(&row.NoteID, &row.TagID)
	return row, err
}

func scanClickRow(rows pgx.Rows) (any, error) {
	var row ClickRow
	err := rows.Scan(&row.LinkID, &row.ClickedAt)
	return row, err
}

func scanNoteClickRow(rows pgx.Rows) (any, error) {
	var row NoteClickRow
	err := rows.Scan(&row.NoteID, &row.ClickedAt)
	return row, err
}

// countConflicts checks how many incoming rows would collide with existing
// UNIQUE constraints, without writing.
func countConflicts(ctx context.Context, pool *pgxpool.Pool, uid authctx.UserID, snap *Snapshot) (Conflicts, error) {
	var c Conflicts

	if len(snap.Links) > 0 {
		urls := make([]string, len(snap.Links))
		for i, l := range snap.Links {
			urls[i] = l.URL
		}
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM link WHERE user_id = $2 AND url = ANY($1::text[])`, urls, int64(uid)).Scan(&c.Links); err != nil {
			return c, fmt.Errorf("conflict links: %w", err)
		}
	}
	if len(snap.Tags) > 0 {
		names := make([]string, len(snap.Tags))
		for i, t := range snap.Tags {
			names[i] = t.Name
		}
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM tag WHERE user_id = $2 AND name = ANY($1::text[])`, names, int64(uid)).Scan(&c.Tags); err != nil {
			return c, fmt.Errorf("conflict tags: %w", err)
		}
	}
	// folders have no unique constraint => 0 conflicts by construction.

	return c, nil
}

// objectKeyRE pulls the bucket key out of a stored proxy URL
// (/api/files/screenshots/12.jpg → screenshots/12.jpg).
//
// The leading boundary group is a guard, not decoration. og_image_url is filled
// by preview.Worker straight from a page's <meta property="og:image">, and that
// page belongs to whoever the user bookmarked — so its value is attacker-chosen
// text. Unanchored, `https://attacker.example/x/api/files/screenshots/9.jpg`
// matches and hands out key `screenshots/9.jpg`. Requiring the path to start at
// a string boundary or an HTML attribute delimiter means only a genuinely LOCAL
// proxy path matches; a remote URL always carries a host character in front.
var objectKeyRE = regexp.MustCompile(`(?:^|["'\s(=])/api/files/((?:screenshots|images)/[A-Za-z0-9._-]+)`)

// userObjectKeys enumerates the object-store keys referenced by uid's rows.
//
// Wipe-mode restore needs this because object keys are FLAT — screenshots/{id}.jpg
// with no tenant segment — so the pre-000017 DeleteObjectsPrefix would delete
// every other user's screenshots. Re-keying the whole bucket by user was
// rejected as disproportionate (it would mean rewriting og_image_url on every
// row and moving existing objects); enumerating the caller's own keys achieves
// the same isolation with no migration.
func userObjectKeys(ctx context.Context, tx pgx.Tx, uid authctx.UserID, includeUnreferencedNoteMedia bool) ([]string, error) {
	seen := map[string]struct{}{}
	linkCandidates := map[string]int64{}
	add := func(key string) error {
		if _, exists := seen[key]; exists {
			return nil
		}
		if len(seen) >= maxBackupFileEntries {
			return fmt.Errorf("owner has more than %d object keys", maxBackupFileEntries)
		}
		seen[key] = struct{}{}
		return nil
	}
	if err := scanRows(ctx, tx,
		`SELECT id, COALESCE(og_image_url, '') FROM link WHERE user_id = $1`, []any{int64(uid)},
		func(rows pgx.Rows) error {
			var id int64
			var u string
			if err := rows.Scan(&id, &u); err != nil {
				return err
			}
			for _, match := range objectKeyRE.FindAllStringSubmatch(u, -1) {
				key := match[1]
				if _, candidateID, _, ok := linkObjectID(key); ok {
					if _, exists := linkCandidates[key]; !exists && len(linkCandidates) >= maxBackupFileEntries {
						return fmt.Errorf("owner has more than %d candidate object keys", maxBackupFileEntries)
					}
					linkCandidates[key] = candidateID
				}
			}
			return nil
		}); err != nil {
		return nil, fmt.Errorf("link object keys: %w", err)
	}
	mediaQuery := `SELECT DISTINCT object_key FROM note_media_ref WHERE user_id = $1`
	if includeUnreferencedNoteMedia {
		// Wipe owns pending leases too. Export deliberately uses only refs so an
		// abandoned upload does not become a durable backup file.
		mediaQuery = `SELECT object_key FROM note_media WHERE user_id = $1`
	}
	if err := scanRows(ctx, tx, mediaQuery, []any{int64(uid)},
		func(rows pgx.Rows) error {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			return add(key)
		}); err != nil {
		return nil, fmt.Errorf("note object keys: %w", err)
	}
	// REFERENCING a link key is not owning it: og_image_url can contain remote
	// attacker-controlled text, so id-derived keys are checked against ids uid
	// owns. Note-media ownership never comes from body_html; migration 000022's
	// owner/ref rows above are the only authority, and legacy keys fail closed.
	candidateIDs := make([]int64, 0, len(linkCandidates))
	for _, id := range linkCandidates {
		candidateIDs = append(candidateIDs, id)
	}
	ownedLinks, err := ownedLinkIDs(ctx, tx, uid, candidateIDs)
	if err != nil {
		return nil, err
	}
	for key, id := range linkCandidates {
		if _, ok := ownedLinks[id]; ok {
			if err := add(key); err != nil {
				return nil, err
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out) // deterministic delete order keeps failures reproducible
	return out, nil
}

func ownedLinkIDs(ctx context.Context, tx pgx.Tx, uid authctx.UserID, ids []int64) (map[int64]struct{}, error) {
	out := map[int64]struct{}{}
	if len(ids) == 0 {
		return out, nil
	}
	if err := scanRows(ctx, tx, `SELECT id FROM link WHERE user_id = $1 AND id = ANY($2::bigint[])`, []any{int64(uid), ids},
		func(rows pgx.Rows) error {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out[id] = struct{}{}
			return nil
		}); err != nil {
		return nil, fmt.Errorf("owned link ids: %w", err)
	}
	return out, nil
}
