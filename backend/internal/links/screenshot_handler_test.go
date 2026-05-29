package links

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pngHeader is the 8-byte magic prefix every PNG starts with. http.DetectContentType
// looks at the first 512 bytes; this is enough to be classified as image/png.
var pngHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

// fakePNG returns bytes that http.DetectContentType classifies as image/png
// but that are NOT a decodable PNG. Used to test fallback paths.
func fakePNG(payload string) []byte {
	out := make([]byte, 0, len(pngHeader)+len(payload))
	out = append(out, pngHeader...)
	out = append(out, []byte(payload)...)
	return out
}

// realPNG returns a decodable solid-color PNG at the given dimensions.
func realPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 255), G: uint8(y % 255), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// --- fakes ---

type fakeScreenshotter struct {
	png []byte
	err error
}

func (f *fakeScreenshotter) Capture(_ context.Context, _ string) ([]byte, error) {
	return f.png, f.err
}

type uploadOp struct {
	key         string
	contentType string
	bytes       []byte
}

type fakeUploader struct {
	uploaded map[string][]byte
	ops      []uploadOp // ordered call log
	deleted  []string   // ordered DeleteObject call log
	err      error
	getErr   error
}

func newFakeUploader() *fakeUploader {
	return &fakeUploader{uploaded: map[string][]byte{}}
}

func (f *fakeUploader) Upload(_ context.Context, key string, data []byte, ct string) error {
	if f.err != nil {
		return f.err
	}
	f.uploaded[key] = data
	f.ops = append(f.ops, uploadOp{key: key, contentType: ct, bytes: data})
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

func (f *fakeUploader) DeleteObject(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	// fakeUploader treats every delete as success (matches the production
	// idempotent behaviour where NoSuchKey is swallowed).
	delete(f.uploaded, key)
	return nil
}

type fakeRepo struct {
	links       map[int64]Link
	updatedURL  map[int64]string
	clearedIDs  []int64
	getErr      error
	updateErr   error
	clearErr    error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{links: map[int64]Link{}, updatedURL: map[int64]string{}}
}

func (f *fakeRepo) Get(_ context.Context, id int64) (Link, error) {
	if f.getErr != nil {
		return Link{}, f.getErr
	}
	l, ok := f.links[id]
	if !ok {
		return Link{}, errors.New("not found")
	}
	return l, nil
}

func (f *fakeRepo) UpdateOGImage(_ context.Context, id int64, imageURL string) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updatedURL[id] = imageURL
	return nil
}

func (f *fakeRepo) ClearOGImage(_ context.Context, id int64) error {
	if f.clearErr != nil {
		return f.clearErr
	}
	f.clearedIDs = append(f.clearedIDs, id)
	return nil
}

// --- helpers ---

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// allowAllPolicy is a test-only URLPolicy that approves every URL. Production
// uses preview.IsPublicURL, but here we want to exercise the handler logic
// without going through real DNS — the SSRF gate itself is tested separately
// via TestCaptureAndStore_Rejects*.
func allowAllPolicy(_ context.Context, _ string) bool { return true }

// buildRouter mounts the real ScreenshotHandler methods backed by fakes —
// no inlined closures, so production code paths (including imageopt) run.
func buildRouter(t *testing.T, sc Screenshotter, up Uploader, repo screenshotRepo) (*chi.Mux, *fakeUploader, *fakeRepo) {
	t.Helper()
	fakeUp, _ := up.(*fakeUploader)
	fakeRp, _ := repo.(*fakeRepo)

	sh := &ScreenshotHandler{
		repo:          repo,
		screenshotter: sc,
		storage:       up,
		urlPolicy:     allowAllPolicy,
		logger:        newTestLogger(),
	}

	r := chi.NewRouter()
	r.Route("/api", func(api chi.Router) {
		api.Post("/links/{id}/screenshot", sh.CaptureAndStore)
		api.Post("/links/{id}/image", sh.UploadImage)
		api.Get("/files/*", sh.ProxyFile)
	})
	return r, fakeUp, fakeRp
}

// --- unit tests for ScreenshotHandler ---

