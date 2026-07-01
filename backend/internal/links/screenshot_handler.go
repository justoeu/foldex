package links

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"foldex/internal/imageopt"
	"foldex/internal/pkg/httperr"
)

// allowedFilePrefixes is the closed set of object-key prefixes ProxyFile is
// allowed to serve. Keeps the proxy from being a generic read-any-key MinIO
// gateway. "notes/" holds inline images uploaded through the note rich-text
// editor (notes.ImageHandler) — ProxyFile is shared infrastructure so notes
// reuses it rather than standing up a second file-serving endpoint.
var allowedFilePrefixes = []string{"screenshots/", "images/", "notes/"}

// allowedUploadMIMEs is the closed set of MIME types accepted by UploadImage,
// detected from the actual upload bytes (NOT from the client-supplied header).
// SVG is intentionally excluded — it can carry executable script.
var allowedUploadMIMEs = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

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

// Uploader stores bytes to object storage.
type Uploader interface {
	Upload(ctx context.Context, key string, data []byte, contentType string) error
	GetObject(ctx context.Context, key string) ([]byte, string, error)
	DeleteObject(ctx context.Context, key string) error
}

// screenshotRepo is the slice of the Repository that ScreenshotHandler needs.
// Defined as an interface so unit tests can inject a fake without a real DB.
type screenshotRepo interface {
	Get(ctx context.Context, id int64) (Link, error)
	UpdateOGImage(ctx context.Context, id int64, imageURL string) error
	ClearOGImage(ctx context.Context, id int64) error
}

// ScreenshotHandler handles screenshot capture and file proxy routes.
type ScreenshotHandler struct {
	repo          screenshotRepo
	screenshotter Screenshotter
	storage       Uploader
	urlPolicy     URLPolicy
	logger        *slog.Logger
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
	}
}

// CaptureAndStore captures a screenshot of the link's URL, optimizes it, saves
// it to object storage under screenshots/{id}.{ext}, and returns the proxy URL.
func (h *ScreenshotHandler) CaptureAndStore(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}

	link, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httperr.Write(w, err)
		return
	}

	// SSRF gate. Without this, Chromium happily navigates to file://,
	// 169.254.169.254 (IMDS), 127.0.0.1, RFC1918 hosts, etc., and the
	// resulting screenshot would be served back to the caller via
	// /api/files/screenshots/{id} — a read-anywhere primitive.
	if !isHTTPScheme(link.URL) {
		h.logger.Warn("screenshot rejected: non-http scheme", "id", id, "url", link.URL)
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
	if !h.urlPolicy(r.Context(), link.URL) {
		h.logger.Warn("screenshot rejected: non-public target", "id", id, "url", link.URL)
		httperr.Write(w, httperr.New(http.StatusBadRequest, "private_target", "screenshot target must resolve to a public address"))
		return
	}

	png, err := h.screenshotter.Capture(r.Context(), link.URL)
	if err != nil {
		// Log the underlying error with full detail; the wire response gets
		// a generic message — Chromium errors can include local binary paths
		// / system state that shouldn't reach a (possibly remote) caller.
		h.logger.Error("screenshot capture failed", "id", id, "url", link.URL, "err", err)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "screenshot_failed", "failed to capture screenshot"))
		return
	}

	opt := optimizeOrFallback(png, "image/png", "png", h.logger, "screenshot", id)

	key := fmt.Sprintf("screenshots/%d.%s", id, opt.Ext)
	h.purgeLegacyVariants(r.Context(), "screenshots", id, opt.Ext)
	if err := h.storage.Upload(r.Context(), key, opt.Data, opt.ContentType); err != nil {
		h.logger.Error("screenshot upload failed", "id", id, "key", key, "err", err)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "upload_failed", "failed to store screenshot"))
		return
	}

	h.logger.Info("screenshot stored",
		"id", id, "key", key,
		"source_bytes", len(png), "stored_bytes", len(opt.Data),
		"resized", opt.Resized, "reencoded", opt.Reencoded,
	)
	httperr.JSON(w, http.StatusOK, map[string]string{
		"url": "/api/files/" + key,
	})
}

