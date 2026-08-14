//go:build integration

// Package security_test holds the cross-tenant isolation suite.
//
// It lives outside every domain package on purpose: these tests exercise the
// real repositories through their public API, from a package that owns none of
// them, so nothing here can accidentally lean on an unexported helper that
// happens to be scoped correctly. This is the multi-tenant analogue of
// internal/notes' TestCrossContamination_LinkAndNoteRowsDoNotLeak, and it
// borrows that test's key trick: the fixture asserts the id collision
// EXPLICITLY, so a passing run can never mean "the ids never overlapped anyway".
package security_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/entries"
	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/stats"
	"foldex/internal/tags"
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

type tenant struct {
	uid    authctx.UserID
	link   links.Link
	note   notes.Note
	folder folders.Folder
	tag    tags.Tag
}

type fixture struct {
	pool  *pgxpool.Pool
	a, b  tenant
	lrepo *links.Repository
	nrepo *notes.Repository
	frepo *folders.Repository
	trepo *tags.Repository
	erepo *entries.Repository
	srepo *stats.Repository
}

// setup creates two tenants with INTERLEAVED inserts, so A's and B's row ids are
// adjacent in every table. Without that, a scoping bug could pass simply because
// the ids never collided.
func setup(t *testing.T) (context.Context, fixture) {
	t.Helper()
	ctx := context.Background()
	pool := testdb.Shared(t)

	f := fixture{
		pool:  pool,
		lrepo: links.NewRepository(pool),
		nrepo: notes.NewRepository(pool),
		frepo: folders.NewRepository(pool),
		trepo: tags.NewRepository(pool),
		erepo: entries.NewRepository(pool),
		srepo: stats.NewRepository(pool),
	}
	f.a.uid = testdb.SeedUser(t, pool, "alice@test.local", "user")
	f.b.uid = testdb.SeedUser(t, pool, "bob@test.local", "user")
	require.NotEqual(t, f.a.uid, f.b.uid)

	mk := func(tn *tenant, label string) {
		var err error
		tn.tag, err = f.trepo.Create(ctx, tn.uid, tags.CreateInput{Name: label + "-tag", Color: "#abc"})
		require.NoError(t, err)
		tn.folder, err = f.frepo.Create(ctx, tn.uid, folders.CreateInput{Name: label + "-folder", Color: "#abc"})
		require.NoError(t, err)
		tn.link, err = f.lrepo.Create(ctx, tn.uid, links.CreateInput{
			URL: "https://" + label + ".example", Title: label + " link",
			FolderID: &tn.folder.ID, TagIDs: []int64{tn.tag.ID},
		})
		require.NoError(t, err)
		tn.note, err = f.nrepo.Create(ctx, tn.uid, notes.CreateInput{
			Title: label + " note", BodyHTML: "<p>" + label + " secret body</p>",
		})
		require.NoError(t, err)
	}
	mk(&f.a, "alpha")
	mk(&f.b, "bravo")

	// The whole suite is only meaningful if the two tenants' ids are close
	// enough to be confusable. Adjacent is what interleaving buys us.
	require.InDelta(t, float64(f.a.link.ID), float64(f.b.link.ID), 1,
		"fixture must produce adjacent link ids or the isolation assertions prove nothing")
	require.InDelta(t, float64(f.a.note.ID), float64(f.b.note.ID), 1,
		"fixture must produce adjacent note ids")
	return ctx, f
}

// ── Reads ────────────────────────────────────────────────────────────────

func TestCrossUser_ListsReturnOnlyOwnRows(t *testing.T) {
	ctx, f := setup(t)

	gotLinks, err := f.lrepo.List(ctx, f.a.uid, links.ListQuery{})
	require.NoError(t, err)
	require.Len(t, gotLinks, 1)
	assert.Equal(t, f.a.link.ID, gotLinks[0].ID)

	gotNotes, err := f.nrepo.List(ctx, f.a.uid, notes.ListQuery{})
	require.NoError(t, err)
	require.Len(t, gotNotes, 1)
	assert.Equal(t, f.a.note.ID, gotNotes[0].ID)

	gotFolders, err := f.frepo.List(ctx, f.a.uid, folders.ListQuery{})
	require.NoError(t, err)
	require.Len(t, gotFolders, 1)
	assert.Equal(t, f.a.folder.ID, gotFolders[0].ID)

	gotTags, err := f.trepo.List(ctx, f.a.uid)
	require.NoError(t, err)
	require.Len(t, gotTags, 1)
	assert.Equal(t, f.a.tag.ID, gotTags[0].ID)

	// entries is the UNION ALL projection that feeds the grid — the one place
	// where a placeholder-index mistake could silently drop the tenant filter
	// from one arm only.
	gotEntries, err := f.erepo.List(ctx, f.a.uid, entries.ListQuery{})
	require.NoError(t, err)
	require.Len(t, gotEntries, 2, "exactly A's one link + one note")
	for _, e := range gotEntries {
		assert.NotEqual(t, "bravo link", e.Title)
		assert.NotEqual(t, "bravo note", e.Title)
	}
}

