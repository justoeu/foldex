//go:build integration

package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

// Restore is the most destructive code in the system (TRUNCATE on wipe,
// old→new id re-keying on skip, rename-on-collision on duplicate). These tests
// lock the §4 backup invariants against a real Postgres — Export was already
// covered, Restore was not.

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type seeded struct {
	tagID    int64
	folderID int64
	linkA    int64
	linkB    int64
}

// seedSnapshot populates pool with one tag, one folder, two links (A inside the
// folder and tagged, B at root), three clicks on A, and the matching bucket
// file. Returns the live ids so callers can assert identity preservation.
func seedSnapshot(t *testing.T, pool *pgxpool.Pool, bucket *stubBucket) seeded {
	t.Helper()
	ctx := context.Background()
	tag, err := tags.NewRepository(pool).Create(ctx, tags.CreateInput{Name: "work", Color: "#abc"})
	require.NoError(t, err)
	folder, err := folders.NewRepository(pool).Create(ctx, folders.CreateInput{Name: "Reading", Color: "#abc"})
	require.NoError(t, err)
	lrepo := links.NewRepository(pool)
	la, err := lrepo.Create(ctx, links.CreateInput{URL: "https://a.example", Title: "Alpha", TagIDs: []int64{tag.ID}, FolderID: &folder.ID})
	require.NoError(t, err)
	lb, err := lrepo.Create(ctx, links.CreateInput{URL: "https://b.example", Title: "Beta"})
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err := pool.Exec(ctx, `INSERT INTO click_log (entity_kind, entity_id, clicked_at) VALUES ('link', $1, now())`, la.ID)
		require.NoError(t, err)
	}
	bucket.objs[fmt.Sprintf("screenshots/%d.jpg", la.ID)] = []byte("img-A")
	return seeded{tagID: tag.ID, folderID: folder.ID, linkA: la.ID, linkB: lb.ID}
}

func exportToReader(t *testing.T, svc *backup.Service) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	_, err := svc.Export(context.Background(), &buf, func(backup.Counts) error { return nil })
	require.NoError(t, err)
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	return zr
}

func count(t *testing.T, pool *pgxpool.Pool, table string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func rowExists(t *testing.T, pool *pgxpool.Pool, table string, id int64) bool {
	t.Helper()
	var ok bool
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE id=$1)", id).Scan(&ok))
	return ok
}

func tagNameExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var ok bool
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM tag WHERE name=$1)`, name).Scan(&ok))
	return ok
}

func scalar(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

// TestRestore_WipePreservesIdentityAndBumpsSequence locks the §4 wipe contract:
// TRUNCATE + restore with ORIGINAL ids, sequences bumped past max(id) so a
// later insert can't collide, and the MinIO prefix replaced from the zip.
func TestRestore_WipePreservesIdentityAndBumpsSequence(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())
	ids := seedSnapshot(t, pool, bucket)

	zr := exportToReader(t, svc)
	rep, err := svc.Restore(ctx, zr, backup.ModeWipe)
	require.NoError(t, err)

	assert.EqualValues(t, 2, rep.Wiped.Links)
	assert.EqualValues(t, 1, rep.Wiped.Tags)
	assert.EqualValues(t, 3, rep.Wiped.ClickLogs)
	assert.EqualValues(t, 2, rep.Inserted.Links)

	// Identity preserved: the very same ids exist after wipe+restore.
	assert.True(t, rowExists(t, pool, "link", ids.linkA), "original link id must survive wipe restore")
	assert.True(t, rowExists(t, pool, "tag", ids.tagID), "original tag id must survive wipe restore")
	assert.EqualValues(t, 2, count(t, pool, "link"))
	assert.EqualValues(t, 3, count(t, pool, "click_log"))

	// Sequence bumped: a fresh insert gets an id strictly greater than the
	// largest restored id (no PK collision) — the gotcha wipe restore exists to
	// avoid.
	nl, err := links.NewRepository(pool).Create(ctx, links.CreateInput{URL: "https://new.example", Title: "New"})
	require.NoError(t, err)
	assert.Greater(t, nl.ID, ids.linkB, "sequence must be advanced past the restored ids")

	// Files prefix replaced from the zip.
	_, ok := bucket.objs[fmt.Sprintf("screenshots/%d.jpg", ids.linkA)]
	assert.True(t, ok, "wipe restore must re-upload files from the zip")
	assert.EqualValues(t, 1, rep.Files.Uploaded)
}

// TestRestore_SkipLeavesCollisionsAndIsIdempotentForUniqueEntities locks the
// §4 skip contract: URL/name collisions are preserved (ON CONFLICT DO NOTHING),
// never duplicated, and re-running the SAME zip inserts no new unique entities.
func TestRestore_SkipLeavesCollisionsAndIsIdempotentForUniqueEntities(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())
	seedSnapshot(t, pool, bucket)

	require.EqualValues(t, 3, count(t, pool, "click_log"), "precondition: 3 seeded clicks")

	zr := exportToReader(t, svc)
	rep, err := svc.Restore(ctx, zr, backup.ModeSkip)
	require.NoError(t, err)

	assert.EqualValues(t, 0, rep.Inserted.Links, "colliding URLs must not be inserted under skip")
	assert.EqualValues(t, 2, rep.Skipped.Links)
	assert.EqualValues(t, 0, rep.Inserted.Tags)
	assert.EqualValues(t, 1, rep.Skipped.Tags)
	assert.EqualValues(t, 2, count(t, pool, "link"), "skip must not duplicate links")

	// Re-key check: the snapshot's link_tag must resolve to the SURVIVING link
	// and tag ids (old→new mapping), not create a dangling row. link_tag's PK
	// is (entity_kind, entity_id, tag_id) so the existing pair is kept, not
	// doubled.
	assert.EqualValues(t, 1, count(t, pool, "link_tag"), "link_tag must not be duplicated under skip")
	assert.EqualValues(t, 1, scalar(t, pool,
		`SELECT count(*) FROM link_tag lt JOIN link l ON l.id=lt.entity_id AND lt.entity_kind='link' JOIN tag t ON t.id=lt.tag_id
		 WHERE l.url='https://a.example' AND t.name='work'`),
		"the surviving link must keep its tag after a skip restore")

	// click_log has NO natural unique key, so skip RE-INSERTS every snapshot
	// click against the surviving link id: 3 seeded + 3 restored = 6. This
	// documents that skip is NOT idempotent for click_log (it inflates click
	// counts on re-run) — a known quirk vs the §4 "idempotent by default"
	// wording; see the follow-up note in docs/TASKS.md.
	assert.EqualValues(t, 6, count(t, pool, "click_log"), "skip re-inserts click_logs against the surviving link")

	// Same in-memory zip again — links/tags (the UNIQUE-constrained entities)
	// stay at zero new inserts, but click_logs grow again (6 → 9).
	rep2, err := svc.Restore(ctx, zr, backup.ModeSkip)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rep2.Inserted.Links)
	assert.EqualValues(t, 0, rep2.Inserted.Tags)
	assert.EqualValues(t, 2, count(t, pool, "link"))
	assert.EqualValues(t, 9, count(t, pool, "click_log"), "second skip restore re-inserts the snapshot clicks again")
}

// TestRestore_DuplicateRenamesTagsAndFallsBackOnURLCollision locks the §4
// duplicate contract: tags collide-rename to "nome (2)", folders are always
// new, and a link whose URL already exists falls back to skip + warning (URL is
// UNIQUE so honest duplication is impossible).
func TestRestore_DuplicateRenamesTagsAndFallsBackOnURLCollision(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())
	seedSnapshot(t, pool, bucket)

	zr := exportToReader(t, svc)
	rep, err := svc.Restore(ctx, zr, backup.ModeDuplicate)
	require.NoError(t, err)

	assert.True(t, tagNameExists(t, pool, "work"))
	assert.True(t, tagNameExists(t, pool, "work (2)"), "colliding tag must be renamed under duplicate")
	assert.EqualValues(t, 2, count(t, pool, "tag"))
	assert.EqualValues(t, 2, count(t, pool, "folder"), "folders are always new under duplicate")

	assert.EqualValues(t, 0, rep.Inserted.Links, "URL-colliding links fall back to skip")
	assert.EqualValues(t, 2, count(t, pool, "link"))
	require.NotEmpty(t, rep.Warnings)
	joined := strings.Join(rep.Warnings, "\n")
	assert.Contains(t, joined, "work")
	assert.Contains(t, joined, "já existia")
}

// minimalZipWithFile builds a valid-enough backup zip (Restore reads the
// manifest kind/schema + an empty snapshot, then reaches applyFiles) carrying a
// single crafted files/ entry — the vehicle for the path-rejection tests.
func minimalZipWithFile(t *testing.T, fileEntry string) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	writeJSON := func(name string, v any) {
		w, err := zw.Create(name)
		require.NoError(t, err)
		require.NoError(t, json.NewEncoder(w).Encode(v))
	}
	writeJSON("manifest.json", backup.Manifest{
		Kind:          backup.ManifestKind,
		Version:       backup.ManifestVersion,
		SchemaVersion: backup.CurrentSchemaVersion,
	})
	writeJSON("database.json", backup.Snapshot{Version: backup.DatabaseSnapshotVersion})
	fw, err := zw.Create(fileEntry)
	require.NoError(t, err)
	_, err = fw.Write([]byte("payload"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	return zr
}

// TestRestore_DuplicateAppendsSlugSuffixOnCollision exercises uniqueLinkSlug's
// `-2` suffix branch: restore into a target DB that already owns the slug under
// a DIFFERENT url, so the duplicated link inserts fresh (no URL collision) but
// must dodge the slug UNIQUE constraint.
func TestRestore_DuplicateAppendsSlugSuffixOnCollision(t *testing.T) {
	ctx := context.Background()

	// Source: seed + export.
	srcPool := testdb.New(t)
	srcBucket := newStubBucket()
	srcSvc := backup.NewService(srcPool, srcBucket, discardLogger())
	seedSnapshot(t, srcPool, srcBucket) // link A: url https://a.example, slug "alpha"
	zr := exportToReader(t, srcSvc)

	// Target: a pre-existing, different-URL link occupying slug "alpha".
	tgtPool := testdb.New(t)
	occupy := "alpha"
	_, err := links.NewRepository(tgtPool).Create(ctx, links.CreateInput{
		URL: "https://occupied.example", Title: "Occupier", Slug: &occupy,
	})
	require.NoError(t, err)

	tgtSvc := backup.NewService(tgtPool, newStubBucket(), discardLogger())
	_, err = tgtSvc.Restore(ctx, zr, backup.ModeDuplicate)
	require.NoError(t, err)

	// Link A had no URL collision in the target, so it inserts — but slug
	// "alpha" was taken, so uniqueLinkSlug must have produced "alpha-2".
	assert.EqualValues(t, 1, scalar(t, tgtPool, `SELECT count(*) FROM link WHERE slug='alpha-2'`),
		"slug collision under restore must append a -2 suffix")
	assert.EqualValues(t, 1, scalar(t, tgtPool, `SELECT count(*) FROM link WHERE url='https://a.example'`),
		"the duplicated link must be inserted under its original url")
}

func TestRestore_RejectsPathTraversalFileEntry(t *testing.T) {
	pool := testdb.New(t)
	svc := backup.NewService(pool, newStubBucket(), discardLogger())
	zr := minimalZipWithFile(t, "files/../evil.txt")
	_, err := svc.Restore(context.Background(), zr, backup.ModeSkip)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")
}

// TestRestore_NotesRoundTripWipeMode locks that notes (plus their note_tag
// and note_click rows, both living in the polymorphic link_tag/click_log
// tables) survive an export→wipe→restore cycle with identity preserved —
// the note-specific sibling of TestRestore_WipePreservesIdentityAndBumpsSequence.
func TestRestore_NotesRoundTripWipeMode(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	tag, err := tags.NewRepository(pool).Create(ctx, tags.CreateInput{Name: "pastebin", Color: "#abc"})
	require.NoError(t, err)
	nrepo := notes.NewRepository(pool)
	n, err := nrepo.Create(ctx, notes.CreateInput{Title: "Recipe", BodyHTML: "<p>flour</p>", TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	_, err = nrepo.ViewAndResolve(ctx, n.Slug)
	require.NoError(t, err)

	zr := exportToReader(t, svc)
	rep, err := svc.Restore(ctx, zr, backup.ModeWipe)
	require.NoError(t, err)

	assert.EqualValues(t, 1, rep.Wiped.Notes)
	assert.EqualValues(t, 1, rep.Inserted.Notes)
	assert.True(t, rowExists(t, pool, "note", n.ID), "original note id must survive wipe restore")
	assert.EqualValues(t, 1, scalar(t, pool, `SELECT count(*) FROM link_tag WHERE entity_kind='note' AND entity_id=$1`, n.ID))
	assert.EqualValues(t, 1, scalar(t, pool, `SELECT count(*) FROM click_log WHERE entity_kind='note' AND entity_id=$1`, n.ID))

	// Sequence bumped past the restored note id too.
	n2, err := nrepo.Create(ctx, notes.CreateInput{Title: "After restore"})
	require.NoError(t, err)
	assert.Greater(t, n2.ID, n.ID)
}

// TestRestore_NotesRoundTripSkipMode_AlwaysInsertsFreshRow documents the
// deliberate divergence from links' skip semantics: notes have no natural
// content-identity key (unlike link's UNIQUE url), so restoreSkip always
// inserts a fresh note row rather than detecting "already restored" — see
// db.go's restoreSkip comment.
func TestRestore_NotesRoundTripSkipMode_AlwaysInsertsFreshRow(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())
	_, err := notes.NewRepository(pool).Create(ctx, notes.CreateInput{Title: "Idempotency-immune"})
	require.NoError(t, err)

	zr := exportToReader(t, svc)
	rep, err := svc.Restore(ctx, zr, backup.ModeSkip)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Inserted.Notes)
	assert.EqualValues(t, 2, count(t, pool, "note"))

	rep2, err := svc.Restore(ctx, zr, backup.ModeSkip)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep2.Inserted.Notes)
	assert.EqualValues(t, 3, count(t, pool, "note"), "skip mode has no identity key for notes — every restore inserts another row")
}

// TestRestore_FolderPasswordRoundTripWipeMode locks the CLAUDE.md-documented
// contract that a folder's password_hash round-trips VERBATIM through
// backup/restore — it's already a bcrypt hash, restore must copy it as-is
// (never re-hash it, never drop it, never treat it as plaintext).
func TestRestore_FolderPasswordRoundTripWipeMode(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	pw := "correct-horse-battery"
	frepo := folders.NewRepository(pool)
	f, err := frepo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	require.True(t, f.HasPassword)

	zr := exportToReader(t, svc)
	_, err = svc.Restore(ctx, zr, backup.ModeWipe)
	require.NoError(t, err)

	got, err := frepo.Get(ctx, f.ID)
	require.NoError(t, err, "original folder id must survive wipe restore")
	assert.True(t, got.HasPassword)
	hash, err := frepo.PasswordHashFor(ctx, f.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	assert.True(t, folders.VerifyPassword(*hash, pw), "the restored hash must still verify the ORIGINAL password — restore must never re-hash")
}

// TestRestore_FolderPasswordRoundTripSkipMode documents the same "no
// identity key" divergence as notes (see
// TestRestore_NotesRoundTripSkipMode_AlwaysInsertsFreshRow): folder has no
// unique constraint, so restoreSkip always inserts a fresh row — but that
// fresh row must still carry the original password_hash forward.
func TestRestore_FolderPasswordRoundTripSkipMode(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	pw := "correct-horse-battery"
	frepo := folders.NewRepository(pool)
	_, err := frepo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)

	zr := exportToReader(t, svc)
	rep, err := svc.Restore(ctx, zr, backup.ModeSkip)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Inserted.Folders)
	assert.EqualValues(t, 2, count(t, pool, "folder"), "skip has no identity key for folders — restore inserts a second row")

	list, err := frepo.List(ctx, folders.ListQuery{RootOnly: true})
	require.NoError(t, err)
	require.Len(t, list, 2)
	for _, f := range list {
		assert.True(t, f.Name == "Secret", "both the original and the skip-restored copy must be named Secret")
		hash, err := frepo.PasswordHashFor(ctx, f.ID)
		require.NoError(t, err)
		require.NotNil(t, hash, "the skip-restored copy must carry the password forward, not drop it")
		assert.True(t, folders.VerifyPassword(*hash, pw))
	}
}

// TestRestore_FolderPasswordRoundTripDuplicateMode mirrors the skip-mode
// test for the third restore mode: folders are ALWAYS duplicated as new rows
// (no rename-on-collision the way tags get, since folder.name has no unique
// constraint) — the duplicated copy must still carry password_hash forward.
func TestRestore_FolderPasswordRoundTripDuplicateMode(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	pw := "correct-horse-battery"
	frepo := folders.NewRepository(pool)
	_, err := frepo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)

	zr := exportToReader(t, svc)
	rep, err := svc.Restore(ctx, zr, backup.ModeDuplicate)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Inserted.Folders)
	assert.EqualValues(t, 2, count(t, pool, "folder"))

	list, err := frepo.List(ctx, folders.ListQuery{RootOnly: true})
	require.NoError(t, err)
	require.Len(t, list, 2)
	for _, f := range list {
		hash, err := frepo.PasswordHashFor(ctx, f.ID)
		require.NoError(t, err)
		require.NotNil(t, hash, "the duplicate-restored copy must carry the password forward, not drop it")
		assert.True(t, folders.VerifyPassword(*hash, pw))
	}
}

// TestRestore_SanitizesNoteBodyHTMLFromHostileZip is the regression lock for
// the XSS gap a malicious backup zip could otherwise exploit: restore writes
// note rows straight to SQL (CopyFrom/INSERT), bypassing
// notes.Repository/notes.CreateInput.Normalize entirely, so the database.json
// is a trust boundary in its own right — the same way Snapshot.Sanitize
// already treats tag/folder colors. GET /n/{id-or-slug} renders body_html as
// raw, unescaped template.HTML on the assumption it was sanitized at write
// time; without this guard a crafted backup plants a payload that executes on
// every visitor of that public, unauthenticated route.
func TestRestore_SanitizesNoteBodyHTMLFromHostileZip(t *testing.T) {
	pool := testdb.New(t)
	svc := backup.NewService(pool, newStubBucket(), discardLogger())

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	writeJSON := func(name string, raw string) {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(raw))
		require.NoError(t, err)
	}
	manifestJSON, err := json.Marshal(backup.Manifest{
		Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: backup.CurrentSchemaVersion,
	})
	require.NoError(t, err)
	writeJSON("manifest.json", string(manifestJSON))
	writeJSON("database.json", `{
		"version": 4,
		"tags": [], "folders": [], "links": [], "link_tags": [], "click_logs": [],
		"notes": [{
			"id": 1, "title": "hostile",
			"body_html": "<p>hi</p><script>alert(1)</script><img src=\"x\" onerror=\"alert(2)\">",
			"body_text": "doesn't matter — server re-derives it",
			"pinned": false, "folder_id": null, "cover_url": null,
			"created_at": "2024-01-01T00:00:00Z", "updated_at": "2024-01-01T00:00:00Z"
		}],
		"note_tags": [], "note_clicks": []
	}`)
	require.NoError(t, zw.Close())
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	rep, err := svc.Restore(context.Background(), zr, backup.ModeWipe)
	require.NoError(t, err)
	require.EqualValues(t, 1, rep.Inserted.Notes)

	var bodyHTML string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT body_html FROM note WHERE title = 'hostile'`).Scan(&bodyHTML))
	assert.NotContains(t, bodyHTML, "<script", "restore must sanitize note body_html from the zip")
	assert.NotContains(t, bodyHTML, "onerror", "restore must strip event handler attributes")
	assert.Contains(t, bodyHTML, "<p>hi</p>", "legitimate markup must survive sanitization")
}