func TestCaptureAndStore_Success(t *testing.T) {
	src := realPNG(t, 1500, 900)
	sc := &fakeScreenshotter{png: src}
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "https://example.com"}
	r, fakeUp, _ := buildRouter(t, sc, up, repo)

	req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, "/api/files/screenshots/1.jpg", body["url"])

	// Stored object is a real JPEG with the long side downscaled to 1024.
	// Size-vs-source isn't asserted: synthetic test PNGs compress better
	// with DEFLATE than JPEG. The production case (real screenshots /
	// photos) is exercised via integration tests.
	stored, ok := fakeUp.uploaded["screenshots/1.jpg"]
	require.True(t, ok, "expected screenshots/1.jpg in uploaded map")
	assert.Equal(t, "image/jpeg", http.DetectContentType(stored))
	cfg, _, err := image.DecodeConfig(bytes.NewReader(stored))
	require.NoError(t, err)
	assert.Equal(t, 1024, cfg.Width)
	// No legacy .png left in the map (would only matter if seeded — assert
	// the cleanup call happened).
	assert.Contains(t, fakeUp.deleted, "screenshots/1.png")
}

func TestCaptureAndStore_ScreenshotFails(t *testing.T) {
	sc := &fakeScreenshotter{err: errors.New("chromium crashed")}
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "https://example.com"}
	r, _, _ := buildRouter(t, sc, up, repo)

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
	sc := &fakeScreenshotter{png: realPNG(t, 300, 200)}
	up := newFakeUploader()
	up.err = errors.New("minio down")
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "https://example.com"}
	r, _, _ := buildRouter(t, sc, up, repo)

	req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	errBlock, _ := body["error"].(map[string]any)
	assert.Equal(t, "upload_failed", errBlock["code"])
}

// TestCaptureAndStore_RejectsNonHTTPScheme locks the H1 fix part 1: Chrome
// happily navigates to file:// — without scheme validation, a single API call
// turns into a local-file read primitive.
func TestCaptureAndStore_RejectsNonHTTPScheme(t *testing.T) {
	for _, badURL := range []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"data:text/html,<script>",
		"ftp://intranet/x",
	} {
		t.Run(badURL, func(t *testing.T) {
			sc := &fakeScreenshotter{png: realPNG(t, 50, 50)}
			up := newFakeUploader()
			repo := newFakeRepo()
			repo.links[1] = Link{ID: 1, URL: badURL}
			r, _, _ := buildRouter(t, sc, up, repo)

			req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusBadRequest, w.Code, "must reject non-http(s) target")
			var body map[string]any
			require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
			errBlock, _ := body["error"].(map[string]any)
			assert.Equal(t, "invalid_scheme", errBlock["code"])
			assert.Empty(t, up.uploaded, "no upload should happen for rejected URL")
		})
	}
}

// TestCaptureAndStore_RejectsNonPublicTarget locks the H1 fix part 2: even
// when the scheme is http(s), private/loopback/IMDS hosts must be refused.
// Also captures the URL the policy received — a future refactor that
// sanitized/rewrote link.URL before the policy check would silently weaken
// the SSRF gate; this asserts the policy sees the exact stored URL.
func TestCaptureAndStore_RejectsNonPublicTarget(t *testing.T) {
	sc := &fakeScreenshotter{png: realPNG(t, 50, 50)}
	up := newFakeUploader()
	repo := newFakeRepo()
	const storedURL = "http://169.254.169.254/latest/meta-data/"
	repo.links[1] = Link{ID: 1, URL: storedURL}

	var captured []string
	denyPolicy := func(_ context.Context, u string) bool {
		captured = append(captured, u)
		return false
	}
	sh := &ScreenshotHandler{
		repo:          repo,
		screenshotter: sc,
		storage:       up,
		urlPolicy:     denyPolicy,
		logger:        newTestLogger(),
	}
	r := chi.NewRouter()
	r.Post("/api/links/{id}/screenshot", sh.CaptureAndStore)

	req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	errBlock, _ := body["error"].(map[string]any)
	assert.Equal(t, "private_target", errBlock["code"])
	assert.Empty(t, up.uploaded)
	require.Len(t, captured, 1, "policy must be called exactly once")
	assert.Equal(t, storedURL, captured[0], "policy must receive the canonical link.URL")
}

