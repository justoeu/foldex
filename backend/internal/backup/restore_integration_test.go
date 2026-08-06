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
	"foldex/internal/pkg/httperr"
	"foldex/internal/settings"
	"foldex/internal/tags"
	"foldex/internal/testdb"

	"foldex/internal/pkg/authctx"
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
func seedSnapshot(t *testing.T, pool *pgxpool.Pool, uid authctx.UserID, bucket *stubBucket) seeded {
	t.Helper()
	ctx := context.Background()
	tag, err := tags.NewRepository(pool).Create(ctx, uid, tags.CreateInput{Name: "work", Color: "#abc"})
	require.NoError(t, err)
	folder, err := folders.NewRepository(pool).Create(ctx, uid, folders.CreateInput{Name: "Reading", Color: "#abc"})
	require.NoError(t, err)
	lrepo := links.NewRepository(pool)
	la, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://a.example", Title: "Alpha", TagIDs: []int64{tag.ID}, FolderID: &folder.ID})
	require.NoError(t, err)
	lb, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://b.example", Title: "Beta"})
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err := pool.Exec(ctx, `INSERT INTO click_log (entity_kind, entity_id, clicked_at, user_id) VALUES ('link', $1, now(), $2)`, la.ID, int64(uid))
		require.NoError(t, err)
	}
	// The bucket object and the row that references it are seeded together on
	// purpose: object keys carry no tenant segment, so a row pointing at the key
	// is the ONLY thing that makes it attributable to a user. An orphan object
	// belongs to nobody and no longer travels in that user's export.
	key := fmt.Sprintf("screenshots/%d.jpg", la.ID)
	bucket.objs[key] = []byte("img-A")
	require.NoError(t, lrepo.UpdateOGImage(ctx, uid, la.ID, "/api/files/"+key))
	return seeded{tagID: tag.ID, folderID: folder.ID, linkA: la.ID, linkB: lb.ID}
}

