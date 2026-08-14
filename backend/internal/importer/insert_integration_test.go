//go:build integration

package importer

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

func TestStagedImport_WipeLateRelationFailureRollsBackPriorState(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	uid := testdb.SeedUser(t, pool, "import-rollback@test.local", "admin")
	lrepo := links.NewRepository(pool)
	trepo := tags.NewRepository(pool)

	firstTag, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "first prior tag", Color: "#111"})
	require.NoError(t, err)
	secondTag, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "second prior tag", Color: "#222"})
	require.NoError(t, err)
	faultTag, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "reject staged relation", Color: "#333"})
	require.NoError(t, err)

	first, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://rollback-first.example", Title: "First prior link", TagIDs: []int64{firstTag.ID},
	})
	require.NoError(t, err)
	second, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://rollback-second.example", Title: "Second prior link", TagIDs: []int64{secondTag.ID},
	})
	require.NoError(t, err)
	for range 2 {
		_, err = lrepo.ClickAndResolve(ctx, first.ID)
		require.NoError(t, err)
	}
	_, err = lrepo.ClickAndResolve(ctx, second.ID)
	require.NoError(t, err)

	// Existing relations satisfy this test-only constraint; the staged fault tag
	// fails only in the final relation insert, after wipe and entity insertion.
	_, err = pool.Exec(ctx, fmt.Sprintf(`
		ALTER TABLE link_tag ADD CONSTRAINT test_reject_staged_import_relation
		CHECK (tag_id <> %d)
	`, faultTag.ID))
	require.NoError(t, err)

	h := NewHandler(pool, nil)
	_, _, _, _, err = h.importItemsWithMode(ctx, uid, []Item{
		{URL: first.URL, Title: "First replacement", Tags: []string{faultTag.Name}, ClickCount: 4},
		{URL: second.URL, Title: "Second replacement", Tags: []string{secondTag.Name}, ClickCount: 3},
	}, modeWipe, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "attach import tags")

	assertPriorLink := func(want links.Link, tagID, clicks int64) {
		t.Helper()
		var gotURL, gotTitle, gotSlug string
		var gotTags, gotClicks int64
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT url, title, slug FROM link WHERE user_id = $1 AND id = $2
		`, int64(uid), want.ID).Scan(&gotURL, &gotTitle, &gotSlug))
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT count(*) FROM link_tag
			WHERE entity_kind = 'link' AND entity_id = $1 AND tag_id = $2
		`, want.ID, tagID).Scan(&gotTags))
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT count(*) FROM click_log
			WHERE user_id = $1 AND entity_kind = 'link' AND entity_id = $2
		`, int64(uid), want.ID).Scan(&gotClicks))
		assert.Equal(t, want.URL, gotURL)
		assert.Equal(t, want.Title, gotTitle)
		assert.Equal(t, want.Slug, gotSlug)
		assert.EqualValues(t, 1, gotTags)
		assert.Equal(t, clicks, gotClicks)
	}
	assertPriorLink(first, firstTag.ID, 2)
	assertPriorLink(second, secondTag.ID, 1)

	var linksCount, tagsCount, relationsCount, clicksCount, faultRelations int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM link WHERE user_id = $1`, int64(uid)).Scan(&linksCount))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM tag WHERE user_id = $1`, int64(uid)).Scan(&tagsCount))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM link_tag WHERE entity_kind = 'link'`).Scan(&relationsCount))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM click_log WHERE user_id = $1 AND entity_kind = 'link'`, int64(uid)).Scan(&clicksCount))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM link_tag WHERE tag_id = $1`, faultTag.ID).Scan(&faultRelations))
	assert.EqualValues(t, 2, linksCount, "failed replacements must not remain")
	assert.EqualValues(t, 3, tagsCount, "the prior tag catalog must remain exact")
	assert.EqualValues(t, 2, relationsCount, "the prior tag relations must remain exact")
	assert.EqualValues(t, 3, clicksCount, "the prior click history must remain exact")
	assert.Zero(t, faultRelations, "failed staged relations must not remain")
}

