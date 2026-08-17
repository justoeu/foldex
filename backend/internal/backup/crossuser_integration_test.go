//go:build integration

package backup_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backup"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/testdb"
)

// TestMain owns the lifetime of this package's shared Postgres container.
//
// It cannot be a t.Cleanup: os.Exit skips deferred work, and a cleanup hung off
// whichever test ran first would tear the database down while the rest of the
// package still needed it. The Makefile disables testcontainers' reaper, so
// nothing else would collect it.
func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

// The backup suite seeds a SINGLE user everywhere else, which makes
// `DELETE ... WHERE user_id = $1` and `DELETE ... WHERE true` observationally
// identical: with one tenant there is nothing for an over-broad delete to take
// that the correct one would have left. Every test in this file exists to hold a
// SECOND tenant's rows and objects still, so the over-broad version has
// something to destroy and fails.
//
// This was not hypothetical. Reverting wipeUser to the pre-000017
// `TRUNCATE ... CASCADE`, and applyFiles to `DeleteObjectsPrefix`, passed the
// entire 21-test backup suite.

// tenant is one seeded user plus the ids and object keys they own.
type tenant struct {
	uid      authctx.UserID
	linkID   int64
	shotKey  string
	clickIDs []int64
}

// seedTenant gives uid one link, one bucket object claimed by that link's
// og_image_url, and two clicks.
func seedTenant(t *testing.T, pool *pgxpool.Pool, bucket *stubBucket, email, host string) tenant {
	t.Helper()
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, email, "editor")

	lrepo := links.NewRepository(pool)
	l, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://" + host, Title: "Link of " + email})
	require.NoError(t, err)

	key := fmt.Sprintf("screenshots/%d.jpg", l.ID)
	bucket.objs[key] = []byte("bytes-of-" + email)
	require.NoError(t, lrepo.UpdateOGImage(ctx, uid, l.ID, "/api/files/"+key))

	var clickIDs []int64
	for i := 0; i < 2; i++ {
		var id int64
		require.NoError(t, pool.QueryRow(ctx,
			`INSERT INTO click_log (entity_kind, entity_id, user_id) VALUES ('link', $1, $2) RETURNING id`,
			l.ID, int64(uid)).Scan(&id))
		clickIDs = append(clickIDs, id)
	}
	return tenant{uid: uid, linkID: l.ID, shotKey: key, clickIDs: clickIDs}
}

// TestRestore_WipeTouchesOnlyTheCallersRowsAndObjects is the two-tenant lock on
// wipe mode. It asserts the bystander survives in BOTH stores — Postgres and the
// bucket — because the two deletions are separate code paths that failed
// independently: wipeUser once ran TRUNCATE, and applyFiles once called
// DeleteObjectsPrefix on a flat key space.
func TestRestore_WipeTouchesOnlyTheCallersRowsAndObjects(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	// Interleaved so the two tenants' link ids are adjacent. Without that, an
	// assertion could pass because the ids happen to be far apart rather than
	// because the scoping works.
	a := seedTenant(t, pool, bucket, "a@test.local", "a.example")
	b := seedTenant(t, pool, bucket, "b@test.local", "b.example")
	require.InDelta(t, a.linkID, b.linkID, 2,
		"the two tenants must hold adjacent link ids or this test proves nothing")

	zr := exportToReader(t, svc, a.uid)
	rep, err := svc.Restore(ctx, a.uid, zr, backup.ModeWipe)
	require.NoError(t, err)

	// A's own content came back, under a fresh id.
	assert.EqualValues(t, 1, rep.Wiped.Links)
	assert.EqualValues(t, 1, rep.Inserted.Links)
	var restoredA int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM link WHERE user_id = $1 AND url = 'https://a.example'`,
		int64(a.uid)).Scan(&restoredA))
	assert.NotEqual(t, a.linkID, restoredA, "wipe re-mints ids (sequences are shared)")

	// ── The bystander: rows ─────────────────────────────────────────────────
	assert.True(t, rowExists(t, pool, "link", b.linkID),
		"B's link must survive A's wipe restore")
	assert.EqualValues(t, 2, scalar(t, pool,
		`SELECT count(*) FROM click_log WHERE user_id = $1`, int64(b.uid)),
		"B's clicks must survive A's wipe restore")
	for _, id := range b.clickIDs {
		assert.True(t, rowExists(t, pool, "click_log", id),
			"B's click row %d must survive by identity, not just by count", id)
	}

	// ── The bystander: objects ──────────────────────────────────────────────
	// The key that a prefix-delete would have taken. Keys are flat
	// (screenshots/{id}.jpg), so "delete the screenshots/ prefix" reaches every
	// tenant's screenshots.
	_, bStillThere := bucket.objs[b.shotKey]
	assert.True(t, bStillThere,
		"B's object %q must survive A's wipe restore — a prefix delete would have taken it", b.shotKey)
	assert.Equal(t, []byte("bytes-of-b@test.local"), bucket.objs[b.shotKey],
		"B's object must be untouched, not merely present")

	// ── A's own image is not orphaned ───────────────────────────────────────
	// wipe deletes A's old key and re-uploads under the new id; the restored row
	// must point at the key that now exists, or the restore "succeeds" with a
	// broken image on every card.
	var ogURL string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COALESCE(og_image_url, '') FROM link WHERE id = $1`, restoredA).Scan(&ogURL))
	newKey := fmt.Sprintf("screenshots/%d.jpg", restoredA)
	assert.Equal(t, "/api/files/"+newKey, ogURL,
		"restored link must reference the re-keyed object, not the snapshot's old id")
	_, aObjectThere := bucket.objs[newKey]
	assert.True(t, aObjectThere, "the object the restored row points at must exist")
	_, aOldGone := bucket.objs[a.shotKey]
	assert.False(t, aOldGone, "A's pre-wipe object must have been deleted")
}

