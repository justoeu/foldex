//go:build integration

package entityrefs_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/entityrefs"
	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/pkg/authctx"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

type refEntity struct {
	kind string
	id   int64
	uid  authctx.UserID
}

type refFixture struct {
	ctx        context.Context
	pool       *pgxpool.Pool
	owner      authctx.UserID
	bystander  authctx.UserID
	ownerLink  refEntity
	ownerNote  refEntity
	otherLink  refEntity
	otherNote  refEntity
	ownerTagID int64
}

func newRefFixture(t *testing.T) refFixture {
	t.Helper()
	ctx := context.Background()
	pool := testdb.Shared(t)
	owner := testdb.SeedUser(t, pool, "entityrefs-owner@test.local", "admin")
	bystander := testdb.SeedUser(t, pool, "entityrefs-bystander@test.local", "user")

	trepo := tags.NewRepository(pool)
	ownerTag, err := trepo.Create(ctx, owner, tags.CreateInput{Name: "owner", Color: "#fff"})
	require.NoError(t, err)
	otherTag, err := trepo.Create(ctx, bystander, tags.CreateInput{Name: "other", Color: "#000"})
	require.NoError(t, err)

	lrepo := links.NewRepository(pool)
	nrepo := notes.NewRepository(pool)
	ownerLink, err := lrepo.Create(ctx, owner, links.CreateInput{
		URL: "https://entityrefs-owner.example/one", Title: "Owner link", TagIDs: []int64{ownerTag.ID},
	})
	require.NoError(t, err)
	ownerNote, err := nrepo.Create(ctx, owner, notes.CreateInput{Title: "Owner note", TagIDs: []int64{ownerTag.ID}})
	require.NoError(t, err)
	otherLink, err := lrepo.Create(ctx, bystander, links.CreateInput{
		URL: "https://entityrefs-other.example/one", Title: "Other link", TagIDs: []int64{otherTag.ID},
	})
	require.NoError(t, err)
	otherNote, err := nrepo.Create(ctx, bystander, notes.CreateInput{Title: "Other note", TagIDs: []int64{otherTag.ID}})
	require.NoError(t, err)

	entities := []refEntity{
		{kind: "link", id: ownerLink.ID, uid: owner},
		{kind: "note", id: ownerNote.ID, uid: owner},
		{kind: "link", id: otherLink.ID, uid: bystander},
		{kind: "note", id: otherNote.ID, uid: bystander},
	}
	for _, entity := range entities {
		_, err := pool.Exec(ctx, `
            INSERT INTO click_log (entity_kind, entity_id, user_id)
            VALUES ($1, $2, $3)
        `, entity.kind, entity.id, int64(entity.uid))
		require.NoError(t, err)
	}

	return refFixture{
		ctx:        ctx,
		pool:       pool,
		owner:      owner,
		bystander:  bystander,
		ownerLink:  entities[0],
		ownerNote:  entities[1],
		otherLink:  entities[2],
		otherNote:  entities[3],
		ownerTagID: ownerTag.ID,
	}
}

func (f refFixture) addOwnerLink(t *testing.T, suffix string, folderID *int64) refEntity {
	t.Helper()
	created, err := links.NewRepository(f.pool).Create(f.ctx, f.owner, links.CreateInput{
		URL:      "https://entityrefs-owner.example/" + suffix,
		Title:    "Owner link " + suffix,
		TagIDs:   []int64{f.ownerTagID},
		FolderID: folderID,
	})
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `
        INSERT INTO click_log (entity_kind, entity_id, user_id)
        VALUES ('link', $1, $2)
    `, created.ID, int64(f.owner))
	require.NoError(t, err)
	return refEntity{kind: "link", id: created.ID, uid: f.owner}
}

func (f refFixture) addOwnerNote(t *testing.T, title string, folderID *int64) refEntity {
	t.Helper()
	created, err := notes.NewRepository(f.pool).Create(f.ctx, f.owner, notes.CreateInput{
		Title: title, TagIDs: []int64{f.ownerTagID}, FolderID: folderID,
	})
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `
        INSERT INTO click_log (entity_kind, entity_id, user_id)
        VALUES ('note', $1, $2)
    `, created.ID, int64(f.owner))
	require.NoError(t, err)
	return refEntity{kind: "note", id: created.ID, uid: f.owner}
}

func assertReferenceCounts(t *testing.T, pool *pgxpool.Pool, entity refEntity, want int64) {
	t.Helper()
	for _, table := range []string{"link_tag", "click_log"} {
		var got int64
		err := pool.QueryRow(context.Background(), `
            SELECT count(*) FROM `+table+`
            WHERE entity_kind = $1 AND entity_id = $2
        `, entity.kind, entity.id).Scan(&got)
		require.NoError(t, err)
		assert.Equal(t, want, got, "%s references for %s %d", table, entity.kind, entity.id)
	}
}