// TestCaptureAndStore_NilPolicyFailsClosed locks the H1 invariant: a missing
// policy must not silently bypass the SSRF gate. Misconfiguration (forgotten
// wiring in main.go) returns 500 policy_unconfigured — distinct from the 400
// private_target a real SSRF attempt produces, so ops can tell them apart.
// Router boot panics on this same condition; the handler check is the
// defense-in-depth layer.
func TestCaptureAndStore_NilPolicyFailsClosed(t *testing.T) {
	sc := &fakeScreenshotter{png: realPNG(t, 50, 50)}
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "https://example.com"}

	sh := &ScreenshotHandler{
		repo:          repo,
		screenshotter: sc,
		storage:       up,
		urlPolicy:     nil, // simulates a misconfigured deploy
		logger:        newTestLogger(),
	}
	r := chi.NewRouter()
	r.Post("/api/links/{id}/screenshot", sh.CaptureAndStore)

	req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "nil policy must deny with a config error")
	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	errBlock, _ := body["error"].(map[string]any)
	assert.Equal(t, "policy_unconfigured", errBlock["code"])
	assert.Empty(t, up.uploaded)
}

func TestCaptureAndStore_InvalidID(t *testing.T) {
	sc := &fakeScreenshotter{png: realPNG(t, 50, 50)}
	up := newFakeUploader()
	repo := newFakeRepo()
	r, _, _ := buildRouter(t, sc, up, repo)

	req := httptest.NewRequest(http.MethodPost, "/api/links/abc/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCaptureAndStore_OptimizeFailureFallsBackToPNG(t *testing.T) {
	// fakePNG sniffs as image/png but isn't a decodable PNG — Optimize
	// returns ErrDecode, handler falls back to storing the raw bytes under
	// the legacy .png extension.
	bad := fakePNG("not really a png")
	sc := &fakeScreenshotter{png: bad}
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[7] = Link{ID: 7, URL: "https://example.com"}
	r, fakeUp, _ := buildRouter(t, sc, up, repo)

	req := httptest.NewRequest(http.MethodPost, "/api/links/7/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, bad, fakeUp.uploaded["screenshots/7.png"])
	require.Len(t, fakeUp.ops, 1)
	assert.Equal(t, "image/png", fakeUp.ops[0].contentType)
}

func TestProxyFile_Success(t *testing.T) {
	sc := &fakeScreenshotter{}
	up := newFakeUploader()
	up.uploaded["screenshots/42.png"] = fakePNG("IMG_CONTENT")
	repo := newFakeRepo()
	r, _, _ := buildRouter(t, sc, up, repo)

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
	repo := newFakeRepo()
	r, _, _ := buildRouter(t, sc, up, repo)

	req := httptest.NewRequest(http.MethodGet, "/api/files/screenshots/999.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- ProxyFile hardening tests ---

func TestProxyFile_RejectsTraversalKey(t *testing.T) {
	sc := &fakeScreenshotter{}
	up := newFakeUploader()
	repo := newFakeRepo()
	r, _, _ := buildRouter(t, sc, up, repo)

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
	repo := newFakeRepo()
	r, _, _ := buildRouter(t, sc, up, repo)

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

// --- UploadImage tests ---

func buildMultipart(t *testing.T, id int64, field, filename, declaredCT string, body []byte) (*http.Request, string) {
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
	req := httptest.NewRequest(http.MethodPost, "/api/links/"+strconv.FormatInt(id, 10)+"/image", buf)
	return req, mw.FormDataContentType()
}

func TestUploadImage_RejectsHTMLDisguisedAsPNG(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	r, _, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)
	// Client lies — declares image/png but body is plain HTML.
	req, ct := buildMultipart(t, 1, "image", "evil.png", "image/png", []byte("<html><script>alert(1)</script></html>"))
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnsupportedMediaType, w.Code)
	assert.Empty(t, up.uploaded, "must not store HTML-disguised-as-image")
}

func TestUploadImage_RejectsEmptyFile(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	r, _, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)
	req, ct := buildMultipart(t, 1, "image", "empty.png", "image/png", []byte{})
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, up.uploaded)
}

func TestUploadImage_RejectsMissingField(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	r, _, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)
	// Form field is wrong name.
	req, ct := buildMultipart(t, 1, "other", "x.png", "image/png", fakePNG("data"))
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUploadImage_OptimizesPNGToJPEG(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	r, fakeUp, fakeRp := buildRouter(t, &fakeScreenshotter{}, up, repo)

	src := realPNG(t, 1500, 1000)
	req, ct := buildMultipart(t, 42, "image", "large.png", "image/png", src)
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, "/api/files/images/42.jpg", body["url"])
	assert.Equal(t, "/api/files/images/42.jpg", fakeRp.updatedURL[42])

	require.Len(t, fakeUp.ops, 1)
	assert.Equal(t, "images/42.jpg", fakeUp.ops[0].key)
	assert.Equal(t, "image/jpeg", fakeUp.ops[0].contentType)
	assert.Equal(t, "image/jpeg", http.DetectContentType(fakeUp.ops[0].bytes))

	cfg, _, err := image.DecodeConfig(bytes.NewReader(fakeUp.ops[0].bytes))
	require.NoError(t, err)
	assert.Equal(t, 1024, cfg.Width)
}