// TestExport_CarriesOnlyTheCallersObjects locks the export half of the same
// boundary. The bucket is shared and flat, so listing a prefix returns every
// tenant's files; a backup is a file the user downloads and hands around, and
// another tenant's screenshots must not be inside it.
func TestExport_CarriesOnlyTheCallersObjects(t *testing.T) {
	pool := testdb.Shared(t)
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	a := seedTenant(t, pool, bucket, "a@test.local", "a.example")
	b := seedTenant(t, pool, bucket, "b@test.local", "b.example")

	zr := exportToReader(t, svc, a.uid)

	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	assert.True(t, names["files/"+a.shotKey], "A's own object must be in A's export")
	assert.False(t, names["files/"+b.shotKey],
		"B's object must NOT be in A's export — the ZIP is a file A downloads and shares")
}

// TestRestore_CraftedZipCannotOverwriteAnotherTenantsObject is the object-store
// half of the "restore always writes rows owned by the caller" invariant. The
// row half is TestCrossUser_RestoreIgnoresOwnerEmail below; this one covers the
// bucket, where the danger is different:
// user_id is never read from the ZIP, but the object KEY is, and keys are flat.
func TestRestore_CraftedZipCannotOverwriteAnotherTenantsObject(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	victim := seedTenant(t, pool, bucket, "victim@test.local", "victim.example")
	attacker := testdb.SeedUser(t, pool, "attacker@test.local", "editor")

	// A ZIP whose file entry names the VICTIM's object key. Nothing else in the
	// snapshot refers to it — that is the point: the attacker is not restoring
	// their own backup, they are asking for a write at a chosen key.
	zr := minimalZipWithFile(t, "files/"+victim.shotKey)

	_, err := svc.Restore(ctx, attacker, zr, backup.ModeWipe)
	require.NoError(t, err, "the entry is dropped, not treated as a hostile-zip error")

	assert.Equal(t, []byte("bytes-of-victim@test.local"), bucket.objs[victim.shotKey],
		"a ZIP entry naming another tenant's key must be dropped, never written")
}