// A link that belongs to someone else must be reported as NOT FOUND, never as
// forbidden: a 403 confirms the id exists, which turns a dense BIGSERIAL space
// into an enumeration oracle over other tenants' content.
func TestCrossUser_GetOfAnotherUsersRowIsNotFound(t *testing.T) {
	ctx, f := setup(t)

	_, err := f.lrepo.Get(ctx, f.a.uid, f.b.link.ID)
	assert.ErrorIs(t, err, domainerr.ErrNotFound)

	_, err = f.nrepo.Get(ctx, f.a.uid, f.b.note.ID)
	assert.ErrorIs(t, err, domainerr.ErrNotFound)

	_, err = f.frepo.Get(ctx, f.a.uid, f.b.folder.ID)
	assert.ErrorIs(t, err, domainerr.ErrNotFound)

	_, err = f.trepo.Get(ctx, f.a.uid, f.b.tag.ID)
	assert.ErrorIs(t, err, domainerr.ErrNotFound)
}

func TestCrossUser_SearchNeverMatchesAnotherUsersContent(t *testing.T) {
	ctx, f := setup(t)

	// "bravo" appears in B's link title and note body only.
	gotLinks, err := f.lrepo.List(ctx, f.a.uid, links.ListQuery{Q: "bravo"})
	require.NoError(t, err)
	assert.Empty(t, gotLinks)

	gotNotes, err := f.nrepo.List(ctx, f.a.uid, notes.ListQuery{Q: "secret"})
	require.NoError(t, err)
	require.Len(t, gotNotes, 1, "A's own note still matches")
	assert.Equal(t, f.a.note.ID, gotNotes[0].ID)

	gotEntries, err := f.erepo.List(ctx, f.a.uid, entries.ListQuery{Q: "bravo"})
	require.NoError(t, err)
	assert.Empty(t, gotEntries)
}

// ── Writes ───────────────────────────────────────────────────────────────

func TestCrossUser_UpdateAndDeleteOfAnotherUsersRowIsNotFoundAndMutatesNothing(t *testing.T) {
	ctx, f := setup(t)
	hijack := "hijacked"

	_, err := f.lrepo.Update(ctx, f.a.uid, f.b.link.ID, links.UpdateInput{Title: &hijack})
	assert.ErrorIs(t, err, domainerr.ErrNotFound)

	assert.ErrorIs(t, f.lrepo.Delete(ctx, f.a.uid, f.b.link.ID), domainerr.ErrNotFound)
	assert.ErrorIs(t, f.nrepo.Delete(ctx, f.a.uid, f.b.note.ID, nil), domainerr.ErrNotFound)
	assert.ErrorIs(t, f.frepo.Delete(ctx, f.a.uid, f.b.folder.ID, nil, ""), domainerr.ErrNotFound)
	assert.ErrorIs(t, f.trepo.Delete(ctx, f.a.uid, f.b.tag.ID), domainerr.ErrNotFound)

	// B's rows are all still there, unmodified.
	stillLink, err := f.lrepo.Get(ctx, f.b.uid, f.b.link.ID)
	require.NoError(t, err)
	assert.Equal(t, "bravo link", stillLink.Title)
	_, err = f.nrepo.Get(ctx, f.b.uid, f.b.note.ID)
	require.NoError(t, err)
	_, err = f.frepo.Get(ctx, f.b.uid, f.b.folder.ID)
	require.NoError(t, err)
	_, err = f.trepo.Get(ctx, f.b.uid, f.b.tag.ID)
	require.NoError(t, err)
}

// link_tag lost its FK to link(id) in migration 000014, and tag_id's FK carries
// no user_id to compose with — so migration 000017's composite-FK net does NOT
// cover tag attachment. tags.SetEntityTags validates ownership itself; this is
// the test that keeps it honest.
func TestCrossUser_CannotAttachAnotherUsersTag(t *testing.T) {
	ctx, f := setup(t)

	foreign := []int64{f.b.tag.ID}
	_, err := f.lrepo.Update(ctx, f.a.uid, f.a.link.ID, links.UpdateInput{TagIDs: &foreign})
	require.Error(t, err, "attaching another tenant's tag must be rejected")

	// And nothing was written: A's link still carries only A's tag.
	got, err := f.lrepo.Get(ctx, f.a.uid, f.a.link.ID)
	require.NoError(t, err)
	require.Len(t, got.Tags, 1)
	assert.Equal(t, f.a.tag.ID, got.Tags[0].ID)
}

