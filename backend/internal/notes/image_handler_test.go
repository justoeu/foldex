package notes

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

type fakeUploader struct {
	uploaded map[string][]byte
	failNext bool
}

func newFakeUploader() *fakeUploader { return &fakeUploader{uploaded: map[string][]byte{}} }

func (f *fakeUploader) Upload(_ context.Context, key string, data []byte, _ string) error {
	if f.failNext {
		return assert.AnError
	}
	f.uploaded[key] = data
	return nil
}
func (f *fakeUploader) GetObject(_ context.Context, key string) ([]byte, string, error) {
	return f.uploaded[key], "image/jpeg", nil
}
func (f *fakeUploader) DeleteObject(_ context.Context, key string) error {
	delete(f.uploaded, key)
	return nil
}

type uploadOnly struct{}

func (uploadOnly) Upload(context.Context, string, []byte, string) error { return nil }

type fakeMediaLeases struct {
	owned   map[string]authctx.UserID
	failAdd bool
}

func newFakeMediaLeases() *fakeMediaLeases {
	return &fakeMediaLeases{owned: map[string]authctx.UserID{}}
}

func (f *fakeMediaLeases) RegisterMediaLease(_ context.Context, uid authctx.UserID, key string) error {
	if f.failAdd {
		return assert.AnError
	}
	f.owned[key] = uid
	return nil
}

func (f *fakeMediaLeases) ForgetMediaLease(_ context.Context, uid authctx.UserID, key string) error {
	if f.owned[key] == uid {
		delete(f.owned, key)
	}
	return nil
}

func multipartImageRequest(t *testing.T, fieldName, filename string, data []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if fieldName != "" {
		part, err := w.CreateFormFile(fieldName, filename)
		require.NoError(t, err)
		_, err = part.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	req := httptest.NewRequest(http.MethodPost, "/images", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{UserID: 7}))
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestImageHandler_Upload_Success(t *testing.T) {
	up := newFakeUploader()
	leases := newFakeMediaLeases()
	h := NewImageHandler(up, leases, discardLogger())

	req := multipartImageRequest(t, "image", "shot.png", realPNG(t, 80, 60))
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Regexp(t, `^/api/files/notes/[A-Za-z0-9-]+\.jpg$`, body["url"])
	assert.Len(t, up.uploaded, 1)
	assert.Equal(t, authctx.UserID(7), leases.owned[strings.TrimPrefix(body["url"], "/api/files/")])
}

func TestNoteImageUploadReencodesAsJPEG(t *testing.T) {
	up := newFakeUploader()
	h := NewImageHandler(up, newFakeMediaLeases(), discardLogger())

	src := realPNG(t, 200, 150)
	req := multipartImageRequest(t, "image", "photo.png", src)
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Len(t, up.uploaded, 1)
	for key, data := range up.uploaded {
		assert.True(t, strings.HasSuffix(key, ".jpg"), "stored key %s", key)
		assert.Equal(t, "image/jpeg", http.DetectContentType(data))
		assert.NotEqual(t, src, data, "PNG must be re-encoded, not stored raw")
	}
}

func TestNoteImageUploadRejectsDecodeBomb(t *testing.T) {
	up := newFakeUploader()
	leases := newFakeMediaLeases()
	h := NewImageHandler(up, leases, discardLogger())

	req := multipartImageRequest(t, "image", "bomb.png", decodeBombPNG(t))
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	errBlock, _ := body["error"].(map[string]any)
	assert.Equal(t, "invalid_image", errBlock["code"])
	assert.Empty(t, up.uploaded, "decode bomb must not be written to object storage")
	assert.Empty(t, leases.owned, "decode bomb must not register a media lease")
}

func TestImageHandler_Upload_MissingField(t *testing.T) {
	up := newFakeUploader()
	h := NewImageHandler(up, newFakeMediaLeases(), discardLogger())

	req := multipartImageRequest(t, "", "", nil)
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Empty(t, up.uploaded)
}

func TestImageHandler_Upload_RejectsNonImageMIME(t *testing.T) {
	up := newFakeUploader()
	h := NewImageHandler(up, newFakeMediaLeases(), discardLogger())

	req := multipartImageRequest(t, "image", "evil.html", []byte("<script>alert(1)</script>"))
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	assert.Equal(t, http.StatusUnsupportedMediaType, rr.Code)
	assert.Empty(t, up.uploaded, "rejected MIME must never reach storage")
}

func TestImageHandler_Upload_EmptyFile(t *testing.T) {
	up := newFakeUploader()
	h := NewImageHandler(up, newFakeMediaLeases(), discardLogger())

	req := multipartImageRequest(t, "image", "empty.png", []byte{})
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestImageHandler_Upload_StorageFailure(t *testing.T) {
	up := newFakeUploader()
	up.failNext = true
	leases := newFakeMediaLeases()
	h := NewImageHandler(up, leases, discardLogger())

	req := multipartImageRequest(t, "image", "shot.png", realPNG(t, 40, 30))
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Empty(t, leases.owned, "failed storage writes must release the pending ownership row")
}

func TestImageHandler_Upload_LeaseFailureStoresNothing(t *testing.T) {
	up := newFakeUploader()
	leases := newFakeMediaLeases()
	leases.failAdd = true
	h := NewImageHandler(up, leases, discardLogger())

	req := multipartImageRequest(t, "image", "shot.png", realPNG(t, 40, 30))
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Empty(t, up.uploaded)
}

func TestNewImageHandlerAcceptsUploadOnlyStorage(t *testing.T) {
	h := NewImageHandler(uploadOnly{}, newFakeMediaLeases(), discardLogger())
	assert.NotNil(t, h)
}

func realPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 40, G: 80, B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// decodeBombPNG is a tiny PNG whose IHDR declares 8000×8000 (64 MP, over the
// 50 MP cap) while the IDAT stays a 1×1 pixel. Storing it would let any
// decoder OOM; Optimize must refuse before image.Decode allocates.
func decodeBombPNG(t *testing.T) []byte {
	t.Helper()
	bomb := append([]byte(nil), realPNG(t, 1, 1)...)
	require.GreaterOrEqual(t, len(bomb), 33)
	binary.BigEndian.PutUint32(bomb[16:20], 8_000)
	binary.BigEndian.PutUint32(bomb[20:24], 8_000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	return bomb
}