func TestPurgeOne_DeletesOnlyTheRequestedKindAndID(t *testing.T) {
	f := newRefFixture(t)
	require.Equal(t, f.ownerLink.id, f.ownerNote.id, "fixture must exercise colliding polymorphic IDs")

	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer tx.Rollback(f.ctx)
	require.NoError(t, entityrefs.PurgeOne(f.ctx, tx, "link", f.ownerLink.id))
	require.NoError(t, tx.Commit(f.ctx))

	assertReferenceCounts(t, f.pool, f.ownerLink, 0)
	assertReferenceCounts(t, f.pool, f.ownerNote, 1)
	assertReferenceCounts(t, f.pool, f.otherLink, 1)
	assertReferenceCounts(t, f.pool, f.otherNote, 1)
}

func TestPurgeOwnerSet_DeletesTheWholeRequestedSetWithinOneOwnerAndKind(t *testing.T) {
	f := newRefFixture(t)
	secondOwnerLink := f.addOwnerLink(t, "two", nil)

	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer tx.Rollback(f.ctx)
	require.NoError(t, entityrefs.PurgeOwnerSet(f.ctx, tx, f.owner, "link", []int64{
		f.ownerLink.id,
		secondOwnerLink.id,
		f.otherLink.id,
	}))
	require.NoError(t, tx.Commit(f.ctx))

	assertReferenceCounts(t, f.pool, f.ownerLink, 0)
	assertReferenceCounts(t, f.pool, secondOwnerLink, 0)
	assertReferenceCounts(t, f.pool, f.ownerNote, 1)
	assertReferenceCounts(t, f.pool, f.otherLink, 1)
	assertReferenceCounts(t, f.pool, f.otherNote, 1)
}

func TestPurgeOwnerSet_EmptyAndInvalidSetsDoNotMutate(t *testing.T) {
	f := newRefFixture(t)

	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer tx.Rollback(f.ctx)
	require.NoError(t, entityrefs.PurgeOwnerSet(f.ctx, tx, f.owner, "link", nil))
	require.ErrorContains(t, entityrefs.PurgeOwnerSet(f.ctx, tx, f.owner, "folder", []int64{f.ownerLink.id}), "unsupported entity kind")
	require.NoError(t, tx.Commit(f.ctx))

	assertReferenceCounts(t, f.pool, f.ownerLink, 1)
	assertReferenceCounts(t, f.pool, f.ownerNote, 1)
}

func TestPurgeFolderSubtree_UsesTheTempSetWithoutCrossingOwner(t *testing.T) {
	f := newRefFixture(t)
	frepo := folders.NewRepository(f.pool)
	ownerFolder, err := frepo.Create(f.ctx, f.owner, folders.CreateInput{Name: "owner folder", Color: "#fff"})
	require.NoError(t, err)
	otherFolder, err := frepo.Create(f.ctx, f.bystander, folders.CreateInput{Name: "other folder", Color: "#000"})
	require.NoError(t, err)
	insideLink := f.addOwnerLink(t, "inside", &ownerFolder.ID)
	insideNote := f.addOwnerNote(t, "Inside note", &ownerFolder.ID)
	outsideLink := f.addOwnerLink(t, "outside", nil)

	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer tx.Rollback(f.ctx)
	_, err = tx.Exec(f.ctx, `CREATE TEMP TABLE _cascade_subtree (id bigint PRIMARY KEY) ON COMMIT DROP`)
	require.NoError(t, err)
	_, err = tx.Exec(f.ctx, `INSERT INTO _cascade_subtree (id) VALUES ($1), ($2)`, ownerFolder.ID, otherFolder.ID)
	require.NoError(t, err)
	require.NoError(t, entityrefs.PurgeFolderSubtree(f.ctx, tx, f.owner))
	require.NoError(t, tx.Commit(f.ctx))

	assertReferenceCounts(t, f.pool, insideLink, 0)
	assertReferenceCounts(t, f.pool, insideNote, 0)
	assertReferenceCounts(t, f.pool, outsideLink, 1)
	assertReferenceCounts(t, f.pool, f.otherLink, 1)
	assertReferenceCounts(t, f.pool, f.otherNote, 1)
}

func TestPurgeOwner_DeletesBothKindsWithoutTouchingBystanders(t *testing.T) {
	f := newRefFixture(t)

	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer tx.Rollback(f.ctx)
	require.NoError(t, entityrefs.PurgeOwner(f.ctx, tx, f.owner))
	require.NoError(t, tx.Commit(f.ctx))

	assertReferenceCounts(t, f.pool, f.ownerLink, 0)
	assertReferenceCounts(t, f.pool, f.ownerNote, 0)
	assertReferenceCounts(t, f.pool, f.otherLink, 1)
	assertReferenceCounts(t, f.pool, f.otherNote, 1)
}
