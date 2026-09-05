package links

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/linkimage"
	"foldex/internal/pkg/authctx"
	"foldex/internal/ports"

	"foldex/internal/pkg/authctx/authctxtest"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/netpolicy"
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

// decodeBombPNG is a tiny PNG whose IHDR declares 8000×8000 (64 MP, over the
// 50 MP cap) while the IDAT stays a 1×1 pixel.
func decodeBombPNG(t *testing.T) []byte {
	t.Helper()
	bomb := append([]byte(nil), realPNG(t, 1, 1)...)
	require.GreaterOrEqual(t, len(bomb), 33)
	binary.BigEndian.PutUint32(bomb[16:20], 8_000)
	binary.BigEndian.PutUint32(bomb[20:24], 8_000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	return bomb
}

// --- fakes ---

type fakeScreenshotter struct {
	mu           sync.Mutex
	png          []byte
	err          error
	urls         []string
	beforeReturn func()
}

func (f *fakeScreenshotter) Capture(_ context.Context, pageURL string) ([]byte, error) {
	f.mu.Lock()
	f.urls = append(f.urls, pageURL)
	png, err, beforeReturn := f.png, f.err, f.beforeReturn
	f.mu.Unlock()
	if beforeReturn != nil {
		beforeReturn()
	}
	return png, err
}

func (f *fakeScreenshotter) capturedURLs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.urls...)
}

type uploadOp struct {
	key         string
	contentType string
	bytes       []byte
}

type fakeUploader struct {
	mu        sync.Mutex
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
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.uploaded[key] = data
	f.ops = append(f.ops, uploadOp{key: key, contentType: ct, bytes: data})
	return nil
}

func (f *fakeUploader) GetObject(_ context.Context, key string) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, "", f.getErr
	}
	d, ok := f.uploaded[key]
	if !ok {
		return nil, "", fmt.Errorf("fake: %q: %w", key, ports.ErrObjectNotFound)
	}
	return d, "image/png", nil
}

func (f *fakeUploader) DeleteObject(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	mu         sync.Mutex
	links      map[int64]Link
	owners     map[int64]authctx.UserID // absent ⇒ owned by authctxtest.DefaultUser
	gotUID     []authctx.UserID
	updatedURL map[int64]string
	clearedIDs []int64
	getErr     error
	updateErr  error
	clearErr   error
	casApplied bool

	// Self-healing after a missing object. `invalidated` records the exact
	// (id, url) pairs so a test can prove the CONDITIONAL predicate reached
	// the repository, not merely that something was called.
	invalidated   []invalidateCall
	invalidateOK  bool
	invalidateErr error
}

type invalidateCall struct {
	id  int64
	url string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		links:      map[int64]Link{},
		owners:     map[int64]authctx.UserID{},
		updatedURL: map[int64]string{},
		casApplied: true,
	}
}

// ownedBy registers a link belonging to someone other than the default user.
func (f *fakeRepo) ownedBy(id int64, uid authctx.UserID, l Link) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.links[id] = l
	f.owners[id] = uid
}

func (f *fakeRepo) ownerOfLocked(id int64) authctx.UserID {
	if uid, ok := f.owners[id]; ok {
		return uid
	}
	return authctxtest.DefaultUser
}

// errNotFound mirrors what the scoped repository returns for a row that either
// does not exist or belongs to another user — the two are deliberately
// indistinguishable (CLAUDE.md §4). It is the same domain error the real
// Repository returns, so handler tests observe production's 404 rather than the
// 500 a bare error would produce.
var errNotFound = domainerr.ErrNotFound

func (f *fakeRepo) Get(_ context.Context, uid authctx.UserID, id int64) (Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotUID = append(f.gotUID, uid)
	if f.getErr != nil {
		return Link{}, f.getErr
	}
	l, ok := f.links[id]
	if !ok || f.ownerOfLocked(id) != uid {
		return Link{}, errNotFound
	}
	return l, nil
}

func (f *fakeRepo) AssertOwned(_ context.Context, uid authctx.UserID, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotUID = append(f.gotUID, uid)
	if f.getErr != nil {
		return f.getErr
	}
	if _, ok := f.links[id]; !ok || f.ownerOfLocked(id) != uid {
		return errNotFound
	}
	return nil
}