// TestRestore_OldFormatBackupWithoutNotesKeyStillRestores is the forward-
// compat guard: a backup produced before migration 000014 (DatabaseSnapshotVersion
// 3, no "notes"/"note_tags"/"note_clicks" keys in database.json) must still
// restore cleanly — the missing fields decode as nil slices and every note
// loop becomes a no-op.
func TestRestore_OldFormatBackupWithoutNotesKeyStillRestores(t *testing.T) {
	pool := testdb.New(t)
	svc := backup.NewService(pool, newStubBucket(), discardLogger())

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	writeJSON := func(name string, raw string) {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(raw))
		require.NoError(t, err)
	}
	manifestJSON, err := json.Marshal(backup.Manifest{
		Kind: backup.ManifestKind, Version: backup.ManifestVersion, SchemaVersion: 8,
	})
	require.NoError(t, err)
	writeJSON("manifest.json", string(manifestJSON))
	// Pre-000014 shape: version 3, no notes/note_tags/note_clicks keys at all.
	writeJSON("database.json", `{
		"version": 3,
		"tags": [{"id": 1, "name": "old-tag", "color": "#abc", "created_at": "2024-01-01T00:00:00Z"}],
		"folders": [],
		"links": [],
		"link_tags": [],
		"click_logs": []
	}`)
	require.NoError(t, zw.Close())
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	rep, err := svc.Restore(context.Background(), zr, backup.ModeWipe)
	require.NoError(t, err, "an old-format backup with no notes key must still restore")
	assert.EqualValues(t, 0, rep.Inserted.Notes)
	assert.True(t, tagNameExists(t, pool, "old-tag"))
}

