package backup

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type noteMediaPutBucket struct {
	StorageBucket
	key         string
	data        []byte
	contentType string
	putErr      error
}

func (b *noteMediaPutBucket) PutObjectStream(_ context.Context, key string, r io.Reader, _ int64, contentType string) error {
	if b.putErr != nil {
		return b.putErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.key = key
	b.data = data
	b.contentType = contentType
	return nil
}

func TestPreparedNoteMediaSpoolAvoidsSecondArchiveReadAndOptimize(t *testing.T) {
	const oldKey = "notes/22c3a1e2-304d-441f-a525-713dc364bff1.png"
	var source bytes.Buffer
	imageData := image.NewRGBA(image.Rect(0, 0, 2, 2))
	imageData.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	require.NoError(t, png.Encode(&source, imageData))
	valid := zipReaderWithEntries(t, struct {
		name string
		body []byte
	}{name: "files/" + oldKey, body: source.Bytes()})
	snapshot := &Snapshot{Notes: []NoteRow{{BodyHTML: `<img src="/api/files/` + oldKey + `">`}}}

	prepared, err := prepareNoteMediaRestore(snapshot, valid)
	require.NoError(t, err)
	require.NotNil(t, prepared.spool)
	spoolName := prepared.spool.Name()
	defer prepared.cleanup()
	newKey := prepared.mapping[oldKey]
	require.NotEmpty(t, newKey)

	// If applyFiles re-opened and optimized the archive entry, this malformed
	// replacement would fail. Success proves publication reads the prepared spool.
	malformed := zipReaderWithEntries(t, struct {
		name string
		body []byte
	}{name: "files/" + oldKey, body: []byte("not an image")})
	bucket := &noteMediaPutBucket{}
	service := &Service{storage: bucket}
	mapping := idMapping{noteFiles: map[string]string{oldKey: newKey}}
	report, err := service.applyFiles(context.Background(), 1, malformed, mapping, ModeDuplicate, nil, prepared)
	require.NoError(t, err)
	assert.EqualValues(t, 1, report.Uploaded)
	assert.Equal(t, newKey, bucket.key)
	assert.Equal(t, "image/jpeg", bucket.contentType)
	assert.NotEmpty(t, bucket.data)

	prepared.cleanup()
	_, err = os.Stat(spoolName)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestApplyRestoreNoteObjectOptimizesOnLedgerResume(t *testing.T) {
	var source bytes.Buffer
	imageData := image.NewRGBA(image.Rect(0, 0, 2, 2))
	imageData.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	require.NoError(t, png.Encode(&source, imageData))
	zr := zipReaderWithEntries(t, struct {
		name string
		body []byte
	}{name: "files/notes/old.png", body: source.Bytes()})
	bucket := &noteMediaPutBucket{}
	service := &Service{storage: bucket}

	result, err := service.applyRestoreNoteObject(context.Background(), restoreFileWork{
		entry: zr.File[0], key: "notes/new.jpg", isNote: true,
	}, nil)

	require.NoError(t, err)
	assert.EqualValues(t, 1, result.uploaded)
	assert.Equal(t, "notes/new.jpg", bucket.key)
	assert.Equal(t, "image/jpeg", bucket.contentType)
	assert.Equal(t, "image/jpeg", http.DetectContentType(bucket.data))
}

func TestApplyRestoreNoteObjectRejectsInvalidMediaOnLedgerResume(t *testing.T) {
	zr := zipReaderWithEntries(t, struct {
		name string
		body []byte
	}{name: "files/notes/old.png", body: []byte("not an image")})
	bucket := &noteMediaPutBucket{}
	service := &Service{storage: bucket}

	_, err := service.applyRestoreNoteObject(context.Background(), restoreFileWork{
		entry: zr.File[0], key: "notes/new.jpg", isNote: true,
	}, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid note media")
	assert.Empty(t, bucket.key)
}

func TestRestoreSingleObjectStagesPropagateUploadFailure(t *testing.T) {
	sentinel := errors.New("put failed")
	bucket := &noteMediaPutBucket{putErr: sentinel}
	service := &Service{storage: bucket}
	spool, err := os.CreateTemp(t.TempDir(), "prepared-note-*.bin")
	require.NoError(t, err)
	_, err = spool.Write([]byte("x"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = spool.Close() })

	_, err = service.applyRestoreNoteObject(context.Background(), restoreFileWork{
		key: "notes/new.jpg", isNote: true, hasPrepared: true,
		preparedFile: preparedNoteMediaFile{size: 1, contentType: "image/jpeg"},
	}, &preparedNoteMediaRestore{spool: spool})
	assert.ErrorIs(t, err, sentinel)

	zr := zipReaderWithEntries(t, struct {
		name string
		body []byte
	}{name: "files/images/1.jpg", body: []byte("image")})
	_, err = service.applyRestoreArchiveObject(context.Background(), restoreFileWork{
		entry: zr.File[0], key: "images/2.jpg",
	})
	assert.ErrorIs(t, err, sentinel)
}
