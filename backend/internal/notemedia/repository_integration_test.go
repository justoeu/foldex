//go:build integration

package notemedia_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/notemedia"
	"foldex/internal/testdb"
)

type memoryStorage struct {
	objects map[string][]byte
}

func (s *memoryStorage) Upload(_ context.Context, key string, data []byte, _ string) error {
	s.objects[key] = data
	return nil
}

func (s *memoryStorage) GetObject(_ context.Context, key string) ([]byte, string, error) {
	return s.objects[key], "image/jpeg", nil
}

func (s *memoryStorage) DeleteObject(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

func TestSystemSweepExpired_ReclaimsAbandonedOwnedUpload(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "owner@test.local", "user")
	const key = "notes/e7a3797c-08de-4ec4-a591-33f7282f9d61.jpg"
	storage := &memoryStorage{objects: map[string][]byte{key: []byte("orphan")}}
	require.NoError(t, notemedia.RegisterLease(ctx, pool, uid, key))
	_, err := pool.Exec(ctx, `
        UPDATE note_media SET lease_expires_at = now() - interval '1 minute'
        WHERE user_id = $1 AND object_key = $2
    `, int64(uid), key)
	require.NoError(t, err)

	removed, err := notemedia.SystemSweepExpired(ctx, pool, storage, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, removed)
	assert.NotContains(t, storage.objects, key)
	var rows int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM note_media WHERE user_id = $1 AND object_key = $2`, int64(uid), key).Scan(&rows))
	assert.Zero(t, rows)
}

func TestSystemSweepExpired_KeepsReferencedMedia(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "owner@test.local", "user")
	const key = "notes/2d7248ea-5771-4c78-90f9-63b4c921e8dc.jpg"
	storage := &memoryStorage{objects: map[string][]byte{key: []byte("live")}}
	require.NoError(t, notemedia.RegisterLease(ctx, pool, uid, key))
	var noteID int64
	require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO note (user_id, title, slug) VALUES ($1, 'live', 'live-media')
        RETURNING id
    `, int64(uid)).Scan(&noteID))
	_, err := pool.Exec(ctx, `
        INSERT INTO note_media_ref (user_id, note_id, object_key) VALUES ($1, $2, $3)
    `, int64(uid), noteID, key)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
        UPDATE note_media SET lease_expires_at = now() - interval '1 minute'
        WHERE user_id = $1 AND object_key = $2
    `, int64(uid), key)
	require.NoError(t, err)

	removed, err := notemedia.SystemSweepExpired(ctx, pool, storage, 10)
	require.NoError(t, err)
	assert.Zero(t, removed)
	assert.Equal(t, []byte("live"), storage.objects[key])
}

func TestRestoreRefs_ObjectKeyCollisionFailsClosed(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	ownerUID := testdb.SeedUser(t, pool, "owner@test.local", "user")
	restoreUID := testdb.SeedUser(t, pool, "restore@test.local", "user")
	const key = "notes/b654f6e4-494d-4a4d-84c3-17dfde630c41.jpg"
	require.NoError(t, notemedia.RegisterLease(ctx, pool, ownerUID, key))

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	err = notemedia.RestoreRefs(ctx, tx, restoreUID, []string{key}, nil)
	require.ErrorContains(t, err, "insert restored note media")
	require.NoError(t, tx.Rollback(ctx))

	var actualOwner int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT user_id FROM note_media WHERE object_key = $1`, key).Scan(&actualOwner))
	assert.Equal(t, int64(ownerUID), actualOwner)
}