// Migration 000014 removed the polymorphic relation FKs, so staged wipe must
// purge both child tables before replacing the owner-scoped link row.
func TestStagedImport_WipeDoesNotOrphanTagsOrClicks(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	lrepo := links.NewRepository(pool)
	trepo := tags.NewRepository(pool)

	tag, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "work", Color: "#fff"})
	require.NoError(t, err)
	original, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://wipe-target.example", Title: "Original", TagIDs: []int64{tag.ID},
	})
	require.NoError(t, err)
	_, err = lrepo.ClickAndResolve(ctx, original.ID)
	require.NoError(t, err)

	h := NewHandler(pool, nil)
	imported, skipped, wiped, warnings, err := h.importItemsWithMode(ctx, uid, []Item{{
		URL: "https://wipe-target.example", Title: "Replacement",
	}}, modeWipe, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, imported)
	assert.Zero(t, skipped)
	assert.Equal(t, 1, wiped)
	assert.Empty(t, warnings)

	var newID int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM link WHERE user_id = $1 AND url = $2`, int64(uid), "https://wipe-target.example").Scan(&newID))
	assert.NotEqual(t, original.ID, newID, "wipe must replace with a fresh row, not reuse the old id")

	var tagRows, clickRows int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM link_tag WHERE entity_kind = 'link' AND entity_id = $1`, original.ID).Scan(&tagRows))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM click_log WHERE entity_kind = 'link' AND entity_id = $1`, original.ID).Scan(&clickRows))
	assert.EqualValues(t, 0, tagRows, "wipe must not leave an orphaned link_tag row for the replaced link")
	assert.EqualValues(t, 0, clickRows, "wipe must not leave an orphaned click_log row for the replaced link")
}

func TestStagedImport_PreservesGlobalSlugsAndOwnerScopedURLs(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	owner := testdb.SeedUser(t, pool, "import-owner@test.local", "admin")
	other := testdb.SeedUser(t, pool, "import-other@test.local", "admin")
	lrepo := links.NewRepository(pool)
	trepo := tags.NewRepository(pool)
	otherTag, err := trepo.Create(ctx, other, tags.CreateInput{Name: "other tag", Color: "#fff"})
	require.NoError(t, err)

	otherLink, err := lrepo.Create(ctx, other, links.CreateInput{
		URL: "https://shared-owner-url.example", Title: "Shared Title", TagIDs: []int64{otherTag.ID},
	})
	require.NoError(t, err)
	_, err = lrepo.ClickAndResolve(ctx, otherLink.ID)
	require.NoError(t, err)
	assert.Equal(t, "shared-title", otherLink.Slug)

	h := NewHandler(pool, nil)
	imported, skipped, wiped, warnings, err := h.importItemsWithMode(ctx, owner, []Item{
		{URL: "https://shared-owner-url.example", Title: "Shared Title"},
		{URL: "https://second-owner-url.example", Title: "Shared Title"},
	}, modeSkip, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, imported, "another user's URL must not count as an idempotency conflict")
	assert.Zero(t, skipped)
	assert.Zero(t, wiped)
	assert.Empty(t, warnings)

	rows, err := pool.Query(ctx, `SELECT id, slug FROM link WHERE user_id = $1 ORDER BY id`, int64(owner))
	require.NoError(t, err)
	var ownerIDs []int64
	var ownerSlugs []string
	for rows.Next() {
		var id int64
		var slug string
		require.NoError(t, rows.Scan(&id, &slug))
		ownerIDs = append(ownerIDs, id)
		ownerSlugs = append(ownerSlugs, slug)
	}
	require.NoError(t, rows.Err())
	rows.Close()
	assert.Equal(t, []string{"shared-title-2", "shared-title-3"}, ownerSlugs)

	imported, skipped, wiped, warnings, err = h.importItemsWithMode(ctx, owner, []Item{{
		URL: "https://shared-owner-url.example", Title: "Replacement",
	}}, modeWipe, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, imported)
	assert.Zero(t, skipped)
	assert.Equal(t, 1, wiped)
	assert.Empty(t, warnings)

	var otherTitle string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT title FROM link WHERE user_id = $1 AND id = $2`, int64(other), otherLink.ID).Scan(&otherTitle))
	assert.Equal(t, "Shared Title", otherTitle)
	for _, table := range []string{"link_tag", "click_log"} {
		var refs int64
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM `+table+` WHERE entity_kind = 'link' AND entity_id = $1`, otherLink.ID).Scan(&refs))
		assert.EqualValues(t, 1, refs, "import wipe must preserve the bystander's %s rows", table)
	}

	var replacementID int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM link WHERE user_id = $1 AND url = $2`, int64(owner), "https://shared-owner-url.example").Scan(&replacementID))
	assert.NotEqual(t, ownerIDs[0], replacementID)
}

func TestStagedImport_DeduplicatesRepeatedURLs(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "import-duplicates@test.local", "admin")
	h := NewHandler(pool, nil)
	items := []Item{
		{URL: "https://repeated-import.example", Title: "First", ClickCount: 1},
		{URL: "https://repeated-import.example", Title: "Last", ClickCount: 2},
	}

	imported, skipped, wiped, _, err := h.importItemsWithMode(ctx, uid, items, modeSkip, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, imported)
	assert.Equal(t, 1, skipped)
	assert.Zero(t, wiped)

	var title string
	var clicks int
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT l.title, count(c.id)
        FROM link l
        LEFT JOIN click_log c ON c.entity_kind = 'link' AND c.entity_id = l.id
        WHERE l.user_id = $1 AND l.url = $2
        GROUP BY l.id
    `, int64(uid), items[0].URL).Scan(&title, &clicks))
	assert.Equal(t, "First", title)
	assert.Equal(t, 1, clicks)

	imported, skipped, wiped, _, err = h.importItemsWithMode(ctx, uid, items, modeWipe, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, imported)
	assert.Equal(t, 1, skipped)
	assert.Equal(t, 1, wiped)
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT l.title, count(c.id)
        FROM link l
        LEFT JOIN click_log c ON c.entity_kind = 'link' AND c.entity_id = l.id
        WHERE l.user_id = $1 AND l.url = $2
        GROUP BY l.id
    `, int64(uid), items[0].URL).Scan(&title, &clicks))
	assert.Equal(t, "Last", title)
	assert.Equal(t, 2, clicks)
}
