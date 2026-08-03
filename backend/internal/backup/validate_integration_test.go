//go:build integration

package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backup"
	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/settings"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

func zipFromEntries(t *testing.T, entries map[string][]byte) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	return zr
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func TestValidate_ErrorBranches(t *testing.T) {
	pool := testdb.New(t)
	svc := backup.NewService(pool, newStubBucket(), discardLogger())
	ctx := context.Background()

	t.Run("missing_manifest", func(t *testing.T) {
		zr := zipFromEntries(t, map[string][]byte{
			"database.json": mustJSON(t, backup.Snapshot{Version: backup.DatabaseSnapshotVersion}),
		})
		v, err := svc.Validate(ctx, zr)
		require.NoError(t, err)
		assert.False(t, v.OK)
		require.NotEmpty(t, v.Errors)
		assert.Contains(t, v.Errors[0], "manifest")
	})

	t.Run("bad_kind", func(t *testing.T) {
		zr := zipFromEntries(t, map[string][]byte{
			"manifest.json": mustJSON(t, backup.Manifest{
				Kind: "not-foldex", Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
			}),
		})
		v, err := svc.Validate(ctx, zr)
		require.NoError(t, err)
		assert.False(t, v.OK)
		assert.Contains(t, v.Errors[0], "kind mismatch")
	})

	t.Run("major_version_mismatch", func(t *testing.T) {
		zr := zipFromEntries(t, map[string][]byte{
			"manifest.json": mustJSON(t, backup.Manifest{
				Kind: backup.ManifestKind, Version: "2.0", SchemaVersion: backup.CurrentSchemaVersion,
			}),
		})
		v, err := svc.Validate(ctx, zr)
		require.NoError(t, err)
		assert.False(t, v.OK)
		assert.Contains(t, v.Errors[0], "major version")
	})

	t.Run("schema_too_new", func(t *testing.T) {
		zr := zipFromEntries(t, map[string][]byte{
			"manifest.json": mustJSON(t, backup.Manifest{
				Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion + 50,
			}),
		})
		v, err := svc.Validate(ctx, zr)
		require.NoError(t, err)
		assert.False(t, v.OK)
		assert.Contains(t, v.Errors[0], "schema_version too new")
	})

	t.Run("schema_old_warns", func(t *testing.T) {
		db := mustJSON(t, backup.Snapshot{Version: backup.DatabaseSnapshotVersion})
		zr := zipFromEntries(t, map[string][]byte{
			"manifest.json": mustJSON(t, backup.Manifest{
				Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: 1,
				Checksums: map[string]string{"database.json": sha256hex(db)},
			}),
			"database.json": db,
		})
		v, err := svc.Validate(ctx, zr)
		require.NoError(t, err)
		assert.True(t, v.OK)
		require.NotEmpty(t, v.Warnings)
		assert.Contains(t, v.Warnings[0], "schema_version")
	})

	t.Run("missing_checksum_entry", func(t *testing.T) {
		db := mustJSON(t, backup.Snapshot{Version: backup.DatabaseSnapshotVersion})
		zr := zipFromEntries(t, map[string][]byte{
			"manifest.json": mustJSON(t, backup.Manifest{
				Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
				Checksums: map[string]string{
					"database.json":     sha256hex(db),
					"files/missing.jpg": "sha256:deadbeef",
				},
			}),
			"database.json": db,
		})
		v, err := svc.Validate(ctx, zr)
		require.NoError(t, err)
		assert.False(t, v.OK)
		joined := fmt.Sprint(v.Errors)
		assert.Contains(t, joined, "missing entry")
	})

	t.Run("checksum_mismatch", func(t *testing.T) {
		db := mustJSON(t, backup.Snapshot{Version: backup.DatabaseSnapshotVersion})
		zr := zipFromEntries(t, map[string][]byte{
			"manifest.json": mustJSON(t, backup.Manifest{
				Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
				Checksums: map[string]string{"database.json": "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			}),
			"database.json": db,
		})
		v, err := svc.Validate(ctx, zr)
		require.NoError(t, err)
		assert.False(t, v.OK)
		assert.Contains(t, v.Errors[0], "checksum mismatch")
	})

	t.Run("missing_database_json", func(t *testing.T) {
		zr := zipFromEntries(t, map[string][]byte{
			"manifest.json": mustJSON(t, backup.Manifest{
				Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
				Checksums: map[string]string{},
			}),
		})
		v, err := svc.Validate(ctx, zr)
		require.NoError(t, err)
		assert.False(t, v.OK)
		joined := fmt.Sprint(v.Errors)
		assert.Contains(t, joined, "database.json")
	})

	t.Run("bad_database_json", func(t *testing.T) {
		db := []byte(`{not-json`)
		zr := zipFromEntries(t, map[string][]byte{
			"manifest.json": mustJSON(t, backup.Manifest{
				Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
				Checksums: map[string]string{"database.json": sha256hex(db)},
			}),
			"database.json": db,
		})
		v, err := svc.Validate(ctx, zr)
		require.NoError(t, err)
		assert.False(t, v.OK)
		joined := fmt.Sprint(v.Errors)
		assert.Contains(t, joined, "database.json")
	})

	t.Run("og_image_missing_file_warns", func(t *testing.T) {
		og := "/api/files/screenshots/99.jpg"
		snap := backup.Snapshot{
			Version: backup.DatabaseSnapshotVersion,
			Links: []backup.LinkRow{{
				ID: 1, URL: "https://og.example", Title: "OG", Slug: "og",
				OGImageURL: &og, CreatedAt: time.Now().UTC(),
			}},
		}
		db := mustJSON(t, snap)
		zr := zipFromEntries(t, map[string][]byte{
			"manifest.json": mustJSON(t, backup.Manifest{
				Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
				Checksums: map[string]string{"database.json": sha256hex(db)},
			}),
			"database.json": db,
		})
		v, err := svc.Validate(ctx, zr)
		require.NoError(t, err)
		assert.True(t, v.OK)
		require.NotEmpty(t, v.Warnings)
		assert.Contains(t, v.Warnings[0], "screenshots/99.jpg")
	})

	t.Run("external_og_image_ok", func(t *testing.T) {
		og := "https://cdn.example/img.png"
		snap := backup.Snapshot{
			Version: backup.DatabaseSnapshotVersion,
			Links: []backup.LinkRow{{
				ID: 1, URL: "https://ext.example", Title: "Ext", Slug: "ext",
				OGImageURL: &og, CreatedAt: time.Now().UTC(),
			}},
		}
		db := mustJSON(t, snap)
		zr := zipFromEntries(t, map[string][]byte{
			"manifest.json": mustJSON(t, backup.Manifest{
				Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
				Checksums: map[string]string{"database.json": sha256hex(db)},
			}),
			"database.json": db,
		})
		v, err := svc.Validate(ctx, zr)
		require.NoError(t, err)
		assert.True(t, v.OK)
	})

	t.Run("conflicts_against_live_db", func(t *testing.T) {
		_, err := links.NewRepository(pool).Create(ctx, links.CreateInput{
			URL: "https://conflict-val.example", Title: "C",
		})
		require.NoError(t, err)
		snap := backup.Snapshot{
			Version: backup.DatabaseSnapshotVersion,
			Links: []backup.LinkRow{{
				ID: 50, URL: "https://conflict-val.example", Title: "C", Slug: "c",
				CreatedAt: time.Now().UTC(),
			}},
			Tags: []backup.TagRow{{ID: 1, Name: "fresh-tag-only", Color: "#abc", CreatedAt: time.Now().UTC()}},
		}
		db := mustJSON(t, snap)
		zr := zipFromEntries(t, map[string][]byte{
			"manifest.json": mustJSON(t, backup.Manifest{
				Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
				Checksums: map[string]string{"database.json": sha256hex(db)},
			}),
			"database.json": db,
		})
		v, err := svc.Validate(ctx, zr)
		require.NoError(t, err)
		assert.True(t, v.OK)
		assert.EqualValues(t, 1, v.Conflicts.Links)
	})
}

func TestRestore_EmptySlugAndNoteTags(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	now := time.Now().UTC()
	// Snapshot with empty link/note slugs (unique*Slug empty-base branch via
	// skip/duplicate paths), note tags + note clicks.
	// Wipe uses restoreIdentity (CopyFrom) — only include mapped ids there.
	snap := backup.Snapshot{
		Version: backup.DatabaseSnapshotVersion,
		Tags: []backup.TagRow{
			{ID: 1, Name: "ntag", Color: "#abc", CreatedAt: now},
		},
		Folders: []backup.FolderRow{
			{ID: 1, Name: "NF", Color: "#defabc", CreatedAt: now},
		},
		Links: []backup.LinkRow{
			{ID: 10, URL: "https://empty-slug.example", Title: "EmptySlug",
				Slug: "", FolderID: int64Ptr(1), PreviewStatus: "pending",
				CreatedAt: now, UpdatedAt: now},
		},
		Notes: []backup.NoteRow{
			{ID: 20, Title: "Note One", Slug: "", BodyHTML: "<p>hi</p>",
				FolderID: int64Ptr(1), CreatedAt: now, UpdatedAt: now},
		},
		LinkTags:   []backup.LinkTagRow{{LinkID: 10, TagID: 1}},
		NoteTags:   []backup.NoteTagRow{{NoteID: 20, TagID: 1}},
		ClickLogs:  []backup.ClickRow{{LinkID: 10, ClickedAt: now}},
		NoteClicks: []backup.NoteClickRow{{NoteID: 20, ClickedAt: now}},
	}
	db := mustJSON(t, snap)
	zr := zipFromEntries(t, map[string][]byte{
		"manifest.json": mustJSON(t, backup.Manifest{
			Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
			Checksums: map[string]string{"database.json": sha256hex(db)},
		}),
		"database.json": db,
	})

	rep, err := svc.Restore(ctx, zr, backup.ModeWipe)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Inserted.Links)
	assert.EqualValues(t, 1, rep.Inserted.Notes)

	var linkSlug, noteSlug string
	require.NoError(t, pool.QueryRow(ctx, `SELECT slug FROM link WHERE url=$1`, "https://empty-slug.example").Scan(&linkSlug))
	assert.NotEmpty(t, linkSlug)
	require.NoError(t, pool.QueryRow(ctx, `SELECT slug FROM note WHERE title=$1`, "Note One").Scan(&noteSlug))
	assert.NotEmpty(t, noteSlug)

	// Skip mode: URL/name collisions + re-attach tags/clicks via helpers.
	// Craft a second zip with dangling tag refs so attachPolymorphicTags skip paths fire.
	snap2 := snap
	snap2.LinkTags = append(snap2.LinkTags, backup.LinkTagRow{LinkID: 999, TagID: 1})
	snap2.NoteTags = append(snap2.NoteTags, backup.NoteTagRow{NoteID: 888, TagID: 1})
	snap2.ClickLogs = append(snap2.ClickLogs, backup.ClickRow{LinkID: 999, ClickedAt: now})
	snap2.NoteClicks = append(snap2.NoteClicks, backup.NoteClickRow{NoteID: 888, ClickedAt: now})
	db2 := mustJSON(t, snap2)
	zr2 := zipFromEntries(t, map[string][]byte{
		"manifest.json": mustJSON(t, backup.Manifest{
			Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
			Checksums: map[string]string{"database.json": sha256hex(db2)},
		}),
		"database.json": db2,
	})
	rep2, err := svc.Restore(ctx, zr2, backup.ModeSkip)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep2.Skipped.Links)
	assert.GreaterOrEqual(t, rep2.Skipped.LinkTags, int64(1), "unmapped link_tag/note_tag must count as skipped")
	assert.GreaterOrEqual(t, rep2.Skipped.ClickLogs, int64(1), "unmapped clicks must count as skipped")
}

func TestExport_FullSnapshotRoundTrip(t *testing.T) {
	// Covers readSnapshot arms for notes/note_tags/note_clicks/app_settings
	// and folders with password_hash/hint — the branches empty-DB exports skip.
	pool := testdb.New(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	pw := "folder-secret"
	hint := "not-secret"
	frepo := folders.NewRepository(pool)
	f, err := frepo.Create(ctx, folders.CreateInput{Name: "Locked", Color: "#aabbcc", Password: &pw, PasswordHint: &hint})
	require.NoError(t, err)
	tag, err := tags.NewRepository(pool).Create(ctx, tags.CreateInput{Name: "full-tag", Color: "#abc"})
	require.NoError(t, err)
	l, err := links.NewRepository(pool).Create(ctx, links.CreateInput{
		URL: "https://full-export.example", Title: "Full", TagIDs: []int64{tag.ID}, FolderID: &f.ID,
	})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO click_log (entity_kind, entity_id) VALUES ('link', $1)`, l.ID)
	require.NoError(t, err)

	n, err := notes.NewRepository(pool).Create(ctx, notes.CreateInput{
		Title: "Full Note", BodyHTML: "<p>body</p>", TagIDs: []int64{tag.ID}, FolderID: &f.ID,
	})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO click_log (entity_kind, entity_id) VALUES ('note', $1)`, n.ID)
	require.NoError(t, err)

	// Master password setting (app_setting arm of readSnapshot).
	srepo := settings.NewRepository(pool)
	require.NoError(t, srepo.SetMasterPassword(ctx, "master-pass-ok", nil))

	bucket.objs[fmt.Sprintf("screenshots/%d.jpg", l.ID)] = []byte("img")
	bucket.objs["images/orphan.jpg"] = []byte("orphan")

	var buf bytes.Buffer
	rep, err := svc.Export(ctx, &buf, func(c backup.Counts) error {
		assert.GreaterOrEqual(t, c.Links, int64(1))
		assert.GreaterOrEqual(t, c.Notes, int64(1))
		assert.GreaterOrEqual(t, c.Files, int64(1))
		return nil
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, rep.Counts.Notes, int64(1))
	assert.Greater(t, buf.Len(), 0)

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	v, err := svc.Validate(ctx, zr)
	require.NoError(t, err)
	assert.True(t, v.OK)
}

func TestRestore_UniqueTagNameWalksPast2(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	svc := backup.NewService(pool, newStubBucket(), discardLogger())

	_, err := pool.Exec(ctx, `INSERT INTO tag (name, color) VALUES ('walk', '#abc'), ('walk (2)', '#abc')`)
	require.NoError(t, err)

	now := time.Now().UTC()
	snap := backup.Snapshot{
		Version: backup.DatabaseSnapshotVersion,
		Tags:    []backup.TagRow{{ID: 9, Name: "walk", Color: "#def", CreatedAt: now}},
	}
	db := mustJSON(t, snap)
	zr := zipFromEntries(t, map[string][]byte{
		"manifest.json": mustJSON(t, backup.Manifest{
			Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
			Checksums: map[string]string{"database.json": sha256hex(db)},
		}),
		"database.json": db,
	})
	rep, err := svc.Restore(ctx, zr, backup.ModeDuplicate)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Inserted.Tags)
	assert.True(t, tagNameExists(t, pool, "walk (3)"))
}

func TestRestore_SkipIntoEmptyDB_InsertsEverything(t *testing.T) {
	// Exercises restoreSkip's *inserted* branches (not just ON CONFLICT skip).
	pool := testdb.New(t)
	ctx := context.Background()
	svc := backup.NewService(pool, newStubBucket(), discardLogger())

	now := time.Now().UTC()
	icon := "📚"
	snap := backup.Snapshot{
		Version: backup.DatabaseSnapshotVersion,
		Tags:    []backup.TagRow{{ID: 1, Name: "fresh-tag", Color: "#abc", Icon: &icon, CreatedAt: now}},
		Folders: []backup.FolderRow{{ID: 1, Name: "FreshFolder", Color: "#defabc", CreatedAt: now}},
		Links: []backup.LinkRow{{
			ID: 1, URL: "https://skip-empty.example", Title: "Skip Empty",
			Slug: "skip-empty", PreviewStatus: "ok", FolderID: int64Ptr(1),
			CreatedAt: now, UpdatedAt: now,
		}},
		Notes: []backup.NoteRow{{
			ID: 1, Title: "Skip Note", Slug: "skip-note", BodyHTML: "<p>n</p>",
			FolderID: int64Ptr(1), CreatedAt: now, UpdatedAt: now,
		}},
		LinkTags:   []backup.LinkTagRow{{LinkID: 1, TagID: 1}},
		NoteTags:   []backup.NoteTagRow{{NoteID: 1, TagID: 1}},
		ClickLogs:  []backup.ClickRow{{LinkID: 1, ClickedAt: now}},
		NoteClicks: []backup.NoteClickRow{{NoteID: 1, ClickedAt: now}},
		AppSettings: []backup.AppSettingRow{{
			Key: "master_password_hash", Value: "$2a$10$placeholder", UpdatedAt: now,
		}},
	}
	db := mustJSON(t, snap)
	zr := zipFromEntries(t, map[string][]byte{
		"manifest.json": mustJSON(t, backup.Manifest{
			Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
			Checksums: map[string]string{"database.json": sha256hex(db)},
		}),
		"database.json": db,
	})
	rep, err := svc.Restore(ctx, zr, backup.ModeSkip)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Inserted.Links)
	assert.EqualValues(t, 1, rep.Inserted.Notes)
	assert.EqualValues(t, 1, rep.Inserted.Tags)
	assert.EqualValues(t, 1, rep.Inserted.Folders)
	assert.EqualValues(t, 0, rep.Skipped.Links)
	assert.EqualValues(t, 2, count(t, pool, "link_tag"))
	assert.EqualValues(t, 2, count(t, pool, "click_log"))
}

