package links

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"foldex/internal/imageopt"
	"foldex/internal/linkimage"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/ports"
)

// allowedFilePrefixes is the closed set of object-key prefixes ProxyFile is
// allowed to serve. Keeps the proxy from being a generic read-any-key object-store
// gateway. "notes/" holds inline images uploaded through the note rich-text
// editor (notes.ImageHandler) — ProxyFile is shared infrastructure so notes
// reuses it rather than standing up a second file-serving endpoint.
var allowedFilePrefixes = []string{"screenshots/", "images/", "notes/"}

// allowedUploadMIMEs is the shared imageopt allowlist.
var allowedUploadMIMEs = imageopt.AllowedUploadMIMEs

// Image optimization defaults — JPEG q≈82 caps thumbnails at 1024 px on the
// longest side. UI cards render at 150 px; 1024 leaves headroom for retina
// and zoom.
const (
	imageMaxDim  = 1024
	imageQuality = 82
)

// Screenshotter captures a URL and returns PNG bytes.
type Screenshotter interface {
	Capture(ctx context.Context, pageURL string) ([]byte, error)
}

// URLPolicy decides whether the given URL is safe to feed into Chromium.
// CaptureAndStore calls this BEFORE launching the browser. Implementations
// must reject IMDS (169.254.169.254), private/loopback IPs, and any
// non-http(s) scheme — otherwise the manual screenshot endpoint becomes a
// read-anywhere primitive (file:///etc/passwd, cloud-metadata exfil, etc.).
//
// The function form keeps the links package decoupled from preview.IsPublicURL,
// which would otherwise create a circular import (preview already imports
// links).
type URLPolicy func(ctx context.Context, pageURL string) bool

// Uploader stores bytes to object storage (canonical: ports.Uploader).
type Uploader = ports.Uploader

// screenshotRepo is the slice of the Repository that ScreenshotHandler needs.
// Defined as an interface so unit tests can inject a fake without a real DB.
type screenshotRepo interface {
	Get(ctx context.Context, uid authctx.UserID, id int64) (Link, error)
	AssertOwned(ctx context.Context, uid authctx.UserID, id int64) error
	ReplaceOGImage(ctx context.Context, uid authctx.UserID, id int64, imageURL string) (*string, error)
	UpdateOGImageIfUnchanged(ctx context.Context, uid authctx.UserID, id int64, imageURL string, expectedUpdatedAt time.Time) (bool, error)
	ClearOGImage(ctx context.Context, uid authctx.UserID, id int64) error
	InvalidateMissingPreview(ctx context.Context, uid authctx.UserID, id int64, missingURL string) (bool, error)
}

// maxCaptureInFlight bounds concurrent CaptureAndStore requests so a flood
// cannot pin unbounded goroutines waiting on Chromium.
const maxCaptureInFlight = 2

const maxCapturePerUser = 1

// captureTimeout covers the pool's independent queue, cold-start, capture, and
// BrowserContext cleanup budgets without shortening a later phase.
const captureTimeout = 70 * time.Second

const (
	capturePolicyTimeout  = 5 * time.Second
	captureStorageTimeout = 10 * time.Second
)

// ScreenshotHandler handles screenshot capture and file proxy routes.
type ScreenshotHandler struct {
	repo          screenshotRepo
	screenshotter Screenshotter
	storage       Uploader
	urlPolicy     URLPolicy
	logger        *slog.Logger
	captureSem    chan struct{}
	captureMu     sync.Mutex
	captureUsers  map[authctx.UserID]int
	// enqueuer re-arms the preview worker when a stored image turns out to be
	// gone. Nil is a supported wiring (the proxy still serves and still
	// invalidates; nothing regenerates until the next boot's requeuePending).
	enqueuer ports.Enqueuer
}

// WithEnqueuer wires the preview worker so a missing object can heal itself.
func (h *ScreenshotHandler) WithEnqueuer(e ports.Enqueuer) *ScreenshotHandler {
	h.enqueuer = e
	return h
}