// TestCrossUser_RestoreIgnoresOwnerEmail is the row half of "restore always
// writes rows owned by the CALLER" (ADR-30). Snapshot.OwnerEmail travels in the
// ZIP and is informational ONLY: a ZIP is a file users download and hand around,
// so if the declared owner selected who the rows landed on, anyone could
// hand-craft a backup that plants content in another account. The mismatch
// produces a warning and nothing else.
func TestCrossUser_RestoreIgnoresOwnerEmail(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	a := seedTenant(t, pool, bucket, "a@test.local", "a.example")
	b := seedTenant(t, pool, bucket, "b@test.local", "b.example")

	// A's export declares OwnerEmail = a@test.local. B restores it.
	zr := exportToReader(t, svc, a.uid)
	rep, err := svc.Restore(ctx, b.uid, zr, backup.ModeSkip)
	require.NoError(t, err)

	assert.NotEmpty(t, rep.Warnings,
		"restoring a ZIP exported by someone else must warn, since every row changes hands")

	// The imported link landed on B, the caller — not on A, the declared owner.
	assert.EqualValues(t, 1, scalar(t, pool,
		`SELECT count(*) FROM link WHERE user_id = $1 AND url = 'https://a.example'`, int64(b.uid)),
		"A's link must have been recreated under B")
	assert.EqualValues(t, 1, scalar(t, pool,
		`SELECT count(*) FROM link WHERE user_id = $1`, int64(a.uid)),
		"A must still hold exactly their original link — the restore adds nothing to A")
	assert.True(t, rowExists(t, pool, "link", a.linkID), "A's own row is untouched")

	// Nothing anywhere ended up owned by a user the caller is not.
	assert.Zero(t, scalar(t, pool,
		`SELECT count(*) FROM link WHERE user_id NOT IN ($1, $2)`, int64(a.uid), int64(b.uid)))
}

// TestRestore_CraftedZipCannotOverwriteANoteImage covers the UUID key family.
// The UUID is public in /n/{slug} markup, so an unreferenced ZIP entry must be
// dropped; legitimate referenced entries are mapped to fresh UUIDs.
func TestRestore_CraftedZipCannotOverwriteANoteImage(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	const victimKey = "notes/3f2504e0-4f89-11d3-9a0c-0305e82c3301.png"
	bucket.objs[victimKey] = []byte("victims-note-image")
	attacker := testdb.SeedUser(t, pool, "attacker@test.local", "editor")

	for _, mode := range []backup.ConflictMode{backup.ModeWipe, backup.ModeDuplicate, backup.ModeSkip} {
		zr := minimalZipWithFile(t, "files/"+victimKey)
		_, err := svc.Restore(ctx, attacker, zr, mode)
		require.NoError(t, err)
		assert.Equal(t, []byte("victims-note-image"), bucket.objs[victimKey],
			"mode %q must not overwrite an existing note-image key", mode)
	}
}

func TestRestore_WipeCraftedNoteReferenceCannotDeleteOrReplaceVictimMedia(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	const victimKey = "notes/8bcb9d80-8212-4ef3-a6a8-24f9471cf90e.jpg"
	bucket.objs[victimKey] = []byte("victim-bytes")
	victim := testdb.SeedUser(t, pool, "victim@test.local", "editor")
	repo := notes.NewRepository(pool)
	require.NoError(t, repo.RegisterMediaLease(ctx, victim, victimKey))
	_, err := repo.Create(ctx, victim, notes.CreateInput{
		Title: "public victim", BodyHTML: `<img src="/api/files/` + victimKey + `">`,
	})
	require.NoError(t, err)
	attacker := testdb.SeedUser(t, pool, "attacker@test.local", "editor")
	_, err = repo.Create(ctx, attacker, notes.CreateInput{
		Title:    "crafted reference",
		BodyHTML: `<img src="/api/files/` + victimKey + `">`,
	})
	require.NoError(t, err)

	_, err = svc.Restore(ctx, attacker, minimalZipWithFile(t, "files/"+victimKey), backup.ModeWipe)
	require.NoError(t, err)
	assert.Equal(t, []byte("victim-bytes"), bucket.objs[victimKey],
		"wipe must derive delete/write authority from persisted ownership, not attacker-authored HTML")
}