func TestRestore_RejectsFileEntryOutsideAllowedPrefix(t *testing.T) {
	pool := testdb.New(t)
	svc := backup.NewService(pool, newStubBucket(), discardLogger())
	zr := minimalZipWithFile(t, "files/secret/passwd")
	_, err := svc.Restore(context.Background(), zr, backup.ModeSkip)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not under")
}

// TestRestore_CoercesTrackingPixelColors is the end-to-end guard for the
// cssvalid trust boundary on the backup zip path. A snapshot carrying
// `red url("https://evil/exfil")` as a tag/folder color would render as a
// tracking pixel on every chip (CLAUDE.md §4). Sanitize runs at zip-load
// time (readSnapshotFromZip), so by the time any restore mode writes rows
// the value must already be the indigo default. Verified against wipe mode
// (the most direct path — every row comes from the snapshot).
func TestRestore_CoercesTrackingPixelColors(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	svc := backup.NewService(pool, newStubBucket(), discardLogger())

	// Craft a minimal zip whose snapshot has one tag and one folder, both
	// with the tracking-pixel color. Restore must NOT write that value.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	writeJSON := func(name string, v any) {
		w, err := zw.Create(name)
		require.NoError(t, err)
		require.NoError(t, json.NewEncoder(w).Encode(v))
	}
	writeJSON("manifest.json", backup.Manifest{
		Kind:          backup.ManifestKind,
		Version:       backup.ManifestVersion,
		SchemaVersion: backup.CurrentSchemaVersion,
	})
	malicious := `red url("https://evil/exfil")`
	writeJSON("database.json", backup.Snapshot{
		Version: backup.DatabaseSnapshotVersion,
		Tags:    []backup.TagRow{{ID: 1, Name: "evil-tag", Color: malicious}},
		Folders: []backup.FolderRow{{ID: 1, Name: "evil-folder", Color: malicious}},
	})
	require.NoError(t, zw.Close())
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	_, err = svc.Restore(ctx, zr, backup.ModeWipe)
	require.NoError(t, err)

	var tagColor, folderColor string
	require.NoError(t, pool.QueryRow(ctx, `SELECT color FROM tag WHERE name='evil-tag'`).Scan(&tagColor))
	require.NoError(t, pool.QueryRow(ctx, `SELECT color FROM folder WHERE name='evil-folder'`).Scan(&folderColor))

	assert.Equal(t, "#6366F1", tagColor, "tracking-pixel tag color MUST be coerced to indigo default")
	assert.Equal(t, "#6366F1", folderColor, "tracking-pixel folder color MUST be coerced to indigo default")
	assert.NotContains(t, tagColor, "evil", "no part of the malicious payload may survive")
	assert.NotContains(t, folderColor, "evil", "no part of the malicious payload may survive")
}

