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

// TestRepository_CreateWithPassword locks the basic write path: a password
// on create hashes to something stored server-side, never the plaintext,
// and HasPassword reflects it on every read.
func TestRepository_CreateWithPassword(t *testing.T) {
	ctx, frepo, _ := setup(t)

	pw := "hunter22"
	f, err := frepo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	assert.True(t, f.HasPassword)

	hash, err := frepo.PasswordHashFor(ctx, f.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	assert.NotEqual(t, pw, *hash, "stored value must be a hash, not the plaintext")
	assert.True(t, folders.VerifyPassword(*hash, pw))

	unprotected, err := frepo.Create(ctx, folders.CreateInput{Name: "Open", Color: "#def"})
	require.NoError(t, err)
	assert.False(t, unprotected.HasPassword)
	hash, err = frepo.PasswordHashFor(ctx, unprotected.ID)
	require.NoError(t, err)
	assert.Nil(t, hash)
}

// TestRepository_List_RedactsPreviewsForProtectedFolders locks the
// always-on redaction rule (CLAUDE.md folder-password invariant): a
// protected folder's preview_links/preview_folders are cleared in EVERY
// list response, independent of any unlock token — that gate is the
// handler's job for the SEPARATE "list what's inside" call, not this one.
func TestRepository_List_RedactsPreviewsForProtectedFolders(t *testing.T) {
	ctx, frepo, lrepo := setup(t)

	pw := "hunter22"
	protected, err := frepo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)
	_, err = lrepo.Create(ctx, links.CreateInput{URL: "https://hidden.example", Title: "Hidden", FolderID: &protected.ID})
	require.NoError(t, err)

	open, err := frepo.Create(ctx, folders.CreateInput{Name: "Open", Color: "#def"})
	require.NoError(t, err)
	_, err = lrepo.Create(ctx, links.CreateInput{URL: "https://visible.example", Title: "Visible", FolderID: &open.ID})
	require.NoError(t, err)

	list, err := frepo.List(ctx, folders.ListQuery{RootOnly: true})
	require.NoError(t, err)
	require.Len(t, list, 2)

	byName := map[string]folders.Folder{}
	for _, f := range list {
		byName[f.Name] = f
	}

	secret := byName["Secret"]
	assert.True(t, secret.HasPassword)
	assert.EqualValues(t, 1, secret.LinkCount, "counts still show — only the preview content is redacted")
	assert.Empty(t, secret.Previews, "a protected folder's preview_links must be redacted in every List response")

	visible := byName["Open"]
	assert.False(t, visible.HasPassword)
	assert.NotEmpty(t, visible.Previews, "an unprotected folder's previews must NOT be redacted")

	got, err := frepo.Get(ctx, protected.ID)
	require.NoError(t, err)
	assert.True(t, got.HasPassword)
	assert.Empty(t, got.Previews)
}