// NewScreenshotHandler creates a ScreenshotHandler. urlPolicy gates
// CaptureAndStore — pass preview.IsPublicURL from main.go. A nil policy is
// treated as "deny all", which fails closed.
func NewScreenshotHandler(repo *Repository, sc Screenshotter, st Uploader, urlPolicy URLPolicy, logger *slog.Logger) *ScreenshotHandler {
	return &ScreenshotHandler{
		repo:          repo,
		screenshotter: sc,
		storage:       st,
		urlPolicy:     urlPolicy,
		logger:        logger,
		captureSem:    make(chan struct{}, maxCaptureInFlight),
		captureUsers:  make(map[authctx.UserID]int),
	}
}

// CaptureAndStore captures a screenshot of the link's URL, optimizes it, saves
// it to object storage under an operation-owned link key, and publishes that URL.
func (h *ScreenshotHandler) CaptureAndStore(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}

	uid := authctx.MustUser(r.Context())
	if !h.acquireCapture(uid) {
		w.Header().Set("Retry-After", "5")
		httperr.Write(w, httperr.New(http.StatusTooManyRequests, "screenshot_busy", "too many screenshot captures in flight"))
		return
	}
	defer h.releaseCapture(uid)

	link, err := h.repo.Get(r.Context(), uid, id)
	if err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}

	// SSRF gate. Without this, Chromium happily navigates to file://,
	// 169.254.169.254 (IMDS), 127.0.0.1, RFC1918 hosts, etc., and the
	// resulting screenshot would be served back to the caller via
	// /api/files/screenshots/{id} — a read-anywhere primitive.
	if !isHTTPScheme(link.URL) {
		h.logger.Warn("screenshot rejected: non-http scheme", "id", id)
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_scheme", "screenshot target must use http or https"))
		return
	}
	// Nil policy = misconfiguration (handler mounted without the SSRF gate
	// wired in main.go). Distinct error code so ops can tell apart
	// "operator forgot to set ScreenshotURL" from "user picked a private URL".
	// Router boot validation should catch this — guard remains for defense.
	if h.urlPolicy == nil {
		h.logger.Error("screenshot rejected: URLPolicy not configured", "id", id)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "policy_unconfigured", "screenshot policy is not configured"))
		return
	}
	capCtx, cancel := context.WithTimeout(r.Context(), captureTimeout)
	defer cancel()
	policyCtx, policyCancel := context.WithTimeout(capCtx, capturePolicyTimeout)
	allowed := h.urlPolicy(policyCtx, link.URL)
	policyCancel()
	if !allowed {
		h.logger.Warn("screenshot rejected: non-public target", "id", id)
		httperr.Write(w, httperr.New(http.StatusBadRequest, "private_target", "screenshot target must resolve to a public address"))
		return
	}
	png, err := h.screenshotter.Capture(capCtx, link.URL)
	if err != nil {
		// Chromium errors may contain credential-bearing URLs or local paths;
		// logs and the wire response therefore use stable classifications.
		h.logger.Error("screenshot capture failed", "id", id, "reason", screenshotOperationErrorReason(err))
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "screenshot_failed", "failed to capture screenshot"))
		return
	}

	opt := optimizeOrFallback(png, "image/png", "png", h.logger, "screenshot", id)

	storageCtx, storageCancel := context.WithTimeout(r.Context(), captureStorageTimeout)
	defer storageCancel()
	stored, err := linkimage.Store(storageCtx, h.storage, "screenshots", id, opt.Ext, opt.Data, opt.ContentType)
	if err != nil {
		h.logger.Error("screenshot upload failed", "id", id)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "upload_failed", "failed to store screenshot"))
		return
	}
	applied, err := h.repo.UpdateOGImageIfUnchanged(storageCtx, uid, id, stored.URL, link.UpdatedAt)
	if err != nil || !applied {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), captureStorageTimeout)
		cleanupErr := linkimage.Delete(cleanupCtx, h.storage, stored.Key)
		cleanupCancel()
		if cleanupErr != nil {
			h.logger.Warn("screenshot orphan cleanup failed", "id", id)
		}
		if err != nil {
			h.logger.Error("screenshot database publish failed", "id", id)
			httperr.Write(w, httperr.New(http.StatusInternalServerError, "publish_failed", "failed to publish screenshot"))
			return
		}
		httperr.Write(w, httperr.New(http.StatusConflict, "screenshot_superseded", "link changed while screenshot was captured"))
		return
	}
	for _, purgeErr := range linkimage.PurgeLegacy(storageCtx, h.storage, "screenshots", id) {
		h.logger.Warn("purge legacy screenshot failed", "id", id, "err", purgeErr)
	}
	h.deletePreviousLinkImage(storageCtx, link.OGImageURL, stored.Key, id)

	h.logger.Info("screenshot stored",
		"id", id,
		"source_bytes", len(png), "stored_bytes", len(opt.Data),
		"resized", opt.Resized, "reencoded", opt.Reencoded,
	)
	httperr.JSON(w, http.StatusOK, map[string]string{
		"url": stored.URL,
	})
}

