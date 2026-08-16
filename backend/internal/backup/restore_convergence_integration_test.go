//go:build integration

package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backup"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

type failOnceBucket struct {
	base *stubBucket

	mu            sync.Mutex
	failNextPut   bool
	puts          int
	existsChecks  int
	existsLists   int
	deleteCalls   int
	deleteBatches int
	active        int
	maxActive     int
	delay         time.Duration
}

func (b *failOnceBucket) WalkObjects(ctx context.Context, prefix string, visit func(backup.ObjectInfo) error) error {
	return b.base.WalkObjects(ctx, prefix, visit)
}

func (b *failOnceBucket) OpenObject(ctx context.Context, key string) (io.ReadCloser, error) {
	return b.base.OpenObject(ctx, key)
}

func (b *failOnceBucket) PutObjectStream(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	b.mu.Lock()
	b.puts++
	b.active++
	if b.active > b.maxActive {
		b.maxActive = b.active
	}
	fail := b.failNextPut
	b.failNextPut = false
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		b.active--
		b.mu.Unlock()
	}()
	if fail {
		return errors.New("injected object upload failure")
	}
	if b.delay > 0 {
		timer := time.NewTimer(b.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return b.base.PutObjectStream(ctx, key, r, size, contentType)
}

func (b *failOnceBucket) ObjectExists(ctx context.Context, key string) (bool, error) {
	b.mu.Lock()
	b.existsChecks++
	b.mu.Unlock()
	return b.base.ObjectExists(ctx, key)
}

func (b *failOnceBucket) ExistingObjects(ctx context.Context, keys []string) (map[string]bool, error) {
	b.mu.Lock()
	b.existsLists++
	b.mu.Unlock()
	return b.base.ExistingObjects(ctx, keys)
}

func (b *failOnceBucket) DeleteObject(ctx context.Context, key string) error {
	b.mu.Lock()
	b.deleteCalls++
	b.mu.Unlock()
	return b.base.DeleteObject(ctx, key)
}

func (b *failOnceBucket) DeleteObjects(ctx context.Context, keys []string) error {
	b.mu.Lock()
	b.deleteBatches++
	b.mu.Unlock()
	return b.base.DeleteObjects(ctx, keys)
}

func (b *failOnceBucket) operationCounts() (puts, exists, maxActive int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.puts, b.existsChecks, b.maxActive
}

func (b *failOnceBucket) bulkOperationCounts() (existsLists, deleteCalls, deleteBatches int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.existsLists, b.deleteCalls, b.deleteBatches
}

func TestRestore_SkipRetryAfterFileFailureUsesCommittedMapping(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "retry@test.local", "admin")
	ctx := context.Background()

	sourceBucket := newStubBucket()
	seed := seedSnapshot(t, pool, uid, sourceBucket)
	tag, err := tags.NewRepository(pool).Create(ctx, uid, tags.CreateInput{Name: "note-tag", Color: "#abc"})
	require.NoError(t, err)
	note, err := notes.NewRepository(pool).Create(ctx, uid, notes.CreateInput{
		Title: "Retry note", BodyHTML: "<p>durable</p>", TagIDs: []int64{tag.ID},
	})
	require.NoError(t, err)
	_, err = notes.NewRepository(pool).SystemViewAndResolve(ctx, note.Slug)
	require.NoError(t, err)

	var archive bytes.Buffer
	_, err = backup.NewService(pool, sourceBucket, discardLogger()).Export(ctx, uid, &archive, nil)
	require.NoError(t, err)
	zr, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	require.NoError(t, err)

	// Simulate restoring onto an empty library while retaining shared sequences.
	for _, statement := range []string{
		`DELETE FROM link_tag`,
		`DELETE FROM click_log`,
		`DELETE FROM note`,
		`DELETE FROM link`,
		`UPDATE folder SET parent_id = NULL`,
		`DELETE FROM folder`,
		`DELETE FROM tag`,
	} {
		_, err = pool.Exec(ctx, statement)
		require.NoError(t, err)
	}

	destination := &failOnceBucket{base: newStubBucket(), failNextPut: true}
	svc := backup.NewService(pool, destination, discardLogger())
	_, err = svc.Restore(ctx, uid, zr, backup.ModeSkip)
	require.ErrorContains(t, err, "injected object upload failure")

	// The database phase committed before object I/O failed.
	assert.EqualValues(t, 2, scalar(t, pool, `SELECT count(*) FROM tag WHERE user_id=$1`, int64(uid)))
	assert.EqualValues(t, 1, scalar(t, pool, `SELECT count(*) FROM folder WHERE user_id=$1`, int64(uid)))
	assert.EqualValues(t, 2, scalar(t, pool, `SELECT count(*) FROM link WHERE user_id=$1`, int64(uid)))
	assert.EqualValues(t, 1, scalar(t, pool, `SELECT count(*) FROM note WHERE user_id=$1`, int64(uid)))
	assert.EqualValues(t, 2, scalar(t, pool, `SELECT count(*) FROM link_tag`))
	assert.EqualValues(t, 4, scalar(t, pool, `SELECT count(*) FROM click_log`))
	var firstLinkID int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM link WHERE user_id=$1 AND url='https://a.example'`, int64(uid)).Scan(&firstLinkID))
	assert.NotEqual(t, seed.linkA, firstLinkID, "restore must keep allocating fresh ids")

	rep, err := svc.Restore(ctx, uid, zr, backup.ModeSkip)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Files.Uploaded)
	var retryLinkID int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM link WHERE user_id=$1 AND url='https://a.example'`, int64(uid)).Scan(&retryLinkID))
	assert.Equal(t, firstLinkID, retryLinkID, "retry must reuse the committed old-to-new mapping")
	assert.EqualValues(t, 1, scalar(t, pool, `SELECT count(*) FROM folder WHERE user_id=$1`, int64(uid)))
	assert.EqualValues(t, 1, scalar(t, pool, `SELECT count(*) FROM note WHERE user_id=$1`, int64(uid)))
	assert.EqualValues(t, 2, scalar(t, pool, `SELECT count(*) FROM link_tag`))
	assert.EqualValues(t, 4, scalar(t, pool, `SELECT count(*) FROM click_log`))

	puts, exists, maxActive := destination.operationCounts()
	existsLists, _, _ := destination.bulkOperationCounts()
	require.LessOrEqual(t, maxActive, 8, "object uploads must have a fixed concurrency ceiling")
	assert.Zero(t, exists, "retry must not issue per-object HEAD requests")
	assert.Equal(t, 2, existsLists, "initial attempt and incomplete retry each need one bulk listing")
	_, err = svc.Restore(ctx, uid, zr, backup.ModeSkip)
	require.NoError(t, err)
	putsAfter, existsAfter, _ := destination.operationCounts()
	existsListsAfter, _, _ := destination.bulkOperationCounts()
	assert.Equal(t, puts, putsAfter, "a completed repeat must not upload objects again")
	assert.Equal(t, exists, existsAfter, "a completed repeat must not issue per-object HEAD requests")
	assert.Equal(t, existsLists, existsListsAfter, "a completed repeat must not list object namespaces")
}