// TestRepository_Update_SetPasswordFirstTime_NoCurrentPasswordNeeded covers
// the "no admin bypass, but setting a password for the first time doesn't
// need one either" half of the CLAUDE.md-documented decision — there's
// nothing to authorize against yet.
func TestRepository_Update_SetPasswordFirstTime_NoCurrentPasswordNeeded(t *testing.T) {
	ctx, frepo, _ := setup(t)

	f, err := frepo.Create(ctx, folders.CreateInput{Name: "Open", Color: "#abc"})
	require.NoError(t, err)
	require.False(t, f.HasPassword)

	pw := "newpass1"
	updated, err := frepo.Update(ctx, f.ID, folders.UpdateInput{PasswordSet: true, Password: &pw})
	require.NoError(t, err, "setting a password for the first time must not require CurrentPassword")
	assert.True(t, updated.HasPassword)

	hash, err := frepo.PasswordHashFor(ctx, f.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	assert.True(t, folders.VerifyPassword(*hash, pw))
}

// TestRepository_Update_ChangePassword_RequiresCurrentPassword locks the
// CLAUDE.md-documented decision: changing OR removing an EXISTING password
// requires proving you know the current one, with no admin bypass.
func TestRepository_Update_ChangePassword_RequiresCurrentPassword(t *testing.T) {
	ctx, frepo, _ := setup(t)

	oldPW := "oldpass1"
	f, err := frepo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &oldPW})
	require.NoError(t, err)

	newPW := "newpass1"

	// Missing CurrentPassword entirely.
	_, err = frepo.Update(ctx, f.ID, folders.UpdateInput{PasswordSet: true, Password: &newPW})
	require.Error(t, err)
	assertWrongPassword(t, err)

	// Wrong CurrentPassword.
	wrong := "definitely-not-it"
	_, err = frepo.Update(ctx, f.ID, folders.UpdateInput{PasswordSet: true, Password: &newPW, CurrentPassword: &wrong})
	require.Error(t, err)
	assertWrongPassword(t, err)

	// The password must be UNCHANGED after both rejected attempts.
	hash, err := frepo.PasswordHashFor(ctx, f.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	assert.True(t, folders.VerifyPassword(*hash, oldPW), "rejected change attempts must not mutate the stored hash")

	// Correct CurrentPassword succeeds.
	updated, err := frepo.Update(ctx, f.ID, folders.UpdateInput{PasswordSet: true, Password: &newPW, CurrentPassword: &oldPW})
	require.NoError(t, err)
	assert.True(t, updated.HasPassword)

	hash, err = frepo.PasswordHashFor(ctx, f.ID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	assert.True(t, folders.VerifyPassword(*hash, newPW))
	assert.False(t, folders.VerifyPassword(*hash, oldPW), "old password must no longer verify")
}

// TestRepository_Update_RemovePassword_RequiresCurrentPassword mirrors the
// change-password test for the null (remove-protection) branch.
func TestRepository_Update_RemovePassword_RequiresCurrentPassword(t *testing.T) {
	ctx, frepo, _ := setup(t)

	pw := "correctpw"
	f, err := frepo.Create(ctx, folders.CreateInput{Name: "Secret", Color: "#abc", Password: &pw})
	require.NoError(t, err)

	// No CurrentPassword → rejected, folder stays protected.
	_, err = frepo.Update(ctx, f.ID, folders.UpdateInput{PasswordSet: true, Password: nil})
	require.Error(t, err)
	assertWrongPassword(t, err)
	hash, err := frepo.PasswordHashFor(ctx, f.ID)
	require.NoError(t, err)
	assert.NotNil(t, hash, "rejected removal must leave the folder protected")

	// Correct CurrentPassword → removed.
	updated, err := frepo.Update(ctx, f.ID, folders.UpdateInput{PasswordSet: true, Password: nil, CurrentPassword: &pw})
	require.NoError(t, err)
	assert.False(t, updated.HasPassword)
	hash, err = frepo.PasswordHashFor(ctx, f.ID)
	require.NoError(t, err)
	assert.Nil(t, hash)
}

// TestRepository_Update_RemovePassword_OnUnprotectedFolder_IsIdempotent
// covers the redundant-no-op case: removing a password from a folder that
// never had one succeeds without requiring (or even accepting) a
// CurrentPassword, since there's nothing to authorize against.
func TestRepository_Update_RemovePassword_OnUnprotectedFolder_IsIdempotent(t *testing.T) {
	ctx, frepo, _ := setup(t)

	f, err := frepo.Create(ctx, folders.CreateInput{Name: "Open", Color: "#abc"})
	require.NoError(t, err)

	updated, err := frepo.Update(ctx, f.ID, folders.UpdateInput{PasswordSet: true, Password: nil})
	require.NoError(t, err)
	assert.False(t, updated.HasPassword)
}

func assertWrongPassword(t *testing.T, err error) {
	t.Helper()
	var he *httperr.Error
	require.ErrorAs(t, err, &he, "must be a typed httperr.Error, not raw")
	assert.Equal(t, 401, he.Status)
	assert.Equal(t, "wrong_password", he.Code)
}