// TestRestore_HintAndMasterPasswordRoundTripWipeMode locks the ADR-29
// additions to the backup snapshot: a folder's password_hint and the
// app_setting master-password hash both round-trip verbatim through a wipe
// restore (hint shown as-is, master hash never re-hashed).
func TestRestore_HintAndMasterPasswordRoundTripWipeMode(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	pw := "correct-horse-battery"
	hint := "rhymes with force"
	frepo := folders.NewRepository(pool)
	f, err := frepo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw, PasswordHint: &hint})
	require.NoError(t, err)
	require.NotNil(t, f.PasswordHint)

	srepo := settings.NewRepository(pool)
	masterHint := "starts with the-"
	require.NoError(t, srepo.SetMasterPassword(ctx, "the-master-recovery-pass", &masterHint))

	zr := exportToReader(t, svc)
	_, err = svc.Restore(ctx, zr, backup.ModeWipe)
	require.NoError(t, err)

	got, err := frepo.Get(ctx, f.ID)
	require.NoError(t, err)
	require.NotNil(t, got.PasswordHint)
	assert.Equal(t, hint, *got.PasswordHint, "hint must survive wipe restore verbatim")

	ok, configured, err := srepo.VerifyMaster(ctx, "the-master-recovery-pass")
	require.NoError(t, err)
	assert.True(t, configured, "master password must survive wipe restore")
	assert.True(t, ok, "restored master hash must still verify the original password — never re-hashed")

	gotHint, err := srepo.MasterPasswordHint(ctx)
	require.NoError(t, err)
	require.NotNil(t, gotHint, "master hint must survive wipe restore")
	assert.Equal(t, masterHint, *gotHint)
}