func exportToReader(t *testing.T, svc *backup.Service, uid authctx.UserID) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	_, err := svc.Export(context.Background(), uid, &buf, func(backup.Counts) error { return nil })
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
// later insert can't collide, and the object-store prefix replaced from the zip.
// TestRestore_WipeRestoresContentWithFreshIDs locks the POST-ADR-30 wipe
// contract. Before migration 000017 this test asserted the opposite — that wipe
// preserved the snapshot's original ids and then setval'd the sequences.
//
// Neither is valid multi-tenant: ids are only unique per table, not per user, so
// another tenant may already hold them, and RESTART IDENTITY / setval mutate a
// sequence SHARED by every user. wipeUser now deletes only the caller's rows and
// the mapped insert path re-creates them with fresh ids. What must survive is
// the CONTENT and its relationships — not the integers.
func TestRestore_WipeRestoresContentWithFreshIDs(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())
	ids := seedSnapshot(t, pool, uid, bucket)

	zr := exportToReader(t, svc, uid)
	rep, err := svc.Restore(ctx, uid, zr, backup.ModeWipe)
	require.NoError(t, err)

	assert.EqualValues(t, 2, rep.Wiped.Links)
	assert.EqualValues(t, 1, rep.Wiped.Tags)
	assert.EqualValues(t, 3, rep.Wiped.ClickLogs)
	assert.EqualValues(t, 2, rep.Inserted.Links)

	// Content is whole…
	assert.EqualValues(t, 2, count(t, pool, "link"))
	assert.EqualValues(t, 1, count(t, pool, "tag"))
	assert.EqualValues(t, 3, count(t, pool, "click_log"))

	// …and identified by URL, which is what users actually care about.
	var restoredA int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM link WHERE user_id = $1 AND url = 'https://a.example'`,
		int64(uid)).Scan(&restoredA))

	// The original ids are GONE — that is the behaviour change, asserted rather
	// than left implicit so a future "fix" that reinstates them fails loudly.
	assert.False(t, rowExists(t, pool, "link", ids.linkA),
		"wipe restore must NOT reuse the snapshot's ids (sequences are shared across tenants)")

	// Object keys follow the remapped id, so the proxy URL on the restored row
	// still resolves.
	_, ok := bucket.objs[fmt.Sprintf("screenshots/%d.jpg", restoredA)]
	assert.True(t, ok, "wipe restore must re-upload files under the remapped id")
	assert.EqualValues(t, 1, rep.Files.Uploaded)
}

// TestRestore_SkipLeavesCollisionsAndIsIdempotentForUniqueEntities locks the
// §4 skip contract: URL/name collisions are preserved (ON CONFLICT DO NOTHING),
// never duplicated, and re-running the SAME zip inserts no new unique entities.
func TestRestore_SkipLeavesCollisionsAndIsIdempotentForUniqueEntities(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())
	seedSnapshot(t, pool, uid, bucket)

	require.EqualValues(t, 3, count(t, pool, "click_log"), "precondition: 3 seeded clicks")

	zr := exportToReader(t, svc, uid)
	rep, err := svc.Restore(ctx, uid, zr, backup.ModeSkip)
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
	rep2, err := svc.Restore(ctx, uid, zr, backup.ModeSkip)
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
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())
	seedSnapshot(t, pool, uid, bucket)

	zr := exportToReader(t, svc, uid)
	rep, err := svc.Restore(ctx, uid, zr, backup.ModeDuplicate)
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
	// TWO INDEPENDENT databases, so these get dedicated containers rather than
	// the shared one: the whole test is "export from here, restore into there,
	// and watch the slug collide". On one database the collision would be with
	// itself and the test would prove nothing.
	srcPool := testdb.New(t)
	srcUID := testdb.SeedUser(t, srcPool, "src@test.local", "admin")
	srcBucket := newStubBucket()
	srcSvc := backup.NewService(srcPool, srcBucket, discardLogger())
	seedSnapshot(t, srcPool, srcUID, srcBucket) // link A: url https://a.example, slug "alpha"
	zr := exportToReader(t, srcSvc, srcUID)

	// Target: a pre-existing, different-URL link occupying slug "alpha".
	tgtPool := testdb.New(t)
	tgtUID := testdb.SeedUser(t, tgtPool, "tgt@test.local", "admin")
	occupy := "alpha"
	_, err := links.NewRepository(tgtPool).Create(ctx, tgtUID, links.CreateInput{
		URL: "https://occupied.example", Title: "Occupier", Slug: &occupy,
	})
	require.NoError(t, err)

	tgtSvc := backup.NewService(tgtPool, newStubBucket(), discardLogger())
	_, err = tgtSvc.Restore(ctx, tgtUID, zr, backup.ModeDuplicate)
	require.NoError(t, err)

	// Link A had no URL collision in the target, so it inserts — but slug
	// "alpha" was taken, so uniqueLinkSlug must have produced "alpha-2".
	assert.EqualValues(t, 1, scalar(t, tgtPool, `SELECT count(*) FROM link WHERE slug='alpha-2'`),
		"slug collision under restore must append a -2 suffix")
	assert.EqualValues(t, 1, scalar(t, tgtPool, `SELECT count(*) FROM link WHERE url='https://a.example'`),
		"the duplicated link must be inserted under its original url")
}

func TestRestore_RejectsPathTraversalFileEntry(t *testing.T) {
	pool := testdb.Shared(t)

	tgtUID := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	svc := backup.NewService(pool, newStubBucket(), discardLogger())
	zr := minimalZipWithFile(t, "files/../evil.txt")
	_, err := svc.Restore(context.Background(), tgtUID, zr, backup.ModeSkip)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")
}

// TestRestore_NotesRoundTripWipeMode locks that notes (plus their note_tag
// and note_click rows, both living in the polymorphic link_tag/click_log
// tables) survive an export→wipe→restore cycle with identity preserved —
// the note-specific sibling of TestRestore_WipePreservesIdentityAndBumpsSequence.
func TestRestore_NotesRoundTripWipeMode(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	tag, err := tags.NewRepository(pool).Create(ctx, uid, tags.CreateInput{Name: "pastebin", Color: "#abc"})
	require.NoError(t, err)
	nrepo := notes.NewRepository(pool)
	n, err := nrepo.Create(ctx, uid, notes.CreateInput{Title: "Recipe", BodyHTML: "<p>flour</p>", TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	_, err = nrepo.SystemViewAndResolve(ctx, n.Slug)
	require.NoError(t, err)

	zr := exportToReader(t, svc, uid)
	rep, err := svc.Restore(ctx, uid, zr, backup.ModeWipe)
	require.NoError(t, err)

	assert.EqualValues(t, 1, rep.Wiped.Notes)
	assert.EqualValues(t, 1, rep.Inserted.Notes)
	// Ids are re-minted (ADR-30 — see TestRestore_WipeRestoresContentWithFreshIDs);
	// what must survive is the note and its polymorphic tag/click rows, re-keyed
	// onto the new id.
	var restored int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM note WHERE user_id = $1`, int64(uid)).Scan(&restored))
	assert.EqualValues(t, 1, scalar(t, pool, `SELECT count(*) FROM link_tag WHERE entity_kind='note' AND entity_id=$1`, restored))
	assert.EqualValues(t, 1, scalar(t, pool, `SELECT count(*) FROM click_log WHERE entity_kind='note' AND entity_id=$1`, restored))
}