func (h *ScreenshotHandler) acquireCapture(uid authctx.UserID) bool {
	h.captureMu.Lock()
	defer h.captureMu.Unlock()
	if h.captureSem == nil {
		h.captureSem = make(chan struct{}, maxCaptureInFlight)
	}
	if h.captureUsers == nil {
		h.captureUsers = make(map[authctx.UserID]int)
	}
	if h.captureUsers[uid] >= maxCapturePerUser {
		return false
	}
	select {
	case h.captureSem <- struct{}{}:
		h.captureUsers[uid]++
		return true
	default:
		return false
	}
}

func (h *ScreenshotHandler) releaseCapture(uid authctx.UserID) {
	h.captureMu.Lock()
	if h.captureUsers[uid] <= 1 {
		delete(h.captureUsers, uid)
	} else {
		h.captureUsers[uid]--
	}
	<-h.captureSem
	h.captureMu.Unlock()
}

func screenshotOperationErrorReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "capture_failed"
	}
}

// ProxyFile proxies a file from object storage to the HTTP client.
// Mounted at GET /api/files/*key. Keys are restricted to the known
// upload/screenshot prefixes so this can't be used to read arbitrary objects
// out of the bucket.
func (h *ScreenshotHandler) ProxyFile(w http.ResponseWriter, r *http.Request) {
	h.proxyFile(w, r, chi.URLParam(r, "*"))
}

// ProxyNoteFile is mounted outside required authentication so images embedded
// in the public /n/{slug} page remain readable. The fixed prefix prevents this
// route from reaching id-derived link media, which still requires ownership.
func (h *ScreenshotHandler) ProxyNoteFile(w http.ResponseWriter, r *http.Request) {
	key := "notes/" + chi.URLParam(r, "*")
	if !isValidNoteKey(key) {
		httperr.Write(w, httperr.ErrNotFound)
		return
	}
	h.proxyFile(w, r, key)
}