// TestRestore_AppSettingSkipMode_DoesNotClobberExistingMaster locks the
// ON CONFLICT DO NOTHING branch of restoreAppSettings: skip/duplicate restore
// must PRESERVE this instance's existing master password rather than overwrite
// it with the snapshot's (a singleton setting can't be "duplicated").
func TestRestore_AppSettingSkipMode_DoesNotClobberExistingMaster(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	bucket := newStubBucket()

	// Snapshot instance has master "snapshot-master".
	srcSvc := backup.NewService(pool, bucket, discardLogger())
	srepo := settings.NewRepository(pool)
	require.NoError(t, srepo.SetMasterPassword(ctx, "snapshot-master", nil))
	zr := exportToReader(t, srcSvc)

	// Now change THIS instance's master to something else, then skip-restore.
	require.NoError(t, srepo.SetMasterPassword(ctx, "local-master-wins", nil))
	_, err := srcSvc.Restore(ctx, zr, backup.ModeSkip)
	require.NoError(t, err)

	// The local master must survive — the snapshot's value must NOT clobber it.
	ok, configured, err := srepo.VerifyMaster(ctx, "local-master-wins")
	require.NoError(t, err)
	assert.True(t, configured)
	assert.True(t, ok, "skip restore must not overwrite an existing app_setting")
	ok, _, err = srepo.VerifyMaster(ctx, "snapshot-master")
	require.NoError(t, err)
	assert.False(t, ok, "the snapshot's master must NOT win under skip mode")
}
