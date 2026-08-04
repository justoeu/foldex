package links

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/storage"

	"foldex/internal/pkg/authctx"

	"foldex/internal/pkg/authctx/authctxtest"
	"foldex/internal/pkg/httperr"
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
	uploaded  map[string][]byte
	ops       []uploadOp // ordered call log
	deleted   []string   // ordered DeleteObject call log
	err       error
	getErr    error
	deleteErr error
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
	if f.deleteErr != nil {
		return f.deleteErr
	}
	// fakeUploader treats every delete as success (matches the production
	// idempotent behaviour where NoSuchKey is swallowed).
	delete(f.uploaded, key)
	return nil
}

// fakeRepo stands in for links.Repository. It ENFORCES ownership rather than
// ignoring the uid the handler passes: the real repository scopes every query by
// user_id and reports another user's row as not-found, so a fake that answered
// regardless of uid would let a handler drop the principal entirely and still
// go green. gotUID records what was actually passed, so a test can assert the
// handler forwarded the authenticated principal instead of a zero value.
type fakeRepo struct {
	links      map[int64]Link
	owners     map[int64]authctx.UserID // absent ⇒ owned by authctxtest.DefaultUser
	gotUID     []authctx.UserID
	updatedURL map[int64]string
	clearedIDs []int64
	getErr     error
	updateErr  error
	clearErr   error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		links:      map[int64]Link{},
		owners:     map[int64]authctx.UserID{},
		updatedURL: map[int64]string{},
	}
}

// ownedBy registers a link belonging to someone other than the default user.
func (f *fakeRepo) ownedBy(id int64, uid authctx.UserID, l Link) {
	f.links[id] = l
	f.owners[id] = uid
}

func (f *fakeRepo) ownerOf(id int64) authctx.UserID {
	if uid, ok := f.owners[id]; ok {
		return uid
	}
	return authctxtest.DefaultUser
}

// errNotFound mirrors what the scoped repository returns for a row that either
// does not exist or belongs to another user — the two are deliberately
// indistinguishable (CLAUDE.md §4). It is the same httperr.ErrNotFound the real
// Repository returns, so handler tests observe production's 404 rather than the
// 500 a bare error would produce.
var errNotFound = httperr.ErrNotFound

func (f *fakeRepo) Get(_ context.Context, uid authctx.UserID, id int64) (Link, error) {
	f.gotUID = append(f.gotUID, uid)
	if f.getErr != nil {
		return Link{}, f.getErr
	}
	l, ok := f.links[id]
	if !ok || f.ownerOf(id) != uid {
		return Link{}, errNotFound
	}
	return l, nil
}

func (f *fakeRepo) AssertOwned(_ context.Context, uid authctx.UserID, id int64) error {
	f.gotUID = append(f.gotUID, uid)
	if f.getErr != nil {
		return f.getErr
	}
	if _, ok := f.links[id]; !ok || f.ownerOf(id) != uid {
		return errNotFound
	}
	return nil
}

func (f *fakeRepo) UpdateOGImage(_ context.Context, uid authctx.UserID, id int64, imageURL string) error {
	f.gotUID = append(f.gotUID, uid)
	if f.updateErr != nil {
		return f.updateErr
	}
	if _, ok := f.links[id]; !ok || f.ownerOf(id) != uid {
		return errNotFound
	}
	f.updatedURL[id] = imageURL
	return nil
}

func (f *fakeRepo) ClearOGImage(_ context.Context, uid authctx.UserID, id int64) error {
	f.gotUID = append(f.gotUID, uid)
	if f.clearErr != nil {
		return f.clearErr
	}
	// Seeded-link tests aside, a fake that cleared regardless of owner would let
	// DeleteImage drop its scoping unnoticed.
	if _, ok := f.links[id]; ok && f.ownerOf(id) != uid {
		return errNotFound
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
		captureSem:    make(chan struct{}, maxCaptureInFlight),
	}

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
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

	// Defense-in-depth on the leak fix (screenshot_handler.go): Chromium's
	// raw error can include local binary paths / system state. The wire
	// message must be generic; the full err stays only in slog. Asserting
	// both the expected literal AND the absence of the planted payload.
	assert.Equal(t, "failed to capture screenshot", errBlock["message"],
		"wire message must be the generic literal, not the formatted err")
	assert.NotContains(t, errBlock["message"], "chromium crashed",
		"internal Chromium error text must NOT reach the response body")
	assert.NotContains(t, errBlock["message"], "chromium",
		"no part of the planted payload may leak")
}

