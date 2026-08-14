//go:build integration

package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backup"
	"foldex/internal/links"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

// stubBucket is the minimum StorageBucket the Service needs. Tests inject it
// directly rather than spinning up RustFS — backup.Service treats its storage
// as opaque, and the SHA-256 checksums + ZIP layout are what we care about.
type stubBucket struct {
	mu             sync.RWMutex
	objs           map[string][]byte
	walkCalls      int
	generatedOther int
	opened         []string
}

func newStubBucket() *stubBucket { return &stubBucket{objs: map[string][]byte{}} }

func (s *stubBucket) WalkObjects(ctx context.Context, prefix string, visit func(backup.ObjectInfo) error) error {
	s.mu.Lock()
	s.walkCalls++
	generatedOther := s.generatedOther
	owned := make([]backup.ObjectInfo, 0, len(s.objs))
	for key, value := range s.objs {
		if strings.HasPrefix(key, prefix) {
			owned = append(owned, backup.ObjectInfo{Key: key, Size: int64(len(value))})
		}
	}
	s.mu.Unlock()
	for _, object := range owned {
		if err := visit(object); err != nil {
			return err
		}
	}
	for i := 0; i < generatedOther; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(backup.ObjectInfo{Key: fmt.Sprintf("%sother-%d", prefix, i), Size: 1}); err != nil {
			return err
		}
	}
	return nil
}

func (s *stubBucket) OpenObject(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened = append(s.opened, key)
	return io.NopCloser(bytes.NewReader(s.objs[key])), nil
}

func (s *stubBucket) PutObjectStream(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs[key] = data
	return nil
}

func (s *stubBucket) ObjectExists(_ context.Context, key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.objs[key]
	return ok, nil
}

func (s *stubBucket) ExistingObjects(_ context.Context, keys []string) (map[string]bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(keys))
	for _, key := range keys {
		if _, ok := s.objs[key]; ok {
			out[key] = true
		}
	}
	return out, nil
}

func (s *stubBucket) DeleteObject(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objs, key)
	return nil
}

func (s *stubBucket) DeleteObjects(_ context.Context, keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		delete(s.objs, key)
	}
	return nil
}

// TestService_ExportProducesValidZipWithManifest locks the §4 invariant:
// backup is a complete DB + RustFS snapshot, manifest is uncompressed Store,
// every entry has a SHA-256 checksum.
func TestService_ExportProducesValidZipWithManifest(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()

	// Seed: one tag, two links, two files.
	trepo := tags.NewRepository(pool)
	tag, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "work", Color: "#abc"})
	require.NoError(t, err)
	lrepo := links.NewRepository(pool)
	la, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://a", Title: "A", TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	lb, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://b", Title: "B"})
	require.NoError(t, err)

	// Objects are attributed to a user through the row that references them —
	// keys themselves carry no tenant segment — so each one is seeded together
	// with the og_image_url that claims it.
	bucket := newStubBucket()
	shotA := fmt.Sprintf("screenshots/%d.jpg", la.ID)
	imgB := fmt.Sprintf("images/%d.jpg", lb.ID)
	bucket.objs[shotA] = []byte("img-1-bytes")
	bucket.objs[imgB] = []byte("img-2-bytes")
	require.NoError(t, lrepo.UpdateOGImage(ctx, uid, la.ID, "/api/files/"+shotA))
	require.NoError(t, lrepo.UpdateOGImage(ctx, uid, lb.ID, "/api/files/"+imgB))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := backup.NewService(pool, bucket, logger)

	var buf bytes.Buffer
	var callbackCounts backup.Counts
	rep, err := svc.Export(ctx, uid, &buf, func(c backup.Counts) error {
		callbackCounts = c
		return nil
	})
	require.NoError(t, err)

	// Counts from callback must equal the report.
	assert.Equal(t, rep.Counts, callbackCounts, "onCountsReady must receive the same Counts the Report carries")
	assert.EqualValues(t, 2, rep.Counts.Links)
	assert.EqualValues(t, 1, rep.Counts.Tags)
	assert.EqualValues(t, 2, rep.Counts.Files)

	// Parse the produced zip.
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	var sawManifest, sawDB bool
	var manifestCompression uint16
	files := map[string][]byte{}
	for _, f := range zr.File {
		switch f.Name {
		case "manifest.json":
			sawManifest = true
			manifestCompression = f.Method
		case "database.json":
			sawDB = true
		}
		rc, err := f.Open()
		require.NoError(t, err)
		body, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = body
	}
	require.True(t, sawManifest, "manifest.json must exist")
	require.True(t, sawDB, "database.json must exist")
	assert.EqualValues(t, zip.Store, manifestCompression, "manifest must be stored uncompressed so the frontend can read counts without inflate")

	// Both bucket objects must appear under files/.
	assert.Contains(t, files, "files/"+shotA)
	assert.Contains(t, files, "files/"+imgB)
	assert.Equal(t, []byte("img-1-bytes"), files["files/"+shotA])

	// Round-trip Validate on the produced zip (covers Service.Validate).
	v, err := svc.Validate(ctx, uid, zr)
	require.NoError(t, err)
	assert.True(t, v.OK, "fresh export must validate: %v", v.Errors)
	require.NotNil(t, v.Manifest)
}

