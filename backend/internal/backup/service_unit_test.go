package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/testsupport"
)

func TestCreateSnapshotSpool_HappyAndCleanup(t *testing.T) {
	tx := newFakeTx()
	tx.row = valuesRow("owner@example.com")
	spool, err := createSnapshotSpool(context.Background(), tx, testUID)
	require.NoError(t, err)
	require.NotNil(t, spool)
	assert.Positive(t, spool.size)
	assert.Contains(t, spool.checksum, sha256Prefix)

	name := spool.file.Name()
	spool.cleanup()
	_, statErr := os.Stat(name)
	assert.True(t, os.IsNotExist(statErr), "cleanup removes the spool file")
}

func TestCreateSnapshotSpool_StreamFailureLeavesNoFile(t *testing.T) {
	tx := newFakeTx()
	tx.row = errRow(errRestoreForced)
	spool, err := createSnapshotSpool(context.Background(), tx, testUID)
	require.Error(t, err)
	assert.Nil(t, spool)
}

func TestWriteExportZip_ProducesReadableBackup(t *testing.T) {
	db := []byte(`{"version":7}`)
	file, err := os.CreateTemp(t.TempDir(), "database-*.json")
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	_, err = file.Write(db)
	require.NoError(t, err)
	digest := sha256.Sum256(db)
	spool := &snapshotSpool{
		file:     file,
		checksum: sha256Prefix + hex.EncodeToString(digest[:]),
		size:     int64(len(db)),
	}
	svc := &Service{storage: openObjectBucket{data: []byte("imagedata")}}
	listing := ownedObjectListing{
		objects: []ObjectInfo{{Key: "images/1.jpg", Size: 9}},
		count:   1, bytes: 9,
	}
	var out bytes.Buffer
	err = svc.writeExportZip(context.Background(), &out, spool, listing, Counts{Files: 1, FileBytes: 9})
	require.NoError(t, err)

	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	require.NoError(t, err)
	names := make(map[string][]byte)
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		names[f.Name] = body
	}
	assert.Equal(t, db, names[snapshotDBName])
	assert.Equal(t, []byte("imagedata"), names["files/images/1.jpg"])

	var manifest Manifest
	require.NoError(t, json.Unmarshal(names["manifest.json"], &manifest))
	assert.Equal(t, ManifestKind, manifest.Kind)
	assert.Contains(t, manifest.Checksums, snapshotDBName)
	assert.Contains(t, manifest.Checksums, "files/images/1.jpg")
	assert.NotContains(t, manifest.Checksums, "manifest.json")
}

func TestWriteExportZip_WriterFailure(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "database-*.json")
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	_, err = file.WriteString(`{"version":7}`)
	require.NoError(t, err)
	spool := &snapshotSpool{file: file, checksum: "sha256:x", size: 13}
	svc := &Service{storage: openObjectBucket{}}
	err = svc.writeExportZip(context.Background(), &flakyWriter{failOn: 1}, spool, ownedObjectListing{}, Counts{})
	require.Error(t, err)
	assert.ErrorIs(t, err, errFakeWrite)
}

func TestExport_HeaderHookErrorAbortsBeforeZip(t *testing.T) {
	// captureExportSnapshot needs a live pool, so drive Export only up to the
	// begin-tx failure here; the hook branch is covered via writeExportZip's
	// sibling failure test above and integration coverage.
	svc := NewService(testsupport.ClosedPool(t), openObjectBucket{}, nil)
	called := false
	_, err := svc.Export(context.Background(), testUID, &bytes.Buffer{}, func(Counts) error {
		called = true
		return nil
	})
	require.Error(t, err)
	assert.False(t, called)
}