func TestCaptureAndStore_UploadFails(t *testing.T) {
	sc := &fakeScreenshotter{png: realPNG(t, 300, 200)}
	up := newFakeUploader()
	up.err = errors.New("object store down")
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
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
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
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
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
	repo.links[42] = Link{ID: 42}
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
	repo.links[999] = Link{ID: 999}
	r, _, _ := buildRouter(t, sc, up, repo)

	req := httptest.NewRequest(http.MethodGet, "/api/files/screenshots/999.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestProxyFile_RejectsOversizedObject(t *testing.T) {
	sc := &fakeScreenshotter{}
	up := newFakeUploader()
	up.getErr = fmt.Errorf("storage: get object: %w", storage.ErrObjectTooLarge)
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1}
	r, _, _ := buildRouter(t, sc, up, repo)

	req := httptest.NewRequest(http.MethodGet, "/api/files/screenshots/1.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.Contains(t, w.Body.String(), "too_large")
}

func TestCaptureAndStore_Returns429WhenSaturated(t *testing.T) {
	// Hold both admission slots with blocking Captures, then assert extras 429.
	entered := make(chan struct{}, maxCaptureInFlight)
	release := make(chan struct{})
	var inFlight atomic.Int32
	pngBytes := realPNG(t, 40, 30)
	sc := &blockingScreenshotter{entered: entered, release: release, inFlight: &inFlight, png: pngBytes}
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "https://example.com"}
	r, _, _ := buildRouter(t, sc, up, repo)

	var holdWG sync.WaitGroup
	holdCodes := make(chan int, maxCaptureInFlight)
	for i := 0; i < maxCaptureInFlight; i++ {
		holdWG.Add(1)
		go func() {
			defer holdWG.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			holdCodes <- w.Code
		}()
	}
	for i := 0; i < maxCaptureInFlight; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for captures to enter")
		}
	}
	// Slots full — further requests must be rejected immediately.
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusTooManyRequests, w.Code)
		assert.Equal(t, "5", w.Header().Get("Retry-After"))
		assert.Contains(t, w.Body.String(), "screenshot_busy")
	}
	close(release)
	holdWG.Wait()
	close(holdCodes)
	for code := range holdCodes {
		assert.Equal(t, http.StatusOK, code)
	}
	assert.LessOrEqual(t, int(inFlight.Load()), maxCaptureInFlight)
}

type blockingScreenshotter struct {
	entered  chan struct{}
	release  chan struct{}
	inFlight *atomic.Int32
	png      []byte
}

func (b *blockingScreenshotter) Capture(_ context.Context, _ string) ([]byte, error) {
	b.inFlight.Add(1)
	defer b.inFlight.Add(-1)
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return b.png, nil
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
	repo.links[13] = Link{ID: 13}
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
	repo.links[1] = Link{ID: 1}
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
	repo.links[1] = Link{ID: 1}
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
	repo.links[1] = Link{ID: 1}
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
	repo.links[42] = Link{ID: 42}
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
	repo.links[5] = Link{ID: 5}
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
	repo.links[9] = Link{ID: 9}
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
	repo.links[1] = Link{ID: 1}
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
	up.err = errors.New("object store down")
	repo := newFakeRepo()
	repo.links[3] = Link{ID: 3}
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
	repo.links[3] = Link{ID: 3}
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
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	r.Delete("/api/links/{id}/image", sh.DeleteImage)

	req := httptest.NewRequest(http.MethodDelete, "/api/links/8/image", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, []int64{8}, repo.clearedIDs)
}