func (f *fakeRepo) ReplaceOGImage(_ context.Context, uid authctx.UserID, id int64, imageURL string) (*string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotUID = append(f.gotUID, uid)
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	link, ok := f.links[id]
	if !ok || f.ownerOfLocked(id) != uid {
		return nil, errNotFound
	}
	previous := link.OGImageURL
	link.OGImageURL = &imageURL
	f.links[id] = link
	f.updatedURL[id] = imageURL
	return previous, nil
}

func (f *fakeRepo) UpdateOGImageIfUnchanged(_ context.Context, uid authctx.UserID, id int64, imageURL string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotUID = append(f.gotUID, uid)
	if f.updateErr != nil {
		return false, f.updateErr
	}
	if _, ok := f.links[id]; !ok || f.ownerOfLocked(id) != uid {
		return false, nil
	}
	if !f.casApplied {
		return false, nil
	}
	f.updatedURL[id] = imageURL
	return true, nil
}

func (f *fakeRepo) InvalidateMissingPreview(_ context.Context, uid authctx.UserID, id int64, missingURL string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotUID = append(f.gotUID, uid)
	f.invalidated = append(f.invalidated, invalidateCall{id: id, url: missingURL})
	if f.invalidateErr != nil {
		return false, f.invalidateErr
	}
	return f.invalidateOK, nil
}

func (f *fakeRepo) ClearOGImage(_ context.Context, uid authctx.UserID, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotUID = append(f.gotUID, uid)
	if f.clearErr != nil {
		return f.clearErr
	}
	// Seeded-link tests aside, a fake that cleared regardless of owner would let
	// DeleteImage drop its scoping unnoticed.
	if _, ok := f.links[id]; ok && f.ownerOfLocked(id) != uid {
		return errNotFound
	}
	f.clearedIDs = append(f.clearedIDs, id)
	return nil
}

func (f *fakeRepo) delete(id int64) {
	f.mu.Lock()
	delete(f.links, id)
	f.mu.Unlock()
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
		captureUsers:  make(map[authctx.UserID]int),
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
	assert.Regexp(t, `^/api/files/screenshots/1\.[0-9a-f-]{36}\.jpg$`, body["url"])
	assert.Equal(t, body["url"], repo.updatedURL[1])

	// Stored object is a real JPEG with the long side downscaled to 1024.
	// Size-vs-source isn't asserted: synthetic test PNGs compress better
	// with DEFLATE than JPEG. The production case (real screenshots /
	// photos) is exercised via integration tests.
	key := strings.TrimPrefix(body["url"], "/api/files/")
	stored, ok := fakeUp.uploaded[key]
	require.True(t, ok, "expected versioned screenshot in uploaded map")
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

type deadlineScreenshotter struct {
	remaining time.Duration
}

func (s *deadlineScreenshotter) Capture(ctx context.Context, _ string) ([]byte, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, errors.New("capture context has no deadline")
	}
	s.remaining = time.Until(deadline)
	return nil, errors.New("stop after recording deadline")
}

func TestCaptureAndStorePassesFullCaptureEnvelope(t *testing.T) {
	sc := &deadlineScreenshotter{}
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "https://example.com"}
	r, _, _ := buildRouter(t, sc, up, repo)

	req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	assert.Greater(t, sc.remaining, captureTimeout-time.Second)
	assert.LessOrEqual(t, sc.remaining, captureTimeout)
}

func TestCaptureAndStore_LateEgressBlockStoresNothing(t *testing.T) {
	sc := &fakeScreenshotter{err: errors.New("screenshot: blocked private subresource")}
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "https://example.com"}
	r, fakeUp, fakeRepo := buildRouter(t, sc, up, repo)

	req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "screenshot_failed")
	assert.Empty(t, fakeUp.ops, "a capture invalidated after navigation must never reach storage")
	assert.Empty(t, fakeRepo.updatedURL)
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

func TestCaptureAndStore_ConcurrentDeleteRemovesOperationObject(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "https://example.com", UpdatedAt: time.Now()}
	sc := &fakeScreenshotter{png: realPNG(t, 300, 200), beforeReturn: func() {
		repo.delete(1)
	}}
	r, fakeUp, _ := buildRouter(t, sc, up, repo)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil))

	assert.Equal(t, http.StatusConflict, w.Code)
	require.Len(t, fakeUp.ops, 1)
	key := fakeUp.ops[0].key
	assert.Contains(t, fakeUp.deleted, key)
	assert.NotContains(t, fakeUp.uploaded, key)
}

