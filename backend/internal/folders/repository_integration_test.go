//go:build integration

package folders_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/pkg/httperr"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

func setup(t *testing.T) (context.Context, *folders.Repository, *links.Repository) {
	t.Helper()
	pool := testdb.New(t)
	return context.Background(), folders.NewRepository(pool), links.NewRepository(pool)
}

// TestRepository_NestedCRUD covers CLAUDE.md §4 "folders are 1:N exclusive
// AND nestable" — parent_id is honored on create, surfaces on get, can be
// updated, and List with RootOnly/ParentID scopes correctly.
func TestRepository_NestedCRUD(t *testing.T) {
	ctx, frepo, _ := setup(t)

	root, err := frepo.Create(ctx, folders.CreateInput{Name: "Root", Color: "#111111"})
	require.NoError(t, err)
	assert.Nil(t, root.ParentID, "root folder must have NULL parent")

	child, err := frepo.Create(ctx, folders.CreateInput{Name: "Child", Color: "#222222", ParentID: &root.ID})
	require.NoError(t, err)
	require.NotNil(t, child.ParentID)
	assert.Equal(t, root.ID, *child.ParentID)

	// Root-only list must exclude the child.
	roots, err := frepo.List(ctx, folders.ListQuery{RootOnly: true})
	require.NoError(t, err)
	require.Len(t, roots, 1)
	assert.Equal(t, "Root", roots[0].Name)
	assert.EqualValues(t, 1, roots[0].FolderCount, "root has one subfolder")

	// ParentID scope must surface only direct children of root.
	directChildren, err := frepo.List(ctx, folders.ListQuery{ParentID: &root.ID})
	require.NoError(t, err)
	require.Len(t, directChildren, 1)
	assert.Equal(t, "Child", directChildren[0].Name)

	// Flat list (no scoping) returns both.
	flat, err := frepo.List(ctx, folders.ListQuery{})
	require.NoError(t, err)
	assert.Len(t, flat, 2)
}

// TestRepository_DeleteSetsChildrenAndLinksToNull locks the invariant from
// CLAUDE.md §4: "Both FKs are ON DELETE SET NULL — deleting a folder promotes
// children to root and ungroups its links." This invariant was previously
// only documented; without this test, switching either FK to CASCADE or
// RESTRICT would ship green.
func TestRepository_DeleteSetsChildrenAndLinksToNull(t *testing.T) {
	ctx, frepo, lrepo := setup(t)

	parent, err := frepo.Create(ctx, folders.CreateInput{Name: "Parent", Color: "#abc"})
	require.NoError(t, err)
	child, err := frepo.Create(ctx, folders.CreateInput{Name: "Child", Color: "#def", ParentID: &parent.ID})
	require.NoError(t, err)
	link, err := lrepo.Create(ctx, links.CreateInput{
		URL: "https://kept.example", Title: "Survives parent delete", FolderID: &parent.ID,
	})
	require.NoError(t, err)

	require.NoError(t, frepo.Delete(ctx, parent.ID))

	// Child folder must survive with parent_id = NULL.
	gotChild, err := frepo.Get(ctx, child.ID)
	require.NoError(t, err, "child folder must survive a parent delete")
	assert.Nil(t, gotChild.ParentID, "child must be promoted to root")

	// Link must survive with folder_id = NULL (back to home/ungrouped).
	gotLink, err := lrepo.Get(ctx, link.ID)
	require.NoError(t, err, "link must survive a non-cascade folder delete")
	assert.Nil(t, gotLink.FolderID, "link must become ungrouped")
}

// TestRepository_DeleteCascadeRemovesSubtree locks the CTE-based cascade. The
// recursive subtree CTE deletes the target folder + every descendant + every
// link in any of them. Tags survive (only the link↔tag associations vanish).
func TestRepository_DeleteCascadeRemovesSubtree(t *testing.T) {
	ctx, frepo, lrepo := setup(t)

	a, err := frepo.Create(ctx, folders.CreateInput{Name: "A", Color: "#a"})
	require.NoError(t, err)
	b, err := frepo.Create(ctx, folders.CreateInput{Name: "B", Color: "#b", ParentID: &a.ID})
	require.NoError(t, err)
	c, err := frepo.Create(ctx, folders.CreateInput{Name: "C", Color: "#c", ParentID: &b.ID})
	require.NoError(t, err)

	la, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://a", Title: "A", FolderID: &a.ID})
	lb, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://b", Title: "B", FolderID: &b.ID})
	lc, _ := lrepo.Create(ctx, links.CreateInput{URL: "https://c", Title: "C", FolderID: &c.ID})

	require.NoError(t, frepo.DeleteCascade(ctx, a.ID))

	for _, fid := range []int64{a.ID, b.ID, c.ID} {
		_, err := frepo.Get(ctx, fid)
		assert.ErrorIs(t, err, httperr.ErrNotFound, "folder %d must be gone after cascade", fid)
	}
	for _, lid := range []int64{la.ID, lb.ID, lc.ID} {
		_, err := lrepo.Get(ctx, lid)
		assert.ErrorIs(t, err, httperr.ErrNotFound, "link %d must be gone after cascade", lid)
	}
}