// TestRestore_NotesRoundTripSkipMode_AlwaysInsertsFreshRow documents the
// deliberate divergence from links' skip semantics: notes have no natural
// content-identity key (unlike link's UNIQUE url), so restoreSkip always
// inserts a fresh note row rather than detecting "already restored" — see
// db.go's restoreSkip comment.
func TestRestore_NotesRoundTripSkipMode_AlwaysInsertsFreshRow(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())
	_, err := notes.NewRepository(pool).Create(ctx, uid, notes.CreateInput{Title: "Idempotency-immune"})
	require.NoError(t, err)

	zr := exportToReader(t, svc, uid)
	rep, err := svc.Restore(ctx, uid, zr, backup.ModeSkip)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Inserted.Notes)
	assert.EqualValues(t, 2, count(t, pool, "note"))

	rep2, err := svc.Restore(ctx, uid, zr, backup.ModeSkip)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep2.Inserted.Notes)
	assert.EqualValues(t, 3, count(t, pool, "note"), "skip mode has no identity key for notes — every restore inserts another row")
}

// TestRestore_FolderPasswordRoundTripWipeMode locks the CLAUDE.md-documented
// contract that a folder's password_hash round-trips VERBATIM through
// backup/restore — it's already a bcrypt hash, restore must copy it as-is
// (never re-hash it, never drop it, never treat it as plaintext).
func TestRestore_FolderPasswordRoundTripWipeMode(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	pw := "correct-horse-battery"
	frepo := folders.NewRepository(pool)
	f, err := frepo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	require.True(t, f.HasPassword)

	zr := exportToReader(t, svc, uid)
	_, err = svc.Restore(ctx, uid, zr, backup.ModeWipe)
	require.NoError(t, err)

	// Ids are re-minted by wipe restore (ADR-30); resolve the folder by name.
	var restoredFolder int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM folder WHERE user_id = $1 AND name = $2`, int64(uid), f.Name).Scan(&restoredFolder))
	got, err := frepo.Get(ctx, uid, restoredFolder)
	require.NoError(t, err)
	assert.True(t, got.HasPassword)
	hash, err := frepo.PasswordHashFor(ctx, uid, restoredFolder)
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
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	pw := "correct-horse-battery"
	frepo := folders.NewRepository(pool)
	_, err := frepo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)

	zr := exportToReader(t, svc, uid)
	rep, err := svc.Restore(ctx, uid, zr, backup.ModeSkip)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Inserted.Folders)
	assert.EqualValues(t, 2, count(t, pool, "folder"), "skip has no identity key for folders — restore inserts a second row")

	list, err := frepo.List(ctx, uid, folders.ListQuery{RootOnly: true})
	require.NoError(t, err)
	require.Len(t, list, 2)
	for _, f := range list {
		assert.True(t, f.Name == "Secret", "both the original and the skip-restored copy must be named Secret")
		hash, err := frepo.PasswordHashFor(ctx, uid, f.ID)
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
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	pw := "correct-horse-battery"
	frepo := folders.NewRepository(pool)
	_, err := frepo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)

	zr := exportToReader(t, svc, uid)
	rep, err := svc.Restore(ctx, uid, zr, backup.ModeDuplicate)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Inserted.Folders)
	assert.EqualValues(t, 2, count(t, pool, "folder"))

	list, err := frepo.List(ctx, uid, folders.ListQuery{RootOnly: true})
	require.NoError(t, err)
	require.Len(t, list, 2)
	for _, f := range list {
		hash, err := frepo.PasswordHashFor(ctx, uid, f.ID)
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
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
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

	rep, err := svc.Restore(context.Background(), uid, zr, backup.ModeWipe)
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
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
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

	rep, err := svc.Restore(context.Background(), uid, zr, backup.ModeWipe)
	require.NoError(t, err, "an old-format backup with no notes key must still restore")
	assert.EqualValues(t, 0, rep.Inserted.Notes)
	assert.True(t, tagNameExists(t, pool, "old-tag"))
}

func TestRestore_RejectsFileEntryOutsideAllowedPrefix(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	svc := backup.NewService(pool, newStubBucket(), discardLogger())
	zr := minimalZipWithFile(t, "files/secret/passwd")
	_, err := svc.Restore(context.Background(), uid, zr, backup.ModeSkip)
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
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
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

	_, err = svc.Restore(ctx, uid, zr, backup.ModeWipe)
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
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	pw := "correct-horse-battery"
	hint := "rhymes with force"
	frepo := folders.NewRepository(pool)
	f, err := frepo.Create(ctx, uid, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw, PasswordHint: &hint})
	require.NoError(t, err)
	require.NotNil(t, f.PasswordHint)

	srepo := settings.NewRepository(pool)
	masterHint := "starts with the-"
	require.NoError(t, srepo.SetMasterPassword(ctx, uid, "the-master-recovery-pass", &masterHint))

	zr := exportToReader(t, svc, uid)
	_, err = svc.Restore(ctx, uid, zr, backup.ModeWipe)
	require.NoError(t, err)

	// The folder id is re-minted by wipe restore, so resolve it by name.
	var restoredFolder int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM folder WHERE user_id = $1 AND name = $2`, int64(uid), f.Name).Scan(&restoredFolder))
	got, err := frepo.Get(ctx, uid, restoredFolder)
	require.NoError(t, err)
	require.NotNil(t, got.PasswordHint)
	assert.Equal(t, hint, *got.PasswordHint, "hint must survive wipe restore verbatim")

	// Since ADR-30 the master password is a per-user column on app_user and is
	// deliberately NOT exported (no auth material ships in the ZIP). It survives
	// a wipe restore because wipeUser only deletes content rows — never because
	// it round-tripped through the backup.
	ok, configured, err := srepo.VerifyMaster(ctx, uid, "the-master-recovery-pass")
	require.NoError(t, err)
	assert.True(t, configured, "master password must be untouched by a wipe restore")
	assert.True(t, ok, "master hash must still verify — wipe restore must not clear it")

	gotHint, err := srepo.MasterPasswordHint(ctx, uid)
	require.NoError(t, err)
	require.NotNil(t, gotHint, "master hint must survive wipe restore")
	assert.Equal(t, masterHint, *gotHint)
}