func TestCaptureAndStore_PublishFailureRemovesOnlyOperationObject(t *testing.T) {
	up := newFakeUploader()
	const previousKey = "images/1.jpg"
	up.uploaded[previousKey] = []byte("previous")
	previousURL := "/api/files/" + previousKey
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "https://example.com", OGImageURL: &previousURL, UpdatedAt: time.Now()}
	repo.updateErr = errors.New("db down")
	r, fakeUp, _ := buildRouter(t, &fakeScreenshotter{png: realPNG(t, 300, 200)}, up, repo)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "publish_failed")
	require.Len(t, fakeUp.ops, 1)
	assert.Contains(t, fakeUp.deleted, fakeUp.ops[0].key)
	assert.NotContains(t, fakeUp.deleted, previousKey)
	assert.Equal(t, []byte("previous"), fakeUp.uploaded[previousKey])
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

func TestCaptureAndStore_RejectsRFC6598WithProductionPolicy(t *testing.T) {
	sc := &fakeScreenshotter{png: realPNG(t, 50, 50)}
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "http://100.64.0.1/carrier-internal"}
	sh := &ScreenshotHandler{
		repo: repo, screenshotter: sc, storage: up,
		urlPolicy: netpolicy.IsPublicURL, logger: newTestLogger(),
		captureSem: make(chan struct{}, maxCaptureInFlight),
	}
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	r.Post("/api/links/{id}/screenshot", sh.CaptureAndStore)

	req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "private_target")
	assert.Empty(t, up.uploaded)
	assert.Empty(t, sc.capturedURLs(), "RFC6598 must be rejected before Chromium")
}