type queryCounter struct{ total atomic.Int64 }

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.total.Add(1)
	return ctx
}

func (*queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *queryCounter) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceCopyFromStartData) context.Context {
	c.total.Add(1)
	return ctx
}

func (*queryCounter) TraceCopyFromEnd(context.Context, *pgx.Conn, pgx.TraceCopyFromEndData) {}

func tracedPool(t *testing.T, source *pgxpool.Pool, tracer pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	cfg := source.Config()
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func queryBoundArchive(t *testing.T, rows int64) *zip.Reader {
	t.Helper()
	now := time.Now().UTC()
	snap := backup.Snapshot{Version: backup.DatabaseSnapshotVersion}
	for i := int64(1); i <= rows; i++ {
		snap.Tags = append(snap.Tags, backup.TagRow{ID: i, Name: fmt.Sprintf("tag-%d", i), Color: "#abc", CreatedAt: now})
		snap.Folders = append(snap.Folders, backup.FolderRow{ID: i, Name: fmt.Sprintf("folder-%d", i), Color: "#abc", CreatedAt: now})
		folderID := i
		snap.Links = append(snap.Links, backup.LinkRow{
			ID: i, URL: fmt.Sprintf("https://%d.example", i), Title: fmt.Sprintf("Link %d", i),
			Slug: fmt.Sprintf("link-%d", i), PreviewStatus: "pending", FolderID: &folderID,
			CreatedAt: now, UpdatedAt: now,
		})
		snap.Notes = append(snap.Notes, backup.NoteRow{
			ID: i, Title: fmt.Sprintf("Note %d", i), Slug: fmt.Sprintf("note-%d", i),
			BodyHTML: "<p>x</p>", FolderID: &folderID, CreatedAt: now, UpdatedAt: now,
		})
		snap.LinkTags = append(snap.LinkTags, backup.LinkTagRow{LinkID: i, TagID: i})
		snap.NoteTags = append(snap.NoteTags, backup.NoteTagRow{NoteID: i, TagID: i})
		snap.ClickLogs = append(snap.ClickLogs, backup.ClickRow{LinkID: i, ClickedAt: now})
		snap.NoteClicks = append(snap.NoteClicks, backup.NoteClickRow{NoteID: i, ClickedAt: now})
	}
	db := mustJSON(t, snap)
	return zipFromEntries(t, map[string][]byte{
		"manifest.json": mustJSON(t, backup.Manifest{
			Kind: backup.ManifestKind, Version: backup.ManifestVersion,
			SchemaVersion: backup.CurrentSchemaVersion,
			Checksums:     map[string]string{"database.json": sha256hex(db)},
		}),
		"database.json": db,
	})
}

func TestRestore_SkipDatabaseOperationsStayBoundedAsRowsGrow(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "query-bound@test.local", "admin")
	zr := queryBoundArchive(t, 48)

	counter := &queryCounter{}
	svc := backup.NewService(tracedPool(t, pool, counter), newStubBucket(), discardLogger())
	_, err := svc.Restore(context.Background(), uid, zr, backup.ModeSkip)
	require.NoError(t, err)
	t.Logf("skip restore pgx query/CopyFrom operations for 48 rows/type: %d", counter.total.Load())
	assert.LessOrEqual(t, counter.total.Load(), int64(40),
		"restore query count must be cardinality-independent; row-at-a-time entity/slug work regressed")
}

