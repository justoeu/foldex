//go:build integration

package backupagent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backup"
	"foldex/internal/testdb"
)

// emptySourceBucket is the SOURCE side of the export for these tests: a user
// with no stored objects still gets a complete, restorable ZIP (database.json
// + manifest), which is exactly what a fresh instance's users look like.
type emptySourceBucket struct{}

func (emptySourceBucket) WalkObjects(context.Context, string, func(backup.ObjectInfo) error) error {
	return nil
}

func (emptySourceBucket) OpenObject(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (emptySourceBucket) PutObjectStream(context.Context, string, io.Reader, int64, string) error {
	return nil
}

func (emptySourceBucket) ExistingObjects(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (emptySourceBucket) DeleteObjects(context.Context, []string) error { return nil }

func TestUserZip_EndToEndShipsARestorableZipPerActiveUser(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	uid1 := testdb.SeedUser(t, pool, "userzip-one@test.local", "")
	uid2 := testdb.SeedUser(t, pool, "userzip-two@test.local", "")
	// A disabled account must not be exported: 'active' in the listing query
	// is the whole filter.
	uid3 := testdb.SeedUser(t, pool, "userzip-off@test.local", "")
	_, err := pool.Exec(ctx, `UPDATE app_user SET status = 'disabled' WHERE id = $1`, int64(uid3))
	require.NoError(t, err)

	for i, uid := range []int64{int64(uid1), int64(uid2)} {
		_, err := pool.Exec(ctx,
			`INSERT INTO link (url, title, slug, user_id) VALUES ($1, $2, $3, $4)`,
			fmt.Sprintf("https://example.com/userzip-%d", i), fmt.Sprintf("link %d", i),
			fmt.Sprintf("userzip-it-%d", i), uid)
		require.NoError(t, err)
	}

	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	store := newRecorderStore()
	svc := backup.NewService(pool, emptySourceBucket{}, testLogger())
	cfg := Config{
		AgeRecipients: []string{identity.Recipient().String()},
		RetainUserZip: 7, RetentionMode: "agent",
	}
	job, err := NewUserZipJob(cfg, pool, svc, store, testLogger())
	require.NoError(t, err)

	artifact, meta, reason, runErr := job.Run(ctx)
	require.NoError(t, runErr)
	assert.Empty(t, reason)
	assert.Nil(t, artifact)
	assert.Equal(t, 2, meta["users"])
	assert.NotContains(t, meta, "failed_users")
	assert.NotContains(t, meta, "deferred_users")

	require.Len(t, store.uploads, 2)
	for _, uid := range []int64{int64(uid1), int64(uid2)} {
		prefix := fmt.Sprintf("backups/users/%d/", uid)
		var ciphertext []byte
		for key, data := range store.uploads {
			if strings.HasPrefix(key, prefix) {
				require.True(t, strings.HasSuffix(key, ".zip.age"), "key %s must say the bytes are an encrypted zip", key)
				ciphertext = data
			}
		}
		require.NotNil(t, ciphertext, "user %d must have an upload under its own prefix", uid)
		assert.False(t, strings.HasPrefix(string(ciphertext), "PK"), "a zip in the clear must never reach the bucket")

		// The DR contract end to end: standard age opens it, archive/zip
		// reads it, and the manifest counts the user's own row — nobody
		// else's (INV-105 rides on Export; this proves the agent kept it).
		r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
		require.NoError(t, err)
		plain, err := io.ReadAll(r)
		require.NoError(t, err)
		zr, err := zip.NewReader(bytes.NewReader(plain), int64(len(plain)))
		require.NoError(t, err)

		names := map[string]bool{}
		for _, f := range zr.File {
			names[f.Name] = true
		}
		assert.True(t, names["manifest.json"], "manifest.json missing from user %d's archive", uid)
		assert.True(t, names["database.json"], "database.json missing from user %d's archive", uid)

		var manifest backup.Manifest
		for _, f := range zr.File {
			if f.Name != "manifest.json" {
				continue
			}
			rc, err := f.Open()
			require.NoError(t, err)
			require.NoError(t, json.NewDecoder(rc).Decode(&manifest))
			rc.Close()
		}
		assert.EqualValues(t, 1, manifest.Counts.Links, "each archive carries its OWNER's single link, never the other tenant's")
	}
}