func TestCaptureAndStore_PolicyUsesBoundedContextOnce(t *testing.T) {
	sc := &fakeScreenshotter{png: realPNG(t, 50, 50)}
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "https://example.com"}
	var calls int
	var remaining time.Duration
	sh := &ScreenshotHandler{
		repo: repo, screenshotter: sc, storage: up,
		urlPolicy: func(ctx context.Context, _ string) bool {
			calls++
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			remaining = time.Until(deadline)
			return true
		},
		logger: newTestLogger(), captureSem: make(chan struct{}, maxCaptureInFlight),
	}
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	r.Post("/api/links/{id}/screenshot", sh.CaptureAndStore)

	req := httptest.NewRequest(http.MethodPost, "/api/links/1/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, calls)
	assert.Greater(t, remaining, capturePolicyTimeout-time.Second)
	assert.LessOrEqual(t, remaining, capturePolicyTimeout)
	assert.Len(t, sc.capturedURLs(), 1)
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

func TestCaptureAndStore_OptimizeFailureStoresNothing(t *testing.T) {
	// fakePNG sniffs as image/png but isn't a decodable PNG — Optimize
	// returns ErrDecode. Storing the original would skip INV-077 re-encode.
	bad := fakePNG("not really a png")
	sc := &fakeScreenshotter{png: bad}
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[7] = Link{ID: 7, URL: "https://example.com"}
	r, fakeUp, _ := buildRouter(t, sc, up, repo)

	req := httptest.NewRequest(http.MethodPost, "/api/links/7/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	errBlock, _ := body["error"].(map[string]any)
	assert.Equal(t, "invalid_image", errBlock["code"])
	assert.Empty(t, fakeUp.ops, "undecodable screenshot must not be stored")
	assert.Empty(t, fakeUp.uploaded)
}

func TestCaptureAndStore_RejectsDecodeBomb(t *testing.T) {
	sc := &fakeScreenshotter{png: decodeBombPNG(t)}
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[7] = Link{ID: 7, URL: "https://example.com"}
	r, fakeUp, _ := buildRouter(t, sc, up, repo)

	req := httptest.NewRequest(http.MethodPost, "/api/links/7/screenshot", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	errBlock, _ := body["error"].(map[string]any)
	assert.Equal(t, "invalid_image", errBlock["code"])
	assert.Empty(t, fakeUp.ops, "decode bomb must not be written to object storage")
	assert.Empty(t, fakeUp.uploaded)
	assert.Empty(t, repo.updatedURL)
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
	assert.Equal(t, "private, max-age=86400", w.Header().Get("Cache-Control"))
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
	up.getErr = fmt.Errorf("storage: get object: %w", ports.ErrObjectTooLarge)
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
	// One user can hold only one slot, leaving capacity for another tenant.
	entered := make(chan struct{}, maxCaptureInFlight)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	var inFlight atomic.Int32
	pngBytes := realPNG(t, 40, 30)
	sc := &blockingScreenshotter{entered: entered, release: release, inFlight: &inFlight, png: pngBytes}
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[1] = Link{ID: 1, URL: "https://example.com"}
	const secondUser = authctx.UserID(authctxtest.DefaultUser + 1)
	repo.ownedBy(2, secondUser, Link{ID: 2, URL: "https://example.org"})
	sh := &ScreenshotHandler{
		repo: repo, screenshotter: sc, storage: up,
		urlPolicy: allowAllPolicy, logger: newTestLogger(),
		captureSem: make(chan struct{}, maxCaptureInFlight),
	}
	r1 := chi.NewRouter()
	r1.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	r1.Post("/api/links/{id}/screenshot", sh.CaptureAndStore)
	r2 := chi.NewRouter()
	r2.Use(authctxtest.Middleware(secondUser))
	r2.Post("/api/links/{id}/screenshot", sh.CaptureAndStore)

	var holdWG sync.WaitGroup
	holdCodes := make(chan int, maxCaptureInFlight)
	for _, tc := range []struct {
		router http.Handler
		id     int
	}{{r1, 1}, {r2, 2}} {
		holdWG.Add(1)
		go func(router http.Handler, id int) {
			defer holdWG.Done()
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/links/%d/screenshot", id), nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			holdCodes <- w.Code
		}(tc.router, tc.id)
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
		r1.ServeHTTP(w, req)
		assert.Equal(t, http.StatusTooManyRequests, w.Code)
		assert.Equal(t, "5", w.Header().Get("Retry-After"))
		assert.Contains(t, w.Body.String(), "screenshot_busy")
	}
	releaseAll()
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
		{"notes/3f2504e0-4f89-11d3-9a0c-0305e82c3301.png", true},
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

func TestIsValidNoteKey(t *testing.T) {
	for _, key := range []string{
		"notes/3f2504e0-4f89-11d3-9a0c-0305e82c3301.jpg",
		"notes/8bcb9d80-8212-4ef3-a6a8-24f9471cf90e.jpeg",
		"notes/8bcb9d80-8212-4ef3-a6a8-24f9471cf90e.png",
		"notes/8bcb9d80-8212-4ef3-a6a8-24f9471cf90e.gif",
		"notes/8bcb9d80-8212-4ef3-a6a8-24f9471cf90e.webp",
	} {
		assert.True(t, isValidNoteKey(key), "key %q", key)
	}
	for _, key := range []string{
		"notes/1.jpg",
		"notes/not-a-uuid.jpg",
		"notes/8BCB9D80-8212-4EF3-A6A8-24F9471CF90E.jpg",
		"notes/8bcb9d80-8212-4ef3-a6a8-24f9471cf90e.svg",
		"notes/../images/1.jpg",
		"images/8bcb9d80-8212-4ef3-a6a8-24f9471cf90e.jpg",
	} {
		assert.False(t, isValidNoteKey(key), "key %q", key)
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
	assert.Regexp(t, `^/api/files/images/42\.[0-9a-f-]{36}\.jpg$`, body["url"])
	assert.Equal(t, body["url"], fakeRp.updatedURL[42])

	require.Len(t, fakeUp.ops, 1)
	assert.Regexp(t, `^images/42\.[0-9a-f-]{36}\.jpg$`, fakeUp.ops[0].key)
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
	assert.Contains(t, fakeUp.deleted, "images/5.jpg")
	assert.NotContains(t, fakeUp.deleted, fakeUp.ops[0].key, "must not delete the operation-owned object")
	_, oldStillThere := fakeUp.uploaded["images/5.png"]
	assert.False(t, oldStillThere, "fakeUploader DeleteObject should have removed the stale .png")
}

func TestUploadImage_OptimizeFailureStoresNothing(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[9] = Link{ID: 9}
	r, fakeUp, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	// PNG-sniff header but body isn't a real PNG — Optimize returns
	// ErrDecode. Storing the original would skip INV-077 re-encode.
	bad := fakePNG("nope")
	req, ct := buildMultipart(t, 9, "image", "broken.png", "image/png", bad)
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	errBlock, _ := body["error"].(map[string]any)
	assert.Equal(t, "invalid_image", errBlock["code"])
	assert.Empty(t, fakeUp.ops, "undecodable upload must not be stored")
	assert.Empty(t, fakeUp.uploaded)
}

func TestUploadImage_RejectsDecodeBomb(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[9] = Link{ID: 9}
	r, fakeUp, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	req, ct := buildMultipart(t, 9, "image", "bomb.png", "image/png", decodeBombPNG(t))
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	errBlock, _ := body["error"].(map[string]any)
	assert.Equal(t, "invalid_image", errBlock["code"])
	assert.Empty(t, fakeUp.ops, "decode bomb must not be written to object storage")
	assert.Empty(t, fakeUp.uploaded)
	assert.Empty(t, repo.updatedURL)
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
	const previousKey = "images/3.png"
	up.uploaded[previousKey] = []byte("previous")
	up.err = errors.New("object store down")
	repo := newFakeRepo()
	previousURL := "/api/files/" + previousKey
	repo.links[3] = Link{ID: 3, OGImageURL: &previousURL}
	r, _, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	req, ct := buildMultipart(t, 3, "image", "x.png", "image/png", realPNG(t, 100, 100))
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, []byte("previous"), up.uploaded[previousKey])
	assert.NotContains(t, up.deleted, previousKey, "failed replacement must not purge the published object")
	require.Len(t, up.deleted, 1)
	assert.Regexp(t, `^images/3\.[0-9a-f-]{36}\.jpg$`, up.deleted[0])
}

func TestUploadImage_RepoUpdateFails(t *testing.T) {
	up := newFakeUploader()
	const previousKey = "images/3.png"
	up.uploaded[previousKey] = []byte("previous")
	repo := newFakeRepo()
	previousURL := "/api/files/" + previousKey
	repo.links[3] = Link{ID: 3, OGImageURL: &previousURL}
	repo.updateErr = errors.New("db down")
	r, _, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	req, ct := buildMultipart(t, 3, "image", "x.png", "image/png", realPNG(t, 100, 100))
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, []byte("previous"), up.uploaded[previousKey])
	assert.NotContains(t, up.deleted, previousKey, "failed publication must preserve the referenced object")
	require.Len(t, up.deleted, 1)
	assert.Regexp(t, `^images/3\.[0-9a-f-]{36}\.jpg$`, up.deleted[0])
}

func TestUploadImage_ReplacementCleansExactSupersededObject(t *testing.T) {
	up := newFakeUploader()
	const previousKey = "images/3.jpg"
	up.uploaded[previousKey] = []byte("previous")
	repo := newFakeRepo()
	previousURL := "/api/files/" + previousKey
	repo.links[3] = Link{ID: 3, OGImageURL: &previousURL}
	r, _, _ := buildRouter(t, &fakeScreenshotter{}, up, repo)

	req, ct := buildMultipart(t, 3, "image", "x.png", "image/png", realPNG(t, 100, 100))
	req.Header.Set("Content-Type", ct)
	first := httptest.NewRecorder()
	r.ServeHTTP(first, req)
	require.Equal(t, http.StatusOK, first.Code)
	var firstBody map[string]string
	require.NoError(t, json.NewDecoder(first.Body).Decode(&firstBody))
	firstKey := strings.TrimPrefix(firstBody["url"], "/api/files/")
	require.Contains(t, up.uploaded, firstKey)

	req, ct = buildMultipart(t, 3, "image", "y.png", "image/png", realPNG(t, 120, 120))
	req.Header.Set("Content-Type", ct)
	second := httptest.NewRecorder()
	r.ServeHTTP(second, req)

	assert.Equal(t, http.StatusOK, second.Code)
	assert.Contains(t, up.deleted, previousKey)
	assert.Contains(t, up.deleted, firstKey)
	assert.NotContains(t, up.uploaded, firstKey)
	assert.Regexp(t, `^/api/files/images/3\.[0-9a-f-]{36}\.jpg$`, repo.updatedURL[3])
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
	errs := linkimage.PurgeLegacy(context.Background(), up, "og", 42)
	assert.Len(t, errs, 4)
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
// read-only asymmetry: public /n/{slug} browsers fetch notes/{uuid} without a
// principal, while migration 000022 governs write/delete authority separately.
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
	assert.Equal(t, "public, max-age=86400", w.Header().Get("Cache-Control"))
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

// --- self-healing when the stored object is gone ---

// buildHealRouter is buildRouter plus an enqueuer, so a test can see whether
// the preview worker was actually re-armed.
func buildHealRouter(t *testing.T, up *fakeUploader, repo *fakeRepo) (http.Handler, *fakeEnqueuer) {
	t.Helper()
	enq := &fakeEnqueuer{}
	sh := (&ScreenshotHandler{
		repo:         repo,
		storage:      up,
		urlPolicy:    allowAllPolicy,
		logger:       newTestLogger(),
		captureSem:   make(chan struct{}, maxCaptureInFlight),
		captureUsers: make(map[authctx.UserID]int),
	}).WithEnqueuer(enq)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	r.Route("/api", func(api chi.Router) { api.Get("/files/*", sh.ProxyFile) })
	return r, enq
}

type fakeEnqueuer struct {
	mu  sync.Mutex
	ids []int64
}

func (f *fakeEnqueuer) Enqueue(id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, id)
	return nil
}

// `maybeScreenshot` only ever fires for a link whose og_image_url is EMPTY, so
// a row pointing at bytes that no longer exist is stuck: the card stays blank
// forever while preview_status still reads 'ok'. This is the branch that
// unsticks it.
func TestProxyFile_MissingObjectReArmsThePreview(t *testing.T) {
	up := newFakeUploader() // nothing uploaded ⇒ the key is genuinely absent
	repo := newFakeRepo()
	repo.links[42] = Link{ID: 42}
	repo.invalidateOK = true
	r, enq := buildHealRouter(t, up, repo)

	req := httptest.NewRequest(http.MethodGet, "/api/files/screenshots/42.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code, "the caller still gets a 404")
	require.Len(t, repo.invalidated, 1)
	// The exact URL matters: it is the predicate that makes the write
	// conditional, and passing the bare key would match no row at all.
	assert.Equal(t, invalidateCall{id: 42, url: "/api/files/screenshots/42.png"}, repo.invalidated[0])
	assert.Equal(t, []int64{42}, enq.ids)
}

// THE safety property. A store that is merely unreachable must not look like a
// store with nothing in it: taking the healing branch on any error would let a
// single network blip clear every og_image_url on the instance and re-screenshot
// the whole library. Same rule as push subscriptions in CLAUDE.md §4.
func TestProxyFile_TransportFailureNeverClearsAnImage(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"connection refused", errors.New("dial tcp 10.0.0.1:9000: connect: connection refused")},
		{"timeout", context.DeadlineExceeded},
		{"an error that merely says not found", errors.New("not found")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := newFakeUploader()
			up.getErr = tc.err
			repo := newFakeRepo()
			repo.links[42] = Link{ID: 42}
			repo.invalidateOK = true
			r, enq := buildHealRouter(t, up, repo)

			req := httptest.NewRequest(http.MethodGet, "/api/files/screenshots/42.png", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, http.StatusNotFound, w.Code)
			assert.Empty(t, repo.invalidated, "an unreachable store is not an empty one")
			assert.Empty(t, enq.ids)
		})
	}
}

// A screenful of broken cards is a screenful of concurrent 404s. The repository
// reports whether it actually changed the row, and only a real change enqueues
// — otherwise thirty-three cards become thirty-three captures of a handful of
// links.
func TestProxyFile_SecondRequestForTheSameGoneObjectDoesNotEnqueueAgain(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	repo.links[42] = Link{ID: 42}
	repo.invalidateOK = false // the row already moved on
	r, enq := buildHealRouter(t, up, repo)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/files/screenshots/42.png", nil)
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	assert.Len(t, repo.invalidated, 5, "each request still asks")
	assert.Empty(t, enq.ids, "but only a row that actually changed re-arms the worker")
}

// Note media is user-uploaded and nothing can regenerate it, so clearing a
// reference to it would destroy the only record that the image was ever there.
func TestProxyFile_MissingNoteMediaIsNotHealed(t *testing.T) {
	up := newFakeUploader()
	repo := newFakeRepo()
	r, enq := buildHealRouter(t, up, repo)

	req := httptest.NewRequest(http.MethodGet,
		"/api/files/notes/0d5d3a2e-1b3c-4f5a-8e9d-2c1b0a9f8e7d.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Empty(t, repo.invalidated)
	assert.Empty(t, enq.ids)
}