func TestRestore_RekeysOwnedNoteMediaAndRewritesReferences(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())
	uid := testdb.SeedUser(t, pool, "owner@test.local", "editor")
	repo := notes.NewRepository(pool)
	const oldKey = "notes/22c3a1e2-304d-441f-a525-713dc364bff1.png"
	bucket.objs[oldKey] = validNotePNG(t)
	require.NoError(t, repo.RegisterMediaLease(ctx, uid, oldKey))
	created, err := repo.Create(ctx, uid, notes.CreateInput{
		Title:    "with media",
		BodyHTML: `<p><img src="/api/files/` + oldKey + `"></p>`,
	})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE note SET cover_url = $1 WHERE user_id = $2 AND id = $3`,
		"/api/files/"+oldKey, int64(uid), created.ID)
	require.NoError(t, err)

	zr := exportToReader(t, svc, uid)
	_, err = svc.Restore(ctx, uid, zr, backup.ModeWipe)
	require.NoError(t, err)

	var body, cover string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT body_html, COALESCE(cover_url, '') FROM note WHERE user_id = $1 AND title = 'with media'`, int64(uid)).Scan(&body, &cover))
	match := regexp.MustCompile(`/api/files/(notes/[a-f0-9-]+\.jpg)`).FindStringSubmatch(body)
	require.Len(t, match, 2, "restored body must contain a local note-media URL")
	newKey := match[1]
	assert.NotEqual(t, oldKey, newKey, "restore must never reuse the snapshot's public UUID key")
	assert.Equal(t, "/api/files/"+newKey, cover)
	assert.Equal(t, "image/jpeg", http.DetectContentType(bucket.objs[newKey]))
	assert.NotContains(t, bucket.objs, oldKey)
	assert.EqualValues(t, 1, scalar(t, pool, `
        SELECT count(*) FROM note_media_ref r
        JOIN note_media m ON m.user_id = r.user_id AND m.object_key = r.object_key
        WHERE r.user_id = $1 AND r.object_key = $2
    `, int64(uid), newKey))
}

func TestRestore_RejectsNoteMediaDecodeBombBeforeWipe(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "decode-bomb@test.local", "editor")
	repo := notes.NewRepository(pool)
	keep, err := repo.Create(ctx, uid, notes.CreateInput{Title: "must survive", BodyHTML: "<p>safe</p>"})
	require.NoError(t, err)

	const key = "notes/22c3a1e2-304d-441f-a525-713dc364bff1.png"
	now := time.Now().UTC()
	snap := backup.Snapshot{
		Version: backup.DatabaseSnapshotVersion,
		Notes: []backup.NoteRow{{
			ID: 1, Title: "hostile", Slug: "hostile",
			BodyHTML:  `<img src="/api/files/` + key + `">`,
			CreatedAt: now, UpdatedAt: now,
		}},
	}
	db := mustJSON(t, snap)
	zr := zipFromEntries(t, map[string][]byte{
		"manifest.json": mustJSON(t, backup.Manifest{
			Kind: backup.ManifestKind, Version: backup.ManifestVersion,
			SchemaVersion: backup.CurrentSchemaVersion,
			Checksums: map[string]string{
				"database.json": sha256hex(db),
				"files/" + key:  sha256hex(pngHeaderWithDimensions(8_000, 8_000)),
			},
		}),
		"database.json": db,
		"files/" + key:  pngHeaderWithDimensions(8_000, 8_000),
	})

	_, err = backup.NewService(pool, newStubBucket(), discardLogger()).Restore(ctx, uid, zr, backup.ModeWipe)
	require.Error(t, err)
	var httpErr *httperr.Error
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusBadRequest, httpErr.Status)
	assert.Equal(t, "invalid_backup", httpErr.Code)
	_, err = repo.Get(ctx, uid, keep.ID)
	require.NoError(t, err, "note-media validation must run before wipe mutates the database")
}

func pngHeaderWithDimensions(width, height uint32) []byte {
	var out bytes.Buffer
	out.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	data := make([]byte, 13)
	binary.BigEndian.PutUint32(data[0:4], width)
	binary.BigEndian.PutUint32(data[4:8], height)
	data[8], data[9] = 8, 2
	binary.Write(&out, binary.BigEndian, uint32(len(data)))
	out.WriteString("IHDR")
	out.Write(data)
	crcInput := append([]byte("IHDR"), data...)
	binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(crcInput))
	return out.Bytes()
}

func validNotePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			img.Set(x, y, color.RGBA{R: 40, G: 100, B: 180, A: 255})
		}
	}
	var out bytes.Buffer
	require.NoError(t, png.Encode(&out, img))
	return out.Bytes()
}