func TestRestore_DuplicateIntoEmptyDB(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	svc := backup.NewService(pool, newStubBucket(), discardLogger())

	now := time.Now().UTC()
	snap := backup.Snapshot{
		Version: backup.DatabaseSnapshotVersion,
		Tags:    []backup.TagRow{{ID: 1, Name: "solo-tag", Color: "#abc", CreatedAt: now}},
		Folders: []backup.FolderRow{
			{ID: 1, Name: "Root", Color: "#111111", CreatedAt: now},
			{ID: 2, Name: "Child", Color: "#222222", ParentID: int64Ptr(1), CreatedAt: now},
		},
		Links: []backup.LinkRow{{
			ID: 1, URL: "https://dup-empty.example", Title: "Dup Empty",
			Slug: "dup-empty", PreviewStatus: "pending", FolderID: int64Ptr(2),
			CreatedAt: now, UpdatedAt: now,
		}},
		Notes: []backup.NoteRow{{
			ID: 1, Title: "Dup Note", Slug: "dup-note", BodyHTML: "<b>x</b>",
			FolderID: int64Ptr(2), CreatedAt: now, UpdatedAt: now,
		}},
		LinkTags:  []backup.LinkTagRow{{LinkID: 1, TagID: 1}},
		NoteTags:  []backup.NoteTagRow{{NoteID: 1, TagID: 1}},
		ClickLogs: []backup.ClickRow{{LinkID: 1, ClickedAt: now}},
	}
	db := mustJSON(t, snap)
	zr := zipFromEntries(t, map[string][]byte{
		"manifest.json": mustJSON(t, backup.Manifest{
			Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
			Checksums: map[string]string{"database.json": sha256hex(db)},
		}),
		"database.json": db,
	})
	rep, err := svc.Restore(ctx, zr, backup.ModeDuplicate)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Inserted.Links)
	assert.EqualValues(t, 1, rep.Inserted.Notes)
	assert.EqualValues(t, 1, rep.Inserted.Tags)
	assert.EqualValues(t, 2, rep.Inserted.Folders)
	assert.EqualValues(t, 1, count(t, pool, "link"))
	assert.EqualValues(t, 1, count(t, pool, "note"))
}

