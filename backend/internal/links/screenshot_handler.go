package links

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/httperr"
)

// allowedFilePrefixes is the closed set of object-key prefixes ProxyFile is
// allowed to serve. Keeps the proxy from being a generic read-any-key MinIO
// gateway.
var allowedFilePrefixes = []string{"screenshots/", "images/"}

// allowedUploadMIMEs is the closed set of MIME types accepted by UploadImage,
// detected from the actual upload bytes (NOT from the client-supplied header).
// SVG is intentionally excluded — it can carry executable script.
var allowedUploadMIMEs = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// Screenshotter captures a URL and returns PNG bytes.
type Screenshotter interface {
	Capture(ctx context.Context, pageURL string) ([]byte, error)
}

// Uploader stores bytes to object storage.
type Uploader interface {
	Upload(ctx context.Context, key string, data []byte, contentType string) error
	GetObject(ctx context.Context, key string) ([]byte, string, error)
}

// ScreenshotHandler handles screenshot capture and file proxy routes.
type ScreenshotHandler struct {
	repo         *Repository
	screenshotter Screenshotter
	storage      Uploader
	logger       *slog.Logger
}

// NewScreenshotHandler creates a ScreenshotHandler.
func NewScreenshotHandler(repo *Repository, sc Screenshotter, st Uploader, logger *slog.Logger) *ScreenshotHandler {
	return &ScreenshotHandler{
		repo:         repo,
		screenshotter: sc,
		storage:      st,
		logger:       logger,
	}
}

// CaptureAndStore captures a screenshot of the link's URL, saves it to
// object storage under screenshots/{id}.png and returns the proxy URL.
func (h *ScreenshotHandler) CaptureAndStore(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		httperr.Write(w, err)
		return
	}

	link, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httperr.Write(w, err)
		return
	}

	png, err := h.screenshotter.Capture(r.Context(), link.URL)
	if err != nil {
		h.logger.Error("screenshot capture failed", "id", id, "url", link.URL, "err", err)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "screenshot_failed", fmt.Sprintf("capture failed: %v", err)))
		return
	}

	key := fmt.Sprintf("screenshots/%d.png", id)
	if err := h.storage.Upload(r.Context(), key, png, "image/png"); err != nil {
		h.logger.Error("screenshot upload failed", "id", id, "key", key, "err", err)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "upload_failed", "failed to store screenshot"))
		return
	}

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

// UploadImage accepts a multipart upload (field "image"), stores it in object
// storage under images/{id}.{ext}, and updates the link's og_image_url.
// Mounted at POST /api/links/{id}/image.
func (h *ScreenshotHandler) UploadImage(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		httperr.Write(w, err)
		return
	}

	const maxSize = 20 << 20 // 20 MB — comfortable headroom for phone-camera shots
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
		httperr.Write(w, httperr.New(http.StatusRequestEntityTooLarge, "too_large", "image exceeds 20MB limit"))
		return
	}

	// Detect MIME from the actual bytes — never trust the client-supplied
	// Content-Type. Stops HTML/SVG/script files smuggled in with an
	// `image/png` declaration that would later be served back as that MIME
	// (stored XSS via the ProxyFile cache).
	detected := http.DetectContentType(data)
	ext, ok := allowedUploadMIMEs[detected]
	if !ok {
		h.logger.Warn("image upload: rejected MIME", "id", id, "detected", detected)
		httperr.Write(w, httperr.New(http.StatusUnsupportedMediaType, "invalid_mime", "file must be a PNG, JPEG, GIF, or WebP image"))
		return
	}
	key := fmt.Sprintf("images/%d.%s", id, ext)

	if err := h.storage.Upload(r.Context(), key, data, detected); err != nil {
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

	h.logger.Info("image uploaded", "id", id, "key", key)
	httperr.JSON(w, http.StatusOK, map[string]string{"url": proxyURL})
}

// DeleteImage clears the og_image_url for a link (does not delete from storage).
func (h *ScreenshotHandler) DeleteImage(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
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

// mimeToExt maps common image MIME types to file extensions.
// Kept for callers that still need the broader mapping (e.g. arriving via the
// preview pipeline). UploadImage no longer uses it — it pins to the closed set
// in allowedUploadMIMEs.
func mimeToExt(mime string) string {
	switch mime {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		// Fall back to the subtype (e.g. "image/avif" → "avif").
		if idx := strings.Index(mime, "/"); idx >= 0 {
			return mime[idx+1:]
		}
		return "bin"
	}
}