func (h *ScreenshotHandler) proxyFile(w http.ResponseWriter, r *http.Request, key string) {
	if !isAllowedKey(key) {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_key", "key is required and must use a supported image prefix"))
		return
	}
	if err := h.authorizeKey(r.Context(), key); err != nil {
		httperr.Write(w, err)
		return
	}

	data, _, err := h.storage.GetObject(r.Context(), key)
	if err != nil {
		if ports.IsObjectTooLarge(err) {
			h.logger.Warn("proxy file: object exceeds serve ceiling")
			httperr.Write(w, httperr.New(http.StatusRequestEntityTooLarge, "too_large", "file exceeds maximum serve size"))
			return
		}
		// A MISSING object is recoverable; an unreachable store is not, and the
		// two must not share a branch. `maybeScreenshot` only ever fires for a
		// link whose og_image_url is EMPTY (ADR-16), so a row pointing at bytes
		// that no longer exist is stuck: the card stays blank forever while
		// preview_status still reads 'ok'. Clearing the reference is what puts
		// it back in the worker's path.
		//
		// Gated on ErrObjectNotFound precisely so a network blip cannot do it.
		// Taking this branch on any error would let one unreachable moment wipe
		// every og_image_url on the instance and re-screenshot the whole
		// library — the same rule as push subscriptions in CLAUDE.md §4.
		if ports.IsObjectNotFound(err) {
			h.healMissingObject(r.Context(), key)
		} else {
			h.logger.Error("proxy file: get object failed")
		}
		httperr.Write(w, httperr.New(http.StatusNotFound, "not_found", "file not found"))
		return
	}

	// Never trust the stored content-type for the response — pin it to what
	// http.DetectContentType reads from the first 512 bytes. Stops a malicious
	// upload that slipped past UploadImage (or arrived via another vector)
	// from being served as text/html and executing in the browser.
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	detected := http.DetectContentType(sniff)
	if !isAllowedServeMIME(detected) {
		h.logger.Warn("proxy file: refusing to serve non-image content", "reason", "non_image")
		httperr.Write(w, httperr.New(http.StatusUnsupportedMediaType, "unsupported_media", "stored object is not a supported image"))
		return
	}
	w.Header().Set("Content-Type", detected)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(key, "notes/") {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=86400")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// authorizeKey decides whether the caller may read `key`.
//
// Object keys are FLAT — there is no tenant segment — so the prefix alone says
// nothing about ownership. The two families are gated differently because they
// are reachable differently:
//
//   - screenshots/{id}.ext and images/{id}.ext embed a LINK id drawn from a
//     dense BIGSERIAL space, so they are trivially enumerable. They are gated on
//     the caller owning that link. A foreign or absent link is reported as the
//     same 404 the object-store miss returns, so the endpoint never
//     distinguishes "someone else's image" from "no image" (the 404-not-403 rule
//     in CLAUDE.md §4 applies to object keys for the same reason it applies to
//     rows).
//   - notes/{uuid}.ext is intentionally a public READ locator: the session-less
//     /n/{slug} page renders body_html and its browser has no principal. The URL
//     grants no mutation authority; uploads/deletes are governed separately by
//     note_media ownership and refs (migration 000022).
func (h *ScreenshotHandler) authorizeKey(ctx context.Context, key string) error {
	notFound := httperr.New(http.StatusNotFound, "not_found", "file not found")
	switch {
	case strings.HasPrefix(key, "notes/"):
		if isValidNoteKey(key) {
			return nil
		}
		return notFound
	case strings.HasPrefix(key, "screenshots/"), strings.HasPrefix(key, "images/"):
		id, ok := linkKeyID(key)
		if !ok {
			// Nothing under these prefixes is written with a non-numeric name,
			// so an unparseable key cannot name a real object. Fail closed
			// rather than fall through to an ungated read.
			return notFound
		}
		if err := h.repo.AssertOwned(ctx, authctx.MustUser(ctx), id); err != nil {
			return notFound
		}
		return nil
	default:
		return notFound
	}
}

// linkKeyID extracts the link id from an id-derived object key
// (`screenshots/12.version.jpg` → 12). Reports false for anything that is not
// `{prefix}/{digits}.{suffix}`.
func linkKeyID(key string) (int64, bool) {
	slash := strings.IndexByte(key, '/')
	if slash < 0 {
		return 0, false
	}
	rest := key[slash+1:]
	dot := strings.IndexByte(rest, '.')
	if dot <= 0 {
		return 0, false
	}
	id, err := strconv.ParseInt(rest[:dot], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// isValidNoteKey keeps the anonymous note-media route narrower than the
// bucket namespace: only canonical UUID names emitted by the note uploader and
// restore paths, with supported raster-image extensions, are readable.
func isValidNoteKey(key string) bool {
	const prefix = "notes/"
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	name := strings.TrimPrefix(key, prefix)
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 {
		return false
	}
	ext := name[dot+1:]
	switch ext {
	case "jpg", "jpeg", "png", "gif", "webp":
	default:
		return false
	}
	id, err := uuid.Parse(name[:dot])
	return err == nil && id.String() == name[:dot]
}

// isHTTPScheme returns true iff pageURL parses to an http or https URL.
// Used to fail-fast before the SSRF policy check (the policy does DNS, this
// catches non-network schemes for free).
func isHTTPScheme(pageURL string) bool {
	u, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil {
		return false
	}
	s := strings.ToLower(u.Scheme)
	return s == "http" || s == "https"
}

// isAllowedKey rejects empty keys, anything containing ".." or starting with
// "/", and anything outside the allowed prefixes.
func isAllowedKey(key string) bool {
	if key == "" {
		return false
	}
	if strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return false
	}
	for _, p := range allowedFilePrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func isAllowedServeMIME(m string) bool {
	for allowed := range allowedUploadMIMEs {
		if m == allowed {
			return true
		}
	}
	return false
}

// UploadImage accepts a multipart upload (field "image"), optimizes it
// (downscale + JPEG re-encode), stores the result under an operation-owned key,
// and atomically replaces the link's og_image_url.
// Mounted at POST /api/links/{id}/image.
func (h *ScreenshotHandler) UploadImage(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}

	// Ownership FIRST, before a single byte is read or written. The object key
	// starts with images/{id}. and has no tenant segment, so uploading under another
	// user's link id could overwrite their image before the scoped update
	// reports not-found.
	uid := authctx.MustUser(r.Context())
	_, err = h.repo.Get(r.Context(), uid, id)
	if err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}

	// 5 MiB is a generous ceiling for a single bookmark thumbnail — a 5 MB
	// JPEG already covers any phone-camera shot once downscaled to 1024 px.
	// imageopt.Optimize additionally caps decoded pixel area to 50 MP, so
	// a payload-size-vs-decoded-size mismatch (decode bomb) is bounded.
	const maxSize = 5 << 20
	// Cap the whole request body — ParseMultipartForm's `maxMemory` only
	// controls when parts spill to a temp file, not the total upload size.
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)
	// Spill to disk after 1 MiB (gosec G120); body already capped by MaxBytesReader.
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_multipart", "request too large or malformed"))
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}

	file, _, err := r.FormFile("image")
	if err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "missing_image", "field 'image' is required"))
		return
	}
	defer file.Close()

	// Read up to maxSize+1 so an oversized payload trips the size check below.
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		h.logger.Error("image upload: read failed", "id", id, "err", err)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "read_failed", "failed to read uploaded file"))
		return
	}
	if len(data) == 0 {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "empty_file", "uploaded file is empty"))
		return
	}
	if int64(len(data)) > maxSize {
		httperr.Write(w, httperr.New(http.StatusRequestEntityTooLarge, "too_large", "image exceeds 5MB limit"))
		return
	}

	// Detect MIME from the actual bytes — never trust the client-supplied
	// Content-Type. Stops HTML/SVG/script files smuggled in with an
	// `image/png` declaration that would later be served back as that MIME
	// (stored XSS via the ProxyFile cache).
	detected := http.DetectContentType(data)
	srcExt, ok := allowedUploadMIMEs[detected]
	if !ok {
		h.logger.Warn("image upload: rejected MIME", "id", id, "reason", "non_image")
		httperr.Write(w, httperr.New(http.StatusUnsupportedMediaType, "invalid_mime", "file must be a PNG, JPEG, GIF, or WebP image"))
		return
	}

	opt := optimizeOrFallback(data, detected, srcExt, h.logger, "image upload", id)

	stored, err := linkimage.Store(r.Context(), h.storage, "images", id, opt.Ext, opt.Data, opt.ContentType)
	if err != nil {
		h.logger.Error("image upload: storage upload failed", "id", id)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "upload_failed", "failed to store image"))
		return
	}

	previous, err := h.repo.ReplaceOGImage(r.Context(), uid, id, stored.URL)
	if err != nil {
		h.deleteUnpublishedLinkImage(stored.Key, id)
		h.logger.Error("image upload: db update failed", "id", id, "err", err)
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	for _, purgeErr := range linkimage.PurgeLegacy(r.Context(), h.storage, "images", id) {
		h.logger.Warn("purge legacy image failed", "id", id, "err", purgeErr)
	}
	h.deletePreviousLinkImage(r.Context(), previous, stored.Key, id)

	h.logger.Info("image uploaded",
		"id", id,
		"source_mime", opt.SourceMIME,
		"source_bytes", len(data), "stored_bytes", len(opt.Data),
		"resized", opt.Resized, "reencoded", opt.Reencoded,
	)
	httperr.JSON(w, http.StatusOK, map[string]string{"url": stored.URL})
}