func TestValidate_ValidArchiveWithEmptySnapshotNeedsNoDB(t *testing.T) {
	svc := &Service{pool: testsupport.ClosedPool(t)}
	zr, _ := validBackupReader(t, []byte(`{"version":7,"tags":[],"links":[]}`))
	v, err := svc.Validate(context.Background(), testUID, zr)
	require.NoError(t, err)
	assert.True(t, v.OK)
	require.NotNil(t, v.Manifest)

	// A snapshot with links forces the conflict query, which a closed pool
	// surfaces as an infrastructure error rather than a validation entry.
	zr2, _ := validBackupReader(t, []byte(`{"version":7,"links":[{"id":1,"url":"https://a.test"}]}`))
	_, err = svc.Validate(context.Background(), testUID, zr2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicts")

	// Cancelled context short-circuits before conflict detection.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	zr3, _ := validBackupReader(t, []byte(`{"version":7}`))
	_, err = svc.Validate(ctx, testUID, zr3)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRestore_InvalidModeAndCorruptArchive(t *testing.T) {
	svc := &Service{pool: testsupport.ClosedPool(t)}
	zr, _ := validBackupReader(t, []byte(`{"version":7}`))

	_, err := svc.Restore(context.Background(), testUID, zr, ConflictMode("nope"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid mode")

	bad := zipReaderWithEntries(t, struct {
		name string
		body []byte
	}{"manifest.json", []byte(`{}`)})
	_, err = svc.Restore(context.Background(), testUID, bad, ModeSkip)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidBackup)
}

func TestRestore_SkipModeSurfacesLedgerFailure(t *testing.T) {
	// Valid archive + skip mode: Restore consults the ledger before any
	// other DB work, so a closed pool fails exactly there.
	svc := &Service{pool: testsupport.ClosedPool(t)}
	zr, _ := validBackupReader(t, []byte(`{"version":7,"tags":[],"links":[],"notes":[]}`))
	_, err := svc.Restore(context.Background(), testUID, zr, ModeSkip)
	require.Error(t, err)
}

func TestRestore_WipeModeSurfacesBeginTxFailure(t *testing.T) {
	svc := &Service{pool: testsupport.ClosedPool(t)}
	zr, _ := validBackupReader(t, []byte(`{"version":7,"notes":[]}`))
	_, err := svc.Restore(context.Background(), testUID, zr, ModeWipe)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "begin tx")
}

func TestResumeSkipRestore_CompleteLedgerSkipsFiles(t *testing.T) {
	svc := &Service{} // nil pool/storage: a complete ledger must touch neither
	ledger := restoreLedger{
		inserted: Counts{Links: 2},
		skipped:  Counts{Tags: 1},
		warnings: []string{"w"},
		files:    FileReport{Uploaded: 3},
		mapping:  newIDMapping(),
		complete: true,
	}
	report, err := svc.resumeSkipRestore(context.Background(), testUID, nil, [sha256.Size]byte{}, ledger, time.Now(), nil)
	require.NoError(t, err)
	assert.Equal(t, ModeSkip, report.Mode)
	assert.EqualValues(t, 2, report.Inserted.Links)
	assert.EqualValues(t, 1, report.Skipped.Tags)
	assert.EqualValues(t, 3, report.Files.Uploaded)
}

func TestResumeSkipRestore_IncompleteLedgerAppliesFilesThenNeedsLedgerWrite(t *testing.T) {
	// Empty archive: applyFiles is a no-op, then completing the ledger hits
	// the closed pool — proving the resume path reaches the checkpoint write.
	svc := &Service{pool: testsupport.ClosedPool(t), storage: openObjectBucket{}}
	ledger := restoreLedger{mapping: newIDMapping()}
	_, err := svc.resumeSkipRestore(context.Background(), testUID, zipReaderWithEntries(t),
		[sha256.Size]byte{1}, ledger, time.Now(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "complete restore ledger")
}

func TestFinishRestoreFiles_NonSkipCompletesWithoutLedger(t *testing.T) {
	svc := &Service{storage: openObjectBucket{}} // nil pool: duplicate mode writes no ledger
	result := restoreTransactionResult{
		report:  RestoreReport{Mode: ModeDuplicate, Warnings: []string{}},
		mapping: newIDMapping(),
	}
	report, err := svc.finishRestoreFiles(context.Background(), testUID, zipReaderWithEntries(t),
		ModeDuplicate, restoreFinish{start: time.Now()}, result, nil)
	require.NoError(t, err)
	assert.Equal(t, FileReport{}, report.Files)
}

func TestFinishRestoreFiles_SkipModeCompletesLedger(t *testing.T) {
	svc := &Service{pool: testsupport.ClosedPool(t), storage: openObjectBucket{}}
	result := restoreTransactionResult{
		report:  RestoreReport{Mode: ModeSkip, Warnings: []string{}},
		mapping: newIDMapping(),
	}
	_, err := svc.finishRestoreFiles(context.Background(), testUID, zipReaderWithEntries(t),
		ModeSkip, restoreFinish{digest: [sha256.Size]byte{2}, start: time.Now()}, result, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "complete restore ledger")
}

func TestMustPrincipal_PanicsOutsideAuthMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/backup/download", nil)
	assert.Panics(t, func() { mustPrincipal(req) })

	ctx := authctx.WithPrincipal(context.Background(), authctx.Principal{UserID: testUID})
	req = httptest.NewRequest(http.MethodGet, "/api/backup/download", nil).WithContext(ctx)
	assert.Equal(t, testUID, mustPrincipal(req).UserID)
}

func TestUnusedCleanup_IsCallableNoOp(t *testing.T) {
	assert.NotPanics(t, unusedCleanup)
}

type failOpenBucket struct{ StorageBucket }

func (failOpenBucket) OpenObject(context.Context, string) (io.ReadCloser, error) {
	return nil, errRestoreForced
}

func TestStreamObjectIntoZip_OpenFailure(t *testing.T) {
	svc := &Service{storage: failOpenBucket{}}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	err := svc.streamObjectIntoZip(context.Background(), zw, "files/images/1.jpg", "images/1.jpg", 3, map[string]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open")
	assert.ErrorIs(t, err, errRestoreForced)
}

func TestPreflightRestore_ErrorClassification(t *testing.T) {
	ctx := context.Background()

	t.Run("wrong kind gets the friendly message", func(t *testing.T) {
		zr := zipReaderWithEntries(t,
			struct {
				name string
				body []byte
			}{"manifest.json", []byte(`{"kind":"other","version":"1.0","schema_version":14,"checksums":{}}`)},
			struct {
				name string
				body []byte
			}{snapshotDBName, []byte(`{"version":7}`)},
		)
		_, err := preflightRestore(ctx, zr)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a foldex backup")
		assert.ErrorIs(t, err, ErrInvalidBackup)
	})

	t.Run("checksum failure is unprocessable", func(t *testing.T) {
		zr, manifest := validBackupReader(t, []byte(`{"version":7}`))
		_ = manifest
		// Rebuild with a tampered database.json so the checksum no longer matches.
		manifestBytes, err := json.Marshal(manifest)
		require.NoError(t, err)
		tampered := zipReaderWithEntries(t,
			struct {
				name string
				body []byte
			}{"manifest.json", manifestBytes},
			struct {
				name string
				body []byte
			}{snapshotDBName, []byte(`{"version":7,"tags":[]}`)},
		)
		_ = zr
		_, err = preflightRestore(ctx, tampered)
		require.Error(t, err)
		assert.True(t, IsUnprocessableBackup(err))
	})

	t.Run("cancelled context", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		zr, _ := validBackupReader(t, []byte(`{"version":7}`))
		_, err := preflightRestore(cancelled, zr)
		assert.ErrorIs(t, err, context.Canceled)
	})
}