func TestRestore_DuplicateDatabaseOperationsStayBoundedAsRowsGrow(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "query-bound-duplicate@test.local", "admin")
	counter := &queryCounter{}
	svc := backup.NewService(tracedPool(t, pool, counter), newStubBucket(), discardLogger())
	_, err := svc.Restore(context.Background(), uid, queryBoundArchive(t, 48), backup.ModeDuplicate)
	require.NoError(t, err)
	t.Logf("duplicate restore pgx query/CopyFrom operations for 48 rows/type: %d", counter.total.Load())
	assert.LessOrEqual(t, counter.total.Load(), int64(40),
		"duplicate restore query count must be cardinality-independent")
}

func TestRestore_ObjectUploadsUseBoundedConcurrency(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "object-bound@test.local", "admin")
	now := time.Now().UTC()
	const files = 24
	snap := backup.Snapshot{Version: backup.DatabaseSnapshotVersion}
	entries := map[string][]byte{}
	for i := int64(1); i <= files; i++ {
		key := fmt.Sprintf("images/%d.jpg", i)
		proxyURL := "/api/files/" + key
		snap.Links = append(snap.Links, backup.LinkRow{
			ID: i, URL: fmt.Sprintf("https://object-%d.example", i), Title: fmt.Sprintf("Object %d", i),
			Slug: fmt.Sprintf("object-%d", i), OGImageURL: &proxyURL, PreviewStatus: "ok",
			CreatedAt: now, UpdatedAt: now,
		})
		entries["files/"+key] = []byte("image")
	}
	db := mustJSON(t, snap)
	entries["database.json"] = db
	checksums := make(map[string]string, len(entries))
	for name, data := range entries {
		checksums[name] = sha256hex(data)
	}
	entries["manifest.json"] = mustJSON(t, backup.Manifest{
		Kind: backup.ManifestKind, Version: backup.ManifestVersion,
		SchemaVersion: backup.CurrentSchemaVersion,
		Checksums:     checksums,
	})
	bucket := &failOnceBucket{base: newStubBucket(), delay: 20 * time.Millisecond}
	_, err := backup.NewService(pool, bucket, discardLogger()).Restore(
		context.Background(), uid, zipFromEntries(t, entries), backup.ModeSkip)
	require.NoError(t, err)
	puts, exists, maxActive := bucket.operationCounts()
	assert.Equal(t, files, puts)
	assert.Zero(t, exists, "skip restore must not issue one HEAD per object")
	existsLists, _, _ := bucket.bulkOperationCounts()
	assert.Equal(t, 1, existsLists, "skip restore must resolve all explicit target keys in one bulk listing")
	assert.GreaterOrEqual(t, maxActive, 2, "uploads regressed to serial object I/O")
	assert.LessOrEqual(t, maxActive, 8, "uploads exceeded the fixed worker bound")
}

func TestRestore_WipeDeletesOnlyOwnedKeysInOneBatch(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "object-wipe-bound@test.local", "admin")
	base := newStubBucket()
	seedSnapshot(t, pool, uid, base)
	linkRepo := links.NewRepository(pool)
	for i := 0; i < 23; i++ {
		link, err := linkRepo.Create(context.Background(), uid, links.CreateInput{
			URL: fmt.Sprintf("https://wipe-object-%d.example", i), Title: fmt.Sprintf("Wipe Object %d", i),
		})
		require.NoError(t, err)
		key := fmt.Sprintf("images/%d.jpg", link.ID)
		base.objs[key] = []byte("image")
		require.NoError(t, linkRepo.UpdateOGImage(context.Background(), uid, link.ID, "/api/files/"+key))
	}
	svc := backup.NewService(pool, base, discardLogger())
	zr := exportToReader(t, svc, uid)

	bucket := &failOnceBucket{base: base}
	_, err := backup.NewService(pool, bucket, discardLogger()).Restore(
		context.Background(), uid, zr, backup.ModeWipe)
	require.NoError(t, err)

	_, singleDeletes, deleteBatches := bucket.bulkOperationCounts()
	assert.Zero(t, singleDeletes, "wipe must not issue one delete roundtrip per owned key")
	assert.Equal(t, 1, deleteBatches, "wipe must batch its explicit owner-scoped keys")
}