// Proves folder_parent_same_user_fkey / link_folder_same_user_fkey actually
// fire. These are the database-level net that catches a repository which loses
// its scope predicate.
func TestCrossUser_CannotPointAtAnotherUsersFolder(t *testing.T) {
	ctx, f := setup(t)

	foreign := f.b.folder.ID
	_, err := f.lrepo.Update(ctx, f.a.uid, f.a.link.ID,
		links.UpdateInput{FolderID: &foreign, FolderIDSet: true})
	assert.Error(t, err, "link must not be movable into another tenant's folder")

	_, err = f.nrepo.Update(ctx, f.a.uid, f.a.note.ID,
		notes.UpdateInput{FolderID: &foreign, FolderIDSet: true})
	assert.Error(t, err, "note must not be movable into another tenant's folder")

	_, err = f.frepo.Update(ctx, f.a.uid, f.a.folder.ID,
		folders.UpdateInput{ParentID: &foreign, ParentIDSet: true})
	assert.Error(t, err, "folder must not be nestable under another tenant's folder")
}

func TestCrossUser_DatabaseRejectsForeignNoteMediaReference(t *testing.T) {
	ctx, f := setup(t)
	const key = "notes/5b24d8ec-2f5b-47a7-aa97-52f26c93b250.jpg"

	_, err := f.pool.Exec(ctx, `
        INSERT INTO note_media (user_id, object_key, lease_expires_at)
        VALUES ($1, $2, now() + interval '24 hours')
    `, int64(f.b.uid), key)
	require.NoError(t, err, "fixture must create media owned by B")

	_, err = f.pool.Exec(ctx, `
        INSERT INTO note_media_ref (user_id, note_id, object_key)
        VALUES ($1, $2, $3)
    `, int64(f.a.uid), f.a.note.ID, key)
	assert.Error(t, err,
		"the database must reject a ref combining A's note with B's media key")
}

// ── Uniqueness contracts (migration 000017 §8) ───────────────────────────

func TestCrossUser_SameURLAndTagNameAreAllowedForDifferentUsers(t *testing.T) {
	ctx, f := setup(t)

	_, err := f.lrepo.Create(ctx, f.b.uid, links.CreateInput{
		URL: "https://alpha.example", Title: "same url, other tenant",
	})
	require.NoError(t, err, "link.url is UNIQUE (user_id, url) — two tenants may save the same URL")

	_, err = f.trepo.Create(ctx, f.b.uid, tags.CreateInput{Name: "alpha-tag", Color: "#abc"})
	require.NoError(t, err, "tag.name is UNIQUE (user_id, name)")
}

// The inverse of the above, and the reason it is deliberate: /go/{slug} and
// /n/{slug} resolve with NO session, so the slug namespace has no tenant to
// disambiguate by and must stay globally unique.
func TestCrossUser_SlugStaysGloballyUnique(t *testing.T) {
	ctx, f := setup(t)

	taken := f.a.link.Slug
	created, err := f.lrepo.Create(ctx, f.b.uid, links.CreateInput{
		URL: "https://bravo-2.example", Title: "collide", Slug: &taken,
	})
	require.Error(t, err, "an explicitly requested slug already taken by another tenant must 409")
	assert.Equal(t, links.Link{}, created)

	// Auto-derived slugs dodge the collision with the -2 suffix instead.
	auto, err := f.lrepo.Create(ctx, f.b.uid, links.CreateInput{
		URL: "https://bravo-3.example", Title: "alpha link",
	})
	require.NoError(t, err)
	assert.NotEqual(t, f.a.link.Slug, auto.Slug)
	assert.Equal(t, f.a.link.Slug+"-2", auto.Slug)
}

// ── Derived data ─────────────────────────────────────────────────────────

