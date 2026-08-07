//go:build integration

package backup_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backup"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/pkg/authctx"
	"foldex/internal/testdb"
	"os"
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
	uid := testdb.SeedUser(t, pool, email, "user")

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
	attacker := testdb.SeedUser(t, pool, "attacker@test.local", "user")

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

// TestRestore_CraftedZipCannotOverwriteANoteImage covers the key family that
// CANNOT be re-keyed. notes/{uuid} encodes no row id, so the "drop what this
// restore did not produce" rule has nothing to match on — and the UUID, while
// unguessable, is NOT secret: it is written into the body_html that the public
// /n/{slug} page serves to anyone. Harvest it from the markup, put it in a ZIP,
// restore, and the victim's image is replaced for every viewer.
func TestRestore_CraftedZipCannotOverwriteANoteImage(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	bucket := newStubBucket()
	svc := backup.NewService(pool, bucket, discardLogger())

	const victimKey = "notes/3f2504e0-4f89-11d3-9a0c-0305e82c3301.png"
	bucket.objs[victimKey] = []byte("victims-note-image")
	attacker := testdb.SeedUser(t, pool, "attacker@test.local", "user")

	for _, mode := range []backup.ConflictMode{backup.ModeWipe, backup.ModeDuplicate, backup.ModeSkip} {
		zr := minimalZipWithFile(t, "files/"+victimKey)
		_, err := svc.Restore(ctx, attacker, zr, mode)
		require.NoError(t, err)
		assert.Equal(t, []byte("victims-note-image"), bucket.objs[victimKey],
			"mode %q must not overwrite an existing note-image key", mode)
	}
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

	uid := testdb.SeedUser(t, pool, "owner@test.local", "user")
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
	attacker := testdb.SeedUser(t, pool, "attacker@test.local", "user")

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