func TestRestore_InvalidModeAndBadManifest(t *testing.T) {
	pool := testdb.New(t)
	svc := backup.NewService(pool, newStubBucket(), discardLogger())
	ctx := context.Background()

	_, err := svc.Restore(ctx, zipFromEntries(t, map[string][]byte{"x": []byte("y")}), backup.ConflictMode("nope"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid mode")

	_, err = svc.Restore(ctx, zipFromEntries(t, map[string][]byte{
		"manifest.json": mustJSON(t, backup.Manifest{Kind: "other", Version: "1.0", SchemaVersion: 1}),
	}), backup.ModeSkip)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a foldex backup")

	_, err = svc.Restore(ctx, zipFromEntries(t, map[string][]byte{
		"manifest.json": mustJSON(t, backup.Manifest{
			Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion + 99,
		}),
	}), backup.ModeSkip)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too new")

	// Missing database.json after valid manifest
	_, err = svc.Restore(ctx, zipFromEntries(t, map[string][]byte{
		"manifest.json": mustJSON(t, backup.Manifest{
			Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
		}),
	}), backup.ModeSkip)
	require.Error(t, err)
}

func TestRestore_Duplicate_EmptySlugAndRenames(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	svc := backup.NewService(pool, newStubBucket(), discardLogger())

	// Pre-seed colliding tag name so uniqueTagName walks to "(2)".
	_, err := pool.Exec(ctx, `INSERT INTO tag (name, color) VALUES ('dup-tag', '#abc')`)
	require.NoError(t, err)

	now := time.Now().UTC()
	snap := backup.Snapshot{
		Version: backup.DatabaseSnapshotVersion,
		Tags:    []backup.TagRow{{ID: 5, Name: "dup-tag", Color: "#abc", CreatedAt: now}},
		Links: []backup.LinkRow{{
			ID: 11, URL: "https://dup-empty-slug.example", Title: "!!!", // slugify empty → link-restored
			Slug: "", PreviewStatus: "pending", CreatedAt: now, UpdatedAt: now,
		}},
		Notes: []backup.NoteRow{{
			ID: 21, Title: "!!!", Slug: "", BodyHTML: "<p>x</p>",
			CreatedAt: now, UpdatedAt: now,
		}},
		LinkTags:   []backup.LinkTagRow{{LinkID: 11, TagID: 5}},
		NoteTags:   []backup.NoteTagRow{{NoteID: 21, TagID: 5}},
		ClickLogs:  []backup.ClickRow{{LinkID: 11, ClickedAt: now}},
		NoteClicks: []backup.NoteClickRow{{NoteID: 21, ClickedAt: now}},
	}
	db := mustJSON(t, snap)
	zr := zipFromEntries(t, map[string][]byte{
		"manifest.json": mustJSON(t, backup.Manifest{
			Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
			Checksums: map[string]string{"database.json": sha256hex(db)},
		}),
		"database.json": db,
	})
	rep, err := svc.Restore(ctx, zr, backup.ModeDuplicate)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Inserted.Tags)
	assert.True(t, tagNameExists(t, pool, "dup-tag (2)"))
	assert.EqualValues(t, 1, rep.Inserted.Links)
	assert.EqualValues(t, 1, rep.Inserted.Notes)
}

func int64Ptr(v int64) *int64 { return &v }
