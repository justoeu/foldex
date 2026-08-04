package backup

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
)

// readSnapshot reads uid's content inside the given tx and returns a Snapshot.
// Caller is responsible for the transaction (and the isolation level).
//
// NO auth table is exported — not app_user, session, totp_secret,
// recovery_code, api_token, invite, user_identity or password_reset. The ZIP is
// a file users download and hand around; putting bcrypt hashes, TOTP seeds and
// live refresh tokens in it would turn a convenience feature into a
// credential-theft primitive. See docs/SDD-AUTH-RBAC.md §10.1.
//
// app_setting is no longer exported either: after migration 000017 the only
// keys it held (the master password + hint) live on app_user, per user.
func readSnapshot(ctx context.Context, tx pgx.Tx, uid authctx.UserID) (*Snapshot, error) {
	snap := &Snapshot{Version: DatabaseSnapshotVersion}

	// Informational only. Restore NEVER takes user_id from the ZIP (§10.2), so
	// this cannot be used to plant rows in someone else's account — a mismatch
	// only produces a warning.
	if err := tx.QueryRow(ctx, `SELECT email FROM app_user WHERE id = $1`, int64(uid)).
		Scan(&snap.OwnerEmail); err != nil {
		return nil, fmt.Errorf("owner email: %w", err)
	}

	if err := scanRows(ctx, tx, `SELECT id, name, color, icon, created_at FROM tag WHERE user_id = $1 ORDER BY id`, []any{int64(uid)},
		func(rows pgx.Rows) error {
			var t TagRow
			if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Icon, &t.CreatedAt); err != nil {
				return err
			}
			snap.Tags = append(snap.Tags, t)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("tags: %w", err)
	}

	if err := scanRows(ctx, tx, `SELECT id, name, color, parent_id, password_hash, password_hint, created_at FROM folder WHERE user_id = $1 ORDER BY id`, []any{int64(uid)},
		func(rows pgx.Rows) error {
			var f FolderRow
			if err := rows.Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.PasswordHash, &f.PasswordHint, &f.CreatedAt); err != nil {
				return err
			}
			snap.Folders = append(snap.Folders, f)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("folders: %w", err)
	}

	if err := scanRows(ctx, tx, `
        SELECT id, url, title, slug, description, favicon_url, og_image_url, pinned,
               preview_status, preview_error, folder_id, created_at, updated_at
        FROM link WHERE user_id = $1 ORDER BY id`, []any{int64(uid)},
		func(rows pgx.Rows) error {
			var l LinkRow
			if err := rows.Scan(&l.ID, &l.URL, &l.Title, &l.Slug, &l.Description, &l.FaviconURL,
				&l.OGImageURL, &l.Pinned, &l.PreviewStatus, &l.PreviewError, &l.FolderID,
				&l.CreatedAt, &l.UpdatedAt); err != nil {
				return err
			}
			snap.Links = append(snap.Links, l)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("links: %w", err)
	}

	if err := scanRows(ctx, tx, `
        SELECT id, title, slug, body_html, body_text, pinned, folder_id, cover_url, created_at, updated_at
        FROM note WHERE user_id = $1 ORDER BY id`, []any{int64(uid)},
		func(rows pgx.Rows) error {
			var n NoteRow
			if err := rows.Scan(&n.ID, &n.Title, &n.Slug, &n.BodyHTML, &n.BodyText, &n.Pinned,
				&n.FolderID, &n.CoverURL, &n.CreatedAt, &n.UpdatedAt); err != nil {
				return err
			}
			snap.Notes = append(snap.Notes, n)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("notes: %w", err)
	}

	// link_tag/click_log are polymorphized (migration 000014) — split the read
	// by entity_kind so the JSON wire shape stays one array per entity kind.
	if err := scanRows(ctx, tx, `SELECT entity_id, tag_id FROM link_tag WHERE entity_kind = 'link' AND entity_id IN (SELECT id FROM link WHERE user_id = $1) ORDER BY entity_id, tag_id`, []any{int64(uid)},
		func(rows pgx.Rows) error {
			var lt LinkTagRow
			if err := rows.Scan(&lt.LinkID, &lt.TagID); err != nil {
				return err
			}
			snap.LinkTags = append(snap.LinkTags, lt)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("link_tags: %w", err)
	}

	if err := scanRows(ctx, tx, `SELECT entity_id, tag_id FROM link_tag WHERE entity_kind = 'note' AND entity_id IN (SELECT id FROM note WHERE user_id = $1) ORDER BY entity_id, tag_id`, []any{int64(uid)},
		func(rows pgx.Rows) error {
			var nt NoteTagRow
			if err := rows.Scan(&nt.NoteID, &nt.TagID); err != nil {
				return err
			}
			snap.NoteTags = append(snap.NoteTags, nt)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("note_tags: %w", err)
	}

	if err := scanRows(ctx, tx, `SELECT entity_id, clicked_at FROM click_log WHERE entity_kind = 'link' AND entity_id IN (SELECT id FROM link WHERE user_id = $1) ORDER BY id`, []any{int64(uid)},
		func(rows pgx.Rows) error {
			var c ClickRow
			if err := rows.Scan(&c.LinkID, &c.ClickedAt); err != nil {
				return err
			}
			snap.ClickLogs = append(snap.ClickLogs, c)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("click_logs: %w", err)
	}

	if err := scanRows(ctx, tx, `SELECT entity_id, clicked_at FROM click_log WHERE entity_kind = 'note' AND entity_id IN (SELECT id FROM note WHERE user_id = $1) ORDER BY id`, []any{int64(uid)},
		func(rows pgx.Rows) error {
			var c NoteClickRow
			if err := rows.Scan(&c.NoteID, &c.ClickedAt); err != nil {
				return err
			}
			snap.NoteClicks = append(snap.NoteClicks, c)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("note_clicks: %w", err)
	}

	return snap, nil
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
var objectKeyRE = regexp.MustCompile(`/api/files/((?:screenshots|images|notes)/[A-Za-z0-9._-]+)`)

// userObjectKeys enumerates the object-store keys referenced by uid's rows.
//
// Wipe-mode restore needs this because object keys are FLAT — screenshots/{id}.jpg
// with no tenant segment — so the pre-000017 DeleteObjectsPrefix would delete
// every other user's screenshots. Re-keying the whole bucket by user was
// rejected as disproportionate (it would mean rewriting og_image_url on every
// row and moving existing objects); enumerating the caller's own keys achieves
// the same isolation with no migration.
func userObjectKeys(ctx context.Context, tx pgx.Tx, uid authctx.UserID) ([]string, error) {
	seen := map[string]struct{}{}
	add := func(s string) {
		for _, m := range objectKeyRE.FindAllStringSubmatch(s, -1) {
			seen[m[1]] = struct{}{}
		}
	}
	if err := scanRows(ctx, tx,
		`SELECT COALESCE(og_image_url, '') FROM link WHERE user_id = $1`, []any{int64(uid)},
		func(rows pgx.Rows) error {
			var u string
			if err := rows.Scan(&u); err != nil {
				return err
			}
			add(u)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("link object keys: %w", err)
	}
	if err := scanRows(ctx, tx,
		`SELECT COALESCE(cover_url, ''), body_html FROM note WHERE user_id = $1`, []any{int64(uid)},
		func(rows pgx.Rows) error {
			var cover, body string
			if err := rows.Scan(&cover, &body); err != nil {
				return err
			}
			add(cover)
			add(body)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("note object keys: %w", err)
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out) // deterministic delete order keeps failures reproducible
	return out, nil
}
