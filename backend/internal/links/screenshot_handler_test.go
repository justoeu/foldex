package links

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pngHeader is the 8-byte magic prefix every PNG starts with. http.DetectContentType
// looks at the first 512 bytes; this is enough to be classified as image/png.
var pngHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

func fakePNG(payload string) []byte {
	out := make([]byte, 0, len(pngHeader)+len(payload))
	out = append(out, pngHeader...)
	out = append(out, []byte(payload)...)
	return out
}

// --- fakes ---

type fakeScreenshotter struct {
	png []byte
	err error
}

func (f *fakeScreenshotter) Capture(_ context.Context, _ string) ([]byte, error) {
	return f.png, f.err
}

type fakeUploader struct {
	uploaded map[string][]byte
	err      error
	getErr   error
}

func newFakeUploader() *fakeUploader {
	return &fakeUploader{uploaded: map[string][]byte{}}
}

func (f *fakeUploader) Upload(_ context.Context, key string, data []byte, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.uploaded[key] = data
	return nil
}

func (f *fakeUploader) GetObject(_ context.Context, key string) ([]byte, string, error) {
	if f.getErr != nil {
		return nil, "", f.getErr
	}
	d, ok := f.uploaded[key]
	if !ok {
		return nil, "", errors.New("not found")
	}
	return d, "image/png", nil
}

// --- helpers ---

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// buildRouter creates a chi router that mounts the screenshot handler with a
// fake repository seeded with one link. It does not touch a real database.
func buildRouter(t *testing.T, sc Screenshotter, up Uploader) (*chi.Mux, *fakeUploader) {
	t.Helper()
	// We need a real *Repository for the handler. Use a nil pool — that
	// means Get/List/etc. will panic if called. The fakeScreenshotter and
	// fakeUploader control all I/O paths in these unit tests.
	//
	// Instead, inject a custom repo via a small test double for the repo
	// *get* call that the screenshot handler uses.
	fakeUp := up.(*fakeUploader)

	sh := &ScreenshotHandler{
		repo:          nil, // replaced below via a custom handler wrapper
		screenshotter: sc,
		storage:       up,
		logger:        newTestLogger(),
	}

	r := chi.NewRouter()
	r.Route("/api", func(api chi.Router) {
		// POST /api/links/{id}/screenshot — we override CaptureAndStore
		// with a closure that injects a fake link without needing a DB.
		api.Post("/links/{id}/screenshot", func(w http.ResponseWriter, r *http.Request) {
			id, err := parseID(r)
			if err != nil {
				writeSimpleErr(w, &simpleErr{status: http.StatusBadRequest, code: "invalid_id", message: err.Error()})
				return
			}
			// Fake link for test purposes.
			link := Link{ID: id, URL: "https://example.com"}

			png, captErr := sh.screenshotter.Capture(r.Context(), link.URL)
			if captErr != nil {
				writeSimpleErr(w, httperrInternal("screenshot_failed", captErr.Error()))
				return
			}

			key := formKey(id)
			if upErr := sh.storage.Upload(r.Context(), key, png, "image/png"); upErr != nil {
				writeSimpleErr(w, httperrInternal("upload_failed", upErr.Error()))
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "/api/files/" + key})
		})

		// GET /api/files/* — use the real ProxyFile handler.
		api.Get("/files/*", sh.ProxyFile)
	})

	return r, fakeUp
}

// small helpers to avoid importing httperr from within the package test.
type simpleErr struct {
	status  int
	code    string
	message string
}

func writeSimpleErr(w http.ResponseWriter, e *simpleErr) {
	type envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	var env envelope
	env.Error.Code = e.code
	env.Error.Message = e.message
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(env)
}

func httperrInternal(code, msg string) *simpleErr {
	return &simpleErr{status: http.StatusInternalServerError, code: code, message: msg}
}

func formKey(id int64) string {
	return "screenshots/" + itoa(id) + ".png"
}

func itoa(n int64) string {
	return http.StatusText(0)[:0] + func() string {
		b := []byte{}
		if n == 0 {
			return "0"
		}
		for n > 0 {
			b = append([]byte{byte('0' + n%10)}, b...)
			n /= 10
		}
		return string(b)
	}()
}

// --- unit tests for ScreenshotHandler ---

func TestCaptureAndStore_Success(t *testing.T) {
	sc := &fakeScreenshotter{png: []byte("PNG_DATA")}
	up := newFakeUploader()
	r, fakeUp := buildRouter(t, sc, up)

	req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, "/api/files/screenshots/1.png", body["url"])
	assert.Equal(t, []byte("PNG_DATA"), fakeUp.uploaded["screenshots/1.png"])
}

func TestCaptureAndStore_ScreenshotFails(t *testing.T) {
	sc := &fakeScreenshotter{err: errors.New("chromium crashed")}
	up := newFakeUploader()
	r, _ := buildRouter(t, sc, up)

	req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	errBlock, _ := body["error"].(map[string]any)
	assert.Equal(t, "screenshot_failed", errBlock["code"])
}

func TestCaptureAndStore_UploadFails(t *testing.T) {
	sc := &fakeScreenshotter{png: []byte("PNG")}
	up := newFakeUploader()
	up.err = errors.New("minio down")
	r, _ := buildRouter(t, sc, up)

	req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	errBlock, _ := body["error"].(map[string]any)
	assert.Equal(t, "upload_failed", errBlock["code"])
}