// Every stats aggregate has to reach the owner, and each one does it its own
// way — some through a semi-join on link, some through click_log.user_id since
// migration 000018. That per-query independence is why this test walks ALL of
// them: Summary's top-host clause once shipped with no owner predicate at all
// while the rest of Summary was correctly scoped, and the suite stayed green
// because nothing asserted on that particular field.
func TestCrossUser_StatsExcludeAnotherUsersClicks(t *testing.T) {
	ctx, f := setup(t)

	// Three public clicks on B's link, none on A's.
	for range 3 {
		_, err := f.lrepo.ClickAndResolve(ctx, f.b.link.ID)
		require.NoError(t, err)
	}

	sumA, err := f.srepo.Summary(ctx, f.a.uid)
	require.NoError(t, err)
	assert.EqualValues(t, 0, sumA.TotalClicks, "A must not see B's clicks")
	assert.EqualValues(t, 1, sumA.TotalLinks)
	assert.EqualValues(t, 1, sumA.TotalTags)

	// Top host is a SEPARATE query from the scalars above, and it is the one
	// that shipped unscoped: it reads FROM click_log and reaches link only
	// through a JOIN, so both the static detector and the assertions missed it.
	assert.Empty(t, sumA.TopHost, "A has no clicks, so A has no top host — B's must not surface")
	assert.EqualValues(t, 0, sumA.TopHostClicks)

	sumB, err := f.srepo.Summary(ctx, f.b.uid)
	require.NoError(t, err)
	assert.EqualValues(t, 3, sumB.TotalClicks)
	assert.Equal(t, "bravo.example", sumB.TopHost)
	assert.EqualValues(t, 3, sumB.TopHostClicks)

	// Daily is its own query with its own owner predicate, and it was the last
	// aggregate with no cross-tenant coverage — a mutation dropping its
	// predicate passed every test in the tree.
	dailyA, err := f.srepo.Daily(ctx, f.a.uid, 7)
	require.NoError(t, err)
	require.Len(t, dailyA, 7)
	var totalA int64
	for _, p := range dailyA {
		totalA += p.Clicks
	}
	assert.EqualValues(t, 0, totalA, "A's daily buckets must not count B's clicks")

	dailyB, err := f.srepo.Daily(ctx, f.b.uid, 7)
	require.NoError(t, err)
	var totalB int64
	for _, p := range dailyB {
		totalB += p.Clicks
	}
	assert.EqualValues(t, 3, totalB, "B's own clicks must still be counted")

	topA, err := f.srepo.TopLinks(ctx, f.a.uid, 10)
	require.NoError(t, err)
	require.Len(t, topA, 1)
	assert.Equal(t, f.a.link.ID, topA[0].ID)

	bucketsA, err := f.srepo.TagBuckets(ctx, f.a.uid)
	require.NoError(t, err)
	require.Len(t, bucketsA, 1)
	assert.Equal(t, f.a.tag.ID, bucketsA[0].ID)
	assert.EqualValues(t, 0, bucketsA[0].Clicks)
}

// The public routes are tenant-blind by design — that is why slugs stay global.
func TestCrossUser_PublicRoutesResolveWithoutASession(t *testing.T) {
	ctx, f := setup(t)

	dest, err := f.lrepo.ClickAndResolve(ctx, f.a.link.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://alpha.example", dest)

	dest, err = f.lrepo.ClickAndResolveBySlug(ctx, f.b.link.Slug)
	require.NoError(t, err)
	assert.Equal(t, "https://bravo.example", dest)

	n, err := f.nrepo.SystemViewAndResolve(ctx, f.b.note.Slug)
	require.NoError(t, err)
	assert.Equal(t, f.b.note.ID, n.ID)
}

// TestClickLogOwnerMatchesEntityOwner is the drift guard migration 000018 names
// in its header. Denormalizing user_id onto click_log created a SECOND source of
// truth for ownership — entity_kind/entity_id still say WHICH row was clicked,
// user_id now says WHOSE — and two sources of truth can disagree. Nothing in the
// schema prevents it: click_log lost its FK to link in migration 000014, so the
// database cannot check that click_log.user_id equals link.user_id.
//
// Every writer sets the column from the row it just resolved, so the invariant
// holds by construction today. This test is what makes a future writer that
// forgets fail loudly instead of quietly attributing one tenant's clicks to
// another.
func TestClickLogOwnerMatchesEntityOwner(t *testing.T) {
	ctx, f := setup(t)

	// Exercise every production writer of click_log: the public link route, the
	// public note route, both for both tenants.
	for _, tn := range []tenant{f.a, f.b} {
		_, err := f.lrepo.ClickAndResolve(ctx, tn.link.ID)
		require.NoError(t, err)
		_, err = f.lrepo.ClickAndResolveBySlug(ctx, tn.link.Slug)
		require.NoError(t, err)
		_, err = f.nrepo.SystemViewAndResolve(ctx, tn.note.Slug)
		require.NoError(t, err)
	}

	var mismatches int64
	require.NoError(t, f.pool.QueryRow(ctx, `
        SELECT count(*) FROM click_log c
        LEFT JOIN link l ON c.entity_kind = 'link' AND l.id = c.entity_id
        LEFT JOIN note n ON c.entity_kind = 'note' AND n.id = c.entity_id
        WHERE c.user_id IS DISTINCT FROM COALESCE(l.user_id, n.user_id)
    `).Scan(&mismatches))
	assert.Zero(t, mismatches,
		"every click_log row must be attributed to the owner of the entity it references")

	// And the counts landed where they belong, not merely consistently.
	assert.EqualValues(t, 3, scalarOf(t, f.pool, `SELECT count(*) FROM click_log WHERE user_id = $1`, int64(f.a.uid)))
	assert.EqualValues(t, 3, scalarOf(t, f.pool, `SELECT count(*) FROM click_log WHERE user_id = $1`, int64(f.b.uid)))
}

func scalarOf(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}
