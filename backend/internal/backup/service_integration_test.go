//go:build integration

package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backup"
	"foldex/internal/links"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

// stubBucket is the minimum StorageBucket the Service needs. Tests inject it
// directly rather than spinning up MinIO — backup.Service treats its storage
// as opaque, and the SHA-256 checksums + ZIP layout are what we care about.
type stubBucket struct {
	objs map[string][]byte
}

func newStubBucket() *stubBucket { return &stubBucket{objs: map[string][]byte{}} }

func (s *stubBucket) ListObjects(_ context.Context, prefix string) ([]backup.ObjectInfo, error) {
	out := []backup.ObjectInfo{}
	for k, v := range s.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, backup.ObjectInfo{Key: k, Size: int64(len(v))})
		}
	}
	return out, nil
}

func (s *stubBucket) OpenObject(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.objs[key])), nil
}

func (s *stubBucket) PutObjectStream(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.objs[key] = data
	return nil
}

func (s *stubBucket) ObjectExists(_ context.Context, key string) (bool, error) {
	_, ok := s.objs[key]
	return ok, nil
}

func (s *stubBucket) DeleteObjectsPrefix(_ context.Context, prefix string) error {
	for k := range s.objs {
		if strings.HasPrefix(k, prefix) {
			delete(s.objs, k)
		}
	}
	return nil
}

// TestService_ExportProducesValidZipWithManifest locks the §4 invariant:
// backup is a complete DB + MinIO snapshot, manifest is uncompressed Store,
// every entry has a SHA-256 checksum.
func TestService_ExportProducesValidZipWithManifest(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()

	// Seed: one tag, two links, two files.
	trepo := tags.NewRepository(pool)
	tag, err := trepo.Create(ctx, tags.CreateInput{Name: "work", Color: "#abc"})
	require.NoError(t, err)
	lrepo := links.NewRepository(pool)
	_, err = lrepo.Create(ctx, links.CreateInput{URL: "https://a", Title: "A", TagIDs: []int64{tag.ID}})
	require.NoError(t, err)
	_, err = lrepo.Create(ctx, links.CreateInput{URL: "https://b", Title: "B"})
	require.NoError(t, err)

	bucket := newStubBucket()
	bucket.objs["screenshots/1.jpg"] = []byte("img-1-bytes")
	bucket.objs["images/2.jpg"] = []byte("img-2-bytes")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := backup.NewService(pool, bucket, logger)

	var buf bytes.Buffer
	var callbackCounts backup.Counts
	rep, err := svc.Export(ctx, &buf, func(c backup.Counts) error {
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
	assert.Contains(t, files, "files/screenshots/1.jpg")
	assert.Contains(t, files, "files/images/2.jpg")
	assert.Equal(t, []byte("img-1-bytes"), files["files/screenshots/1.jpg"])
}

// TestService_ExportAbortsWhenCallbackErrors confirms a header hook that
// returns an error short-circuits the export — the handler can use this to
// refuse a request before flushing response bytes.
func TestService_ExportAbortsWhenCallbackErrors(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := backup.NewService(pool, newStubBucket(), logger)

	var buf bytes.Buffer
	_, err := svc.Export(ctx, &buf, func(_ backup.Counts) error {
		return io.ErrUnexpectedEOF // sentinel, anything non-nil works
	})
	require.Error(t, err)
	assert.Equal(t, 0, buf.Len(), "no zip body should be flushed when the callback aborts")
}