// TestRepository_DeleteCascadeDoesNotOrphanTagsOrClicks is the regression
// lock for the gap migration 000014 introduced: link_tag/click_log lost
// their ON DELETE CASCADE FK to link(id) when those tables were
// polymorphized, so DeleteCascade's `DELETE FROM link` alone would silently
// leave dangling link_tag/click_log rows behind (entity_id pointing at a
// link that no longer exists) unless the repository purges them itself.
func TestRepository_DeleteCascadeDoesNotOrphanTagsOrClicks(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	frepo := folders.NewRepository(pool)
	lrepo := links.NewRepository(pool)
	trepo := tags.NewRepository(pool)

	folder, err := frepo.Create(ctx, folders.CreateInput{Name: "Doomed", Color: "#abc"})
	require.NoError(t, err)
	tag, err := trepo.Create(ctx, tags.CreateInput{Name: "work", Color: "#fff"})
	require.NoError(t, err)
	link, err := lrepo.Create(ctx, links.CreateInput{
		URL: "https://orphan-check.example", Title: "L", FolderID: &folder.ID, TagIDs: []int64{tag.ID},
	})
	require.NoError(t, err)
	_, err = lrepo.ClickAndResolve(ctx, link.ID)
	require.NoError(t, err)

	require.NoError(t, frepo.DeleteCascade(ctx, folder.ID))

	var tagRows, clickRows int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM link_tag WHERE entity_kind = 'link' AND entity_id = $1`, link.ID).Scan(&tagRows))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM click_log WHERE entity_kind = 'link' AND entity_id = $1`, link.ID).Scan(&clickRows))
	assert.EqualValues(t, 0, tagRows, "DeleteCascade must not leave an orphaned link_tag row")
	assert.EqualValues(t, 0, clickRows, "DeleteCascade must not leave an orphaned click_log row")
}

// TestRepository_DeleteCascadeOnEmptyFolder confirms the cascade path also
// handles the trivial "empty leaf" case — no links, no children.
func TestRepository_DeleteCascadeOnEmptyFolder(t *testing.T) {
	ctx, frepo, _ := setup(t)

	f, err := frepo.Create(ctx, folders.CreateInput{Name: "empty", Color: "#000"})
	require.NoError(t, err)
	require.NoError(t, frepo.DeleteCascade(ctx, f.ID))
	_, err = frepo.Get(ctx, f.ID)
	assert.ErrorIs(t, err, httperr.ErrNotFound)
}

// TestRepository_UpdateRejectsCycle locks the serializable cycle guard.
// Trying to set parent_id = a descendant must return a 409 (or any non-nil
// error wrapped in httperr.Error) — without it, A→B→A would orphan the
// subtree from the root.
func TestRepository_UpdateRejectsCycle(t *testing.T) {
	ctx, frepo, _ := setup(t)

	a, err := frepo.Create(ctx, folders.CreateInput{Name: "A", Color: "#a"})
	require.NoError(t, err)
	b, err := frepo.Create(ctx, folders.CreateInput{Name: "B", Color: "#b", ParentID: &a.ID})
	require.NoError(t, err)

	// A → B (A becomes child of its own child) must be refused with the
	// typed 409 parent_cycle. Asserting just "error" would pass even when a
	// future regression surfaces a 500 — the API client wouldn't be able to
	// tell the user-fixable case apart from a real server error.
	_, err = frepo.Update(ctx, a.ID, folders.UpdateInput{ParentIDSet: true, ParentID: &b.ID})
	require.Error(t, err, "self-cycle must be rejected")
	var he *httperr.Error
	require.ErrorAs(t, err, &he, "cycle must be typed httperr.Error, not raw")
	assert.Equal(t, 409, he.Status)
	assert.Equal(t, "parent_cycle", he.Code)
}