func TestService_Validate_RejectsEmptyZip(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := backup.NewService(pool, newStubBucket(), logger)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	require.NoError(t, zw.Close())
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	v, err := svc.Validate(context.Background(), uid, zr)
	require.NoError(t, err)
	assert.False(t, v.OK)
	assert.NotEmpty(t, v.Errors)
}

// TestService_ExportAbortsWhenCallbackErrors confirms a header hook that
// returns an error short-circuits the export — the handler can use this to
// refuse a request before flushing response bytes.
func TestService_ExportAbortsWhenCallbackErrors(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := backup.NewService(pool, newStubBucket(), logger)
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)

	var buf bytes.Buffer
	_, err := svc.Export(ctx, uid, &buf, func(_ backup.Counts) error {
		return io.ErrUnexpectedEOF // sentinel, anything non-nil works
	})
	require.Error(t, err)
	assert.Equal(t, 0, buf.Len(), "no zip body should be flushed when the callback aborts")
	entries, readErr := os.ReadDir(tempDir)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "callback abort must close and remove the database spool")
}

func TestService_ExportUsesAndCleansBoundedDatabaseSpool(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "spool@test.local", "admin")
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)

	svc := backup.NewService(pool, newStubBucket(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	var out bytes.Buffer
	_, err := svc.Export(context.Background(), uid, &out, func(backup.Counts) error {
		entries, readErr := os.ReadDir(tempDir)
		require.NoError(t, readErr)
		require.Len(t, entries, 1, "database.json must be spooled before response headers are flushed")
		assert.Contains(t, entries[0].Name(), "foldex-backup-database-")
		info, statErr := entries[0].Info()
		require.NoError(t, statErr)
		assert.Greater(t, info.Size(), int64(0))
		return nil
	})
	require.NoError(t, err)

	entries, err := os.ReadDir(tempDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "export must remove its database spool on success")
}

func TestService_ExportStreamsGlobalListingAndRetainsOnlyOwnedMetadata(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "stream-list@test.local", "admin")
	lrepo := links.NewRepository(pool)
	link, err := lrepo.Create(context.Background(), uid, links.CreateInput{URL: "https://owned", Title: "Owned"})
	require.NoError(t, err)
	ownedKey := fmt.Sprintf("images/%d.jpg", link.ID)
	require.NoError(t, lrepo.UpdateOGImage(context.Background(), uid, link.ID, "/api/files/"+ownedKey))

	bucket := newStubBucket()
	bucket.objs[ownedKey] = []byte("owned")
	bucket.generatedOther = 50_000
	svc := backup.NewService(pool, bucket, slog.New(slog.NewTextHandler(io.Discard, nil)))

	var out bytes.Buffer
	rep, err := svc.Export(context.Background(), uid, &out, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rep.Counts.Files)
	bucket.mu.RLock()
	defer bucket.mu.RUnlock()
	assert.Equal(t, 3, bucket.walkCalls, "export should perform one streaming walk per bounded namespace")
	assert.Equal(t, []string{ownedKey}, bucket.opened, "only owner-filtered metadata may schedule object reads")
}