// TestRestore_AppSettingSkipMode_DoesNotClobberExistingMaster locks the
// ON CONFLICT DO NOTHING branch of restoreAppSettings: skip/duplicate restore
// must PRESERVE this instance's existing master password rather than overwrite
// it with the snapshot's (a singleton setting can't be "duplicated").
func TestRestore_AppSettingSkipMode_DoesNotClobberExistingMaster(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	bucket := newStubBucket()

	// Snapshot instance has master "snapshot-master".
	srcSvc := backup.NewService(pool, bucket, discardLogger())
	srepo := settings.NewRepository(pool)
	require.NoError(t, srepo.SetMasterPassword(ctx, uid, "snapshot-master", nil))
	zr := exportToReader(t, srcSvc, uid)

	// Now change THIS instance's master to something else, then skip-restore.
	require.NoError(t, srepo.SetMasterPassword(ctx, uid, "local-master-wins", nil))
	_, err := srcSvc.Restore(ctx, uid, zr, backup.ModeSkip)
	require.NoError(t, err)

	// The local master must survive — the snapshot's value must NOT clobber it.
	ok, configured, err := srepo.VerifyMaster(ctx, uid, "local-master-wins")
	require.NoError(t, err)
	assert.True(t, configured)
	assert.True(t, ok, "skip restore must not overwrite an existing app_setting")
	ok, _, err = srepo.VerifyMaster(ctx, uid, "snapshot-master")
	require.NoError(t, err)
	assert.False(t, ok, "the snapshot's master must NOT win under skip mode")
}

// TestRestore_AdvisoryLockRejectsConcurrentRestore locks RACE-HER-007: a second
// Restore while another session holds the advisory lock fails with 409
// restore_in_progress (pg_try_advisory_xact_lock) rather than interleaving.
func TestRestore_AdvisoryLockRejectsConcurrentRestore(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()

	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	var got bool
	require.NoError(t, tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, backup.RestoreAdvisoryLockKey).Scan(&got))
	require.True(t, got, "test setup must hold the restore lock")

	svc := backup.NewService(pool, newStubBucket(), discardLogger())
	_, err = svc.Restore(ctx, uid, minimalZipWithFile(t, "files/images/1.jpg"), backup.ModeSkip)
	require.Error(t, err)
	var he *httperr.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 409, he.Status)
	assert.Equal(t, "restore_in_progress", he.Code)
}