func TestDeleteImage_InvalidID(t *testing.T) {
	sh := &ScreenshotHandler{repo: newFakeRepo(), storage: newFakeUploader(), logger: newTestLogger()}
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	r.Delete("/api/links/{id}/image", sh.DeleteImage)
	req := httptest.NewRequest(http.MethodDelete, "/api/links/abc/image", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDeleteImage_RepoError(t *testing.T) {
	repo := newFakeRepo()
	repo.clearErr = errors.New("db")
	sh := &ScreenshotHandler{repo: repo, storage: newFakeUploader(), logger: newTestLogger()}
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	r.Delete("/api/links/{id}/image", sh.DeleteImage)
	req := httptest.NewRequest(http.MethodDelete, "/api/links/1/image", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.NotEqual(t, http.StatusNoContent, w.Code)
}

func TestPurgeLegacyVariants_DeleteErrorLogged(t *testing.T) {
	up := newFakeUploader()
	up.deleteErr = errors.New("object store down")
	sh := &ScreenshotHandler{storage: up, logger: newTestLogger()}
	sh.purgeLegacyVariants(context.Background(), "og", 42, "jpg")
	assert.NotEmpty(t, up.deleted)
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

// --- cross-tenant object-store tests ---

// otherUser is a principal that is not the one buildRouter authenticates as.
const otherUser = authctx.UserID(authctxtest.DefaultUser + 1)

// TestProxyFile_ForeignLinkKeyIs404 locks the ownership gate on id-derived
// object keys. `screenshots/{id}.jpg` embeds a link id from a dense BIGSERIAL
// space, so without this gate any authenticated user could walk the range and
// pull every other tenant's screenshots and uploaded thumbnails straight out of
// the bucket — the row scoping in the repository never comes into play, because
// this route reads the object store directly.
func TestProxyFile_ForeignLinkKeyIs404(t *testing.T) {
	up := newFakeUploader()
	up.uploaded["screenshots/77.png"] = fakePNG("SECRET")
	up.uploaded["images/78.png"] = fakePNG("ALSO_SECRET")
	repo := newFakeRepo()
	repo.ownedBy(77, otherUser, Link{ID: 77})
	repo.ownedBy(78, otherUser, Link{ID: 78})
	r, _, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	for _, key := range []string{"screenshots/77.png", "images/78.png"} {
		req := httptest.NewRequest(http.MethodGet, "/api/files/"+key, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code, "key %q belongs to another user", key)
		assert.NotContains(t, w.Body.String(), "SECRET", "no byte of the object may leak")
	}
}

// TestProxyFile_ForeignKeyIsIndistinguishableFromMissing keeps the 404-not-403
// rule (CLAUDE.md §4) alive at the object layer: a distinct status or code for
// "exists but is not yours" would turn the proxy into an existence oracle over
// another tenant's ids.
func TestProxyFile_ForeignKeyIsIndistinguishableFromMissing(t *testing.T) {
	up := newFakeUploader()
	up.uploaded["screenshots/77.png"] = fakePNG("SECRET")
	repo := newFakeRepo()
	repo.ownedBy(77, otherUser, Link{ID: 77})
	r, _, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	get := func(key string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/files/"+key, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	foreignCode, foreignBody := get("screenshots/77.png") // exists, owned by other
	missingCode, missingBody := get("screenshots/12345.png")

	assert.Equal(t, missingCode, foreignCode)
	assert.Equal(t, missingBody, foreignBody,
		"a foreign key and an absent key must be byte-identical, or the response enumerates other tenants' ids")
}

// TestProxyFile_NoteImagesStayReadableWithoutOwnership documents the deliberate
// asymmetry: notes/{uuid} keys are NOT ownership-gated, because the public,
// session-less /n/{slug} page renders body_html and the browser fetches those
// images with no principal at all. Their protection is the 122-bit random UUID,
// which appears nowhere but inside the owning note.
func TestProxyFile_NoteImagesStayReadableWithoutOwnership(t *testing.T) {
	up := newFakeUploader()
	const key = "notes/3f2504e0-4f89-11d3-9a0c-0305e82c3301.png"
	up.uploaded[key] = fakePNG("NOTE_IMAGE")
	repo := newFakeRepo() // no link rows at all
	r, _, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	req := httptest.NewRequest(http.MethodGet, "/api/files/"+key, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "gating this would break every published note")
}

// TestUploadImage_ForeignLinkWritesNothing is the destructive half. The object
// key is images/{id}.ext with no tenant segment, so uploading under another
// user's link id used to overwrite THEIR image and purge their sibling
// extensions before the scoped DB update ever returned 404 — the 404 arrived
// after the damage.
func TestUploadImage_ForeignLinkWritesNothing(t *testing.T) {
	up := newFakeUploader()
	up.uploaded["images/77.jpg"] = []byte("victims-image")
	up.uploaded["images/77.webp"] = []byte("victims-legacy-variant")
	repo := newFakeRepo()
	repo.ownedBy(77, otherUser, Link{ID: 77})
	r, fakeUp, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	req, ct := buildMultipart(t, 77, "image", "x.png", "image/png", realPNG(t, 100, 100))
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, []byte("victims-image"), fakeUp.uploaded["images/77.jpg"],
		"the victim's object must be byte-identical after a rejected upload")
	assert.Equal(t, []byte("victims-legacy-variant"), fakeUp.uploaded["images/77.webp"],
		"purgeLegacyVariants must not run for a link the caller does not own")
	assert.Empty(t, fakeUp.deleted, "no delete may be issued before ownership is established")
	assert.Empty(t, fakeUp.ops, "no write may be issued before ownership is established")
}

// TestScreenshotHandler_ForwardsTheAuthenticatedPrincipal closes the fake-level
// gap: every handler here calls the repository with a uid, and a fake that
// ignored it would let a handler pass a zero value — or the wrong user — and
// still go green on all of the above.
func TestScreenshotHandler_ForwardsTheAuthenticatedPrincipal(t *testing.T) {
	up := newFakeUploader()
	up.uploaded["screenshots/1.png"] = fakePNG("x")
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "https://example.com"}
	r, _, _ := buildRouter(t, &fakeScreenshotter{png: realPNG(t, 10, 10)}, up, repo)

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/files/screenshots/1.png", nil),
		httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil),
	} {
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	require.NotEmpty(t, repo.gotUID, "the repository must have been reached")
	for _, got := range repo.gotUID {
		assert.Equal(t, authctxtest.DefaultUser, got,
			"handlers must forward the request principal, not a zero or hardcoded id")
	}
}