func TestUploadImage_PurgesLegacyExtensions(t *testing.T) {
	up := newFakeUploader()
	// Seed a stale .png and .webp for link 5 — they must be deleted when
	// the new upload writes .jpg.
	up.uploaded["images/5.png"] = []byte("old png")
	up.uploaded["images/5.webp"] = []byte("old webp")
	repo := newFakeRepo()
	r, fakeUp, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	src := realPNG(t, 800, 600)
	req, ct := buildMultipart(t, 5, "image", "new.png", "image/png", src)
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	assert.Contains(t, fakeUp.deleted, "images/5.png")
	assert.Contains(t, fakeUp.deleted, "images/5.webp")
	assert.Contains(t, fakeUp.deleted, "images/5.gif")
	assert.NotContains(t, fakeUp.deleted, "images/5.jpg", "must not delete the key we are about to write")
	_, oldStillThere := fakeUp.uploaded["images/5.png"]
	assert.False(t, oldStillThere, "fakeUploader DeleteObject should have removed the stale .png")
}

func TestUploadImage_OptimizeFailureStoresOriginal(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	r, fakeUp, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	// PNG-sniff header but body isn't a real PNG — Optimize returns
	// ErrDecode, handler falls back to storing original under .png.
	bad := fakePNG("nope")
	req, ct := buildMultipart(t, 9, "image", "broken.png", "image/png", bad)
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	require.Len(t, fakeUp.ops, 1)
	assert.Equal(t, "images/9.png", fakeUp.ops[0].key)
	assert.Equal(t, "image/png", fakeUp.ops[0].contentType)
	assert.Equal(t, bad, fakeUp.ops[0].bytes)
}

// TestUploadImage_Rejects5MBPlus locks the H4 follow-on: the cap dropped from
// 20 MB to 5 MiB. A 5 MiB+1 body must trip MaxBytesReader and return 400
// invalid_multipart (the multipart parser surfaces the size cap as a parse
// failure — the handler can't distinguish from a malformed body without an
// extra syscall, so we accept the broader code).
func TestUploadImage_Rejects5MBPlus(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	r, _, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	// 5 MiB + 64 KiB: comfortably over the cap once multipart framing is
	// added. Body content doesn't need to be a valid PNG — MaxBytesReader
	// fires first.
	const tooBig = (5 << 20) + (64 << 10)
	payload := make([]byte, tooBig)
	req, ct := buildMultipart(t, 1, "image", "big.png", "image/png", payload)
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code, "request body over 5 MiB must be refused")
	assert.Empty(t, up.uploaded)
}

func TestUploadImage_UploadFails(t *testing.T) {
	up := newFakeUploader()
	up.err = errors.New("minio down")
	repo := newFakeRepo()
	r, _, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	req, ct := buildMultipart(t, 3, "image", "x.png", "image/png", realPNG(t, 100, 100))
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestUploadImage_RepoUpdateFails(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.updateErr = errors.New("db down")
	r, _, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	req, ct := buildMultipart(t, 3, "image", "x.png", "image/png", realPNG(t, 100, 100))
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- DeleteImage tests ---

func TestDeleteImage_Success(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	sh := &ScreenshotHandler{repo: repo, storage: up, logger: newTestLogger()}
	r := chi.NewRouter()
	r.Delete("/api/links/{id}/image", sh.DeleteImage)

	req := httptest.NewRequest(http.MethodDelete, "/api/links/8/image", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, []int64{8}, repo.clearedIDs)
}

// --- Construction sanity ---

func TestNewScreenshotHandler(t *testing.T) {
	sc := &fakeScreenshotter{}
	up := newFakeUploader()
	logger := newTestLogger()
	sh := NewScreenshotHandler(nil, sc, up, allowAllPolicy, logger)
	require.NotNil(t, sh)
	assert.Equal(t, sc, sh.screenshotter)
	assert.Equal(t, up, sh.storage)
}