// ProxyFile proxies a file from object storage to the HTTP client.
// Mounted at GET /api/files/*key. Keys are restricted to the known
// upload/screenshot prefixes so this can't be used to read arbitrary objects
// out of the bucket.
func (h *ScreenshotHandler) ProxyFile(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "*")
	if !isAllowedKey(key) {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_key", "key is required and must be under screenshots/ or images/"))
		return
	}

	data, contentType, err := h.storage.GetObject(r.Context(), key)
	if err != nil {
		h.logger.Error("proxy file: get object failed", "key", key, "err", err)
		httperr.Write(w, httperr.New(http.StatusNotFound, "not_found", "file not found"))
		return
	}

	// Never trust the stored content-type for the response — pin it to what
	// http.DetectContentType reads from the actual bytes. Stops a malicious
	// upload that slipped past UploadImage (or arrived via another vector)
	// from being served as text/html and executing in the browser.
	detected := http.DetectContentType(data)
	if !isAllowedServeMIME(detected) {
		h.logger.Warn("proxy file: refusing to serve non-image content", "key", key, "detected", detected, "stored", contentType)
		httperr.Write(w, httperr.New(http.StatusUnsupportedMediaType, "unsupported_media", "stored object is not a supported image"))
		return
	}
	w.Header().Set("Content-Type", detected)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
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
// (downscale + JPEG re-encode), stores the result in object storage under
// images/{id}.{ext}, and updates the link's og_image_url.
// Mounted at POST /api/links/{id}/image.
func (h *ScreenshotHandler) UploadImage(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
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
	if err := r.ParseMultipartForm(maxSize); err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_multipart", "request too large or malformed"))
		return
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
		h.logger.Warn("image upload: rejected MIME", "id", id, "detected", detected)
		httperr.Write(w, httperr.New(http.StatusUnsupportedMediaType, "invalid_mime", "file must be a PNG, JPEG, GIF, or WebP image"))
		return
	}

	opt := optimizeOrFallback(data, detected, srcExt, h.logger, "image upload", id)

	key := fmt.Sprintf("images/%d.%s", id, opt.Ext)
	h.purgeLegacyVariants(r.Context(), "images", id, opt.Ext)
	if err := h.storage.Upload(r.Context(), key, opt.Data, opt.ContentType); err != nil {
		h.logger.Error("image upload: storage upload failed", "id", id, "key", key, "err", err)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "upload_failed", "failed to store image"))
		return
	}

	proxyURL := "/api/files/" + key
	if err := h.repo.UpdateOGImage(r.Context(), id, proxyURL); err != nil {
		h.logger.Error("image upload: db update failed", "id", id, "err", err)
		httperr.Write(w, err)
		return
	}

	h.logger.Info("image uploaded",
		"id", id, "key", key,
		"source_mime", opt.SourceMIME,
		"source_bytes", len(data), "stored_bytes", len(opt.Data),
		"resized", opt.Resized, "reencoded", opt.Reencoded,
	)
	httperr.JSON(w, http.StatusOK, map[string]string{"url": proxyURL})
}

// DeleteImage clears the og_image_url for a link (does not delete from storage).
func (h *ScreenshotHandler) DeleteImage(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, httperr.ErrBadRequest)
		return
	}
	if err := h.repo.ClearOGImage(r.Context(), id); err != nil {
		httperr.Write(w, err)
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

// purgeLegacyVariants removes every sibling-extension object for the same id
// under the given prefix except the one we just wrote. Keeps MinIO from
// accumulating orphans when a link previously had a .png/.gif/.webp upload
// and the new upload writes .jpg (or vice versa via the fallback path).
// DeleteObject is idempotent — NoSuchKey is treated as success.
func (h *ScreenshotHandler) purgeLegacyVariants(ctx context.Context, prefix string, id int64, keepExt string) {
	for _, ext := range allowedUploadMIMEs {
		if ext == keepExt {
			continue
		}
		key := fmt.Sprintf("%s/%d.%s", prefix, id, ext)
		if err := h.storage.DeleteObject(ctx, key); err != nil {
			h.logger.Warn("purge legacy variant failed",
				"key", key, "err", err)
		}
	}
}