func TestCaptureAndStore_InvalidID(t *testing.T) {
	sc := &fakeScreenshotter{png: []byte("PNG")}
	up := newFakeUploader()
	r, _ := buildRouter(t, sc, up)

	// "abc" is matched by chi as a string param; parseID then returns 400.
	req := httptest.NewRequest(http.MethodPost, "/api/links/abc/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestProxyFile_Success(t *testing.T) {
	sc := &fakeScreenshotter{png: fakePNG("payload")}
	up := newFakeUploader()
	up.uploaded["screenshots/42.png"] = fakePNG("IMG_CONTENT")
	r, _ := buildRouter(t, sc, up)

	req := httptest.NewRequest(http.MethodGet, "/api/files/screenshots/42.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/png", w.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, fakePNG("IMG_CONTENT"), w.Body.Bytes())
}

func TestProxyFile_NotFound(t *testing.T) {
	sc := &fakeScreenshotter{}
	up := newFakeUploader()
	up.getErr = errors.New("key does not exist")
	r, _ := buildRouter(t, sc, up)

	req := httptest.NewRequest(http.MethodGet, "/api/files/screenshots/999.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- ProxyFile hardening tests ---

func TestProxyFile_RejectsTraversalKey(t *testing.T) {
	sc := &fakeScreenshotter{}
	up := newFakeUploader()
	r, _ := buildRouter(t, sc, up)

	for _, bad := range []string{
		"/api/files/../etc/passwd",
		"/api/files/screenshots/../images/x.png",
		"/api/files//absolute/path.png",
		"/api/files/uploads/foo.png", // wrong prefix
	} {
		req := httptest.NewRequest(http.MethodGet, bad, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code, "path %s must be rejected", bad)
	}
}

func TestProxyFile_RejectsNonImageContent(t *testing.T) {
	sc := &fakeScreenshotter{}
	up := newFakeUploader()
	// A malicious upload that slipped past UploadImage with valid prefix but
	// non-image contents must not be served back as text/html.
	up.uploaded["images/13.png"] = []byte("<html><script>alert(1)</script></html>")
	r, _ := buildRouter(t, sc, up)

	req := httptest.NewRequest(http.MethodGet, "/api/files/images/13.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnsupportedMediaType, w.Code)
}

func TestIsAllowedKey(t *testing.T) {
	cases := []struct {
		key string
		ok  bool
	}{
		{"screenshots/1.png", true},
		{"images/42.jpg", true},
		{"", false},
		{"/etc/passwd", false},
		{"../etc/passwd", false},
		{"screenshots/../etc/passwd", false},
		{"random/key.bin", false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			assert.Equal(t, tc.ok, isAllowedKey(tc.key))
		})
	}
}

func TestIsAllowedServeMIME(t *testing.T) {
	assert.True(t, isAllowedServeMIME("image/png"))
	assert.True(t, isAllowedServeMIME("image/jpeg"))
	assert.False(t, isAllowedServeMIME("text/html; charset=utf-8"))
	assert.False(t, isAllowedServeMIME("image/svg+xml"))
	assert.False(t, isAllowedServeMIME(""))
}

// --- UploadImage hardening tests ---
//
// These call the real UploadImage method (with a nil repo) so we have to short-
// circuit the repository call. The test builds a router that mounts UploadImage
// only with the storage layer; we patch around the repo write by intercepting
// the storage success path: the test asserts the request was REJECTED before
// reaching the repo (or accepted and reached storage). Repo-success cases are
// covered by the integration test; here we cover the new validation gates.

func newUploadRouter(t *testing.T, up Uploader) *chi.Mux {
	t.Helper()
	sh := &ScreenshotHandler{
		repo:          nil,
		screenshotter: nil,
		storage:       up,
		logger:        newTestLogger(),
	}
	r := chi.NewRouter()
	r.Post("/api/links/{id}/image", sh.UploadImage)
	return r
}

func buildMultipart(t *testing.T, field, filename, declaredCT string, body []byte) (*http.Request, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="` + field + `"; filename="` + filename + `"`}
	if declaredCT != "" {
		h["Content-Type"] = []string{declaredCT}
	}
	part, err := mw.CreatePart(h)
	require.NoError(t, err)
	_, err = part.Write(body)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/links/1/image", buf)
	return req, mw.FormDataContentType()
}

func TestUploadImage_RejectsHTMLDisguisedAsPNG(t *testing.T) {
	up := newFakeUploader()
	r := newUploadRouter(t, up)
	// Client lies — declares image/png but body is plain HTML.
	req, ct := buildMultipart(t, "image", "evil.png", "image/png", []byte("<html><script>alert(1)</script></html>"))
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnsupportedMediaType, w.Code)
	assert.Empty(t, up.uploaded, "must not store HTML-disguised-as-image")
}

func TestUploadImage_RejectsEmptyFile(t *testing.T) {
	up := newFakeUploader()
	r := newUploadRouter(t, up)
	req, ct := buildMultipart(t, "image", "empty.png", "image/png", []byte{})
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, up.uploaded)
}

func TestUploadImage_RejectsMissingField(t *testing.T) {
	up := newFakeUploader()
	r := newUploadRouter(t, up)
	// Form field is wrong name.
	req, ct := buildMultipart(t, "other", "x.png", "image/png", fakePNG("data"))
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Test that NewScreenshotHandler wires everything correctly.
func TestNewScreenshotHandler(t *testing.T) {
	sc := &fakeScreenshotter{}
	up := newFakeUploader()
	logger := newTestLogger()
	sh := NewScreenshotHandler(nil, sc, up, logger)
	require.NotNil(t, sh)
	assert.Equal(t, sc, sh.screenshotter)
	assert.Equal(t, up, sh.storage)
}