// TestRestore_RealignsImagesPrefixToo guards the second alternative in
// realignLinkImageURLs' pattern. Every other fixture in the suite stores its
// object under screenshots/, so dropping `|images` from the alternation leaves
// manually-uploaded thumbnails broken after a restore while the whole suite
// stays green.
func TestRestore_RealignsImagesPrefixToo(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	uid := testdb.SeedUser(t, pool, "owner@test.local", "editor")
	lrepo := links.NewRepository(pool)
	l, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://uploaded.example", Title: "Uploaded"})
	require.NoError(t, err)
	oldKey := fmt.Sprintf("images/%d.jpg", l.ID)
	bucket.objs[oldKey] = []byte("manual-upload")
	require.NoError(t, lrepo.UpdateOGImage(ctx, uid, l.ID, "/api/files/"+oldKey))

	zr := exportToReader(t, svc, uid)
	_, err = svc.Restore(ctx, uid, zr, backup.ModeWipe)
	require.NoError(t, err)

	var newID int64
	var ogURL string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id, COALESCE(og_image_url,'') FROM link WHERE user_id = $1 AND url = 'https://uploaded.example'`,
		int64(uid)).Scan(&newID, &ogURL))
	assert.Equal(t, fmt.Sprintf("/api/files/images/%d.jpg", newID), ogURL,
		"images/ keys must be realigned exactly like screenshots/")
	_, ok := bucket.objs[fmt.Sprintf("images/%d.jpg", newID)]
	assert.True(t, ok)
}

func TestRestore_PreservesVersionedScreenshotSuffix(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	uid := testdb.SeedUser(t, pool, "versioned-shot@test.local", "editor")
	lrepo := links.NewRepository(pool)
	l, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://capture.example", Title: "Captured"})
	require.NoError(t, err)
	const suffix = ".550e8400-e29b-41d4-a716-446655440000.jpg"
	oldKey := fmt.Sprintf("screenshots/%d%s", l.ID, suffix)
	bucket.objs[oldKey] = []byte("versioned-screenshot")
	require.NoError(t, lrepo.UpdateOGImage(ctx, uid, l.ID, "/api/files/"+oldKey))

	zr := exportToReader(t, svc, uid)
	_, err = svc.Restore(ctx, uid, zr, backup.ModeWipe)
	require.NoError(t, err)

	var newID int64
	var ogURL string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id, COALESCE(og_image_url,'') FROM link WHERE user_id = $1 AND url = 'https://capture.example'`,
		int64(uid)).Scan(&newID, &ogURL))
	newKey := fmt.Sprintf("screenshots/%d%s", newID, suffix)
	assert.Equal(t, "/api/files/"+newKey, ogURL)
	assert.Equal(t, []byte("versioned-screenshot"), bucket.objs[newKey])
}

// TestExport_ReferencingAKeyIsNotOwningIt is the exfiltration lock. Note
// body_html is user-authored and the sanitizer allows <img src> with relative
// URLs by design, and og_image_url is copied verbatim from a bookmarked page's
// <meta property="og:image"> — which that page's author controls. Both are
// therefore attacker-chosen text that userObjectKeys scans, so mere reference
// must never be mistaken for ownership.
func TestExport_ReferencingAKeyIsNotOwningIt(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	victim := seedTenant(t, pool, bucket, "victim@test.local", "victim.example")
	attacker := testdb.SeedUser(t, pool, "attacker@test.local", "editor")

	// Vector 1: a note whose body points at the victim's object.
	_, err := notes.NewRepository(pool).Create(ctx, attacker, notes.CreateInput{
		Title:    "innocent",
		BodyHTML: `<p><img src="/api/files/` + victim.shotKey + `" alt="x"></p>`,
	})
	require.NoError(t, err)

	// Vector 2: an external og:image URL that merely CONTAINS the proxy path.
	// preview.Worker writes this verbatim, so the remote page chooses the value.
	l, err := links.NewRepository(pool).Create(ctx, attacker,
		links.CreateInput{URL: "https://attacker.example", Title: "bait"})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE link SET og_image_url = $2 WHERE id = $1`,
		l.ID, "https://attacker.example/x/api/files/"+victim.shotKey)
	require.NoError(t, err)

	zr := exportToReader(t, svc, attacker)
	for _, f := range zr.File {
		assert.NotEqual(t, "files/"+victim.shotKey, f.Name,
			"referencing another tenant's key must not put their bytes in your ZIP")
	}

	// The same list drives wipe's deletion, so the victim's object must also
	// survive an attacker's wipe restore.
	_, err = svc.Restore(ctx, attacker, exportToReader(t, svc, attacker), backup.ModeWipe)
	require.NoError(t, err)
	assert.Equal(t, []byte("bytes-of-victim@test.local"), bucket.objs[victim.shotKey],
		"referencing a key must not grant the power to DELETE it either")
}
