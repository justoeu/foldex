package notes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pngSignature is enough for http.DetectContentType to sniff "image/png" —
// it doesn't need to be a fully decodable image, since imageopt.Optimize's
// decode failure is handled by a fallback path (stores the original bytes).
var pngSignature = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0}

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
	return req
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestImageHandler_Upload_Success(t *testing.T) {
	up := newFakeUploader()
	h := NewImageHandler(up, discardLogger())

	req := multipartImageRequest(t, "image", "shot.png", pngSignature)
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Regexp(t, `^/api/files/notes/[A-Za-z0-9-]+\.(png|jpg)$`, body["url"])
	assert.Len(t, up.uploaded, 1)
}

func TestImageHandler_Upload_MissingField(t *testing.T) {
	up := newFakeUploader()
	h := NewImageHandler(up, discardLogger())

	req := multipartImageRequest(t, "", "", nil)
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Empty(t, up.uploaded)
}

func TestImageHandler_Upload_RejectsNonImageMIME(t *testing.T) {
	up := newFakeUploader()
	h := NewImageHandler(up, discardLogger())

	req := multipartImageRequest(t, "image", "evil.html", []byte("<script>alert(1)</script>"))
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	assert.Equal(t, http.StatusUnsupportedMediaType, rr.Code)
	assert.Empty(t, up.uploaded, "rejected MIME must never reach storage")
}

func TestImageHandler_Upload_EmptyFile(t *testing.T) {
	up := newFakeUploader()
	h := NewImageHandler(up, discardLogger())

	req := multipartImageRequest(t, "image", "empty.png", []byte{})
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestImageHandler_Upload_StorageFailure(t *testing.T) {
	up := newFakeUploader()
	up.failNext = true
	h := NewImageHandler(up, discardLogger())

	req := multipartImageRequest(t, "image", "shot.png", pngSignature)
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestNewImageHandler(t *testing.T) {
	up := newFakeUploader()
	h := NewImageHandler(up, discardLogger())
	assert.NotNil(t, h)
}