func (h *ScreenshotHandler) deleteUnpublishedLinkImage(key string, id int64) {
	ctx, cancel := context.WithTimeout(context.Background(), captureStorageTimeout)
	defer cancel()
	if err := h.storage.DeleteObject(ctx, key); err != nil {
		h.logger.Warn("unpublished link image cleanup failed", "id", id)
	}
}

func (h *ScreenshotHandler) deletePreviousLinkImage(ctx context.Context, previous *string, keepKey string, id int64) {
	if previous == nil {
		return
	}
	key, ok := linkimage.LocalKey(*previous)
	ownedScreenshot := fmt.Sprintf("screenshots/%d.", id)
	ownedImage := fmt.Sprintf("images/%d.", id)
	if !ok || key == keepKey || (!strings.HasPrefix(key, ownedScreenshot) && !strings.HasPrefix(key, ownedImage)) {
		return
	}
	if err := h.storage.DeleteObject(ctx, key); err != nil {
		h.logger.Warn("previous link image cleanup failed", "id", id)
	}
}

// DeleteImage clears the og_image_url for a link (does not delete from storage).
func (h *ScreenshotHandler) DeleteImage(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, httperr.ErrBadRequest)
		return
	}
	if err := h.repo.ClearOGImage(r.Context(), authctx.MustUser(r.Context()), id); err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	h.logger.Info("image cleared", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

// optimizeOrFallback runs imageopt.Optimize and, on failure, returns a Result
// that wraps the original bytes so the upload pipeline never blocks on a
// re-encode bug. The fallback decision is logged at warn level.
func optimizeOrFallback(data []byte, sourceMIME, sourceExt string, logger *slog.Logger, op string, id int64) imageopt.Result {
	res, err := imageopt.Optimize(data, imageopt.Options{MaxDim: imageMaxDim, Quality: imageQuality})
	if err != nil {
		logger.Warn(op+": optimize failed, storing original",
			"id", id, "source_mime", sourceMIME, "err", err)
		return imageopt.Result{
			Data:        data,
			ContentType: sourceMIME,
			Ext:         sourceExt,
			SourceMIME:  sourceMIME,
		}
	}
	return res
}

// healMissingObject re-arms the preview worker for a link-derived key whose
// object is gone.
//
// Only `screenshots/` and `images/` reach the worker: a `notes/` key names
// user-uploaded media that nothing can regenerate, so clearing a reference to
// it would delete the only record that the image was ever there.
//
// Every failure is logged and swallowed. This runs on a READ that has already
// decided its answer — the client gets the same 404 either way — and turning a
// self-healing attempt into a 500 would make a broken thumbnail break the page.
func (h *ScreenshotHandler) healMissingObject(ctx context.Context, key string) {
	if !strings.HasPrefix(key, "screenshots/") && !strings.HasPrefix(key, "images/") {
		return
	}
	id, ok := linkKeyID(key)
	if !ok {
		return
	}
	// Ownership was already asserted by authorizeKey before the read.
	uid := authctx.MustUser(ctx)
	changed, err := h.repo.InvalidateMissingPreview(ctx, uid, id, "/api/files/"+key)
	if err != nil {
		h.logger.Error("proxy file: could not re-arm preview", "link_id", id)
		return
	}
	if !changed {
		// Another request for the same broken card already did it, or the row
		// has since moved on to a different image. Enqueuing here is what would
		// turn a screenful of broken cards into a screenful of captures.
		return
	}
	h.logger.Info("proxy file: stored image is gone, preview re-armed", "link_id", id)
	if h.enqueuer != nil {
		_ = h.enqueuer.Enqueue(id)
	}
}
