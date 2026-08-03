package notes

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"foldex/internal/imageopt"
	"foldex/internal/links"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/logsafe"
)

// allowedUploadMIMEs is the single imageopt allowlist (ARCH-ATL-009).
var allowedUploadMIMEs = imageopt.AllowedUploadMIMEs

const (
	imageMaxDim  = 1024
	imageQuality = 82
	// maxImageSize mirrors links.ScreenshotHandler.UploadImage's 5 MiB cap —
	// a single pasted screenshot comfortably fits under it once downscaled.
	maxImageSize = 5 << 20
)

// ImageHandler handles inline image uploads for the note rich-text editor.
// Split from Handler (same way links splits ScreenshotHandler out) since it
// has a different dependency (object storage) that's only wired when the object store
// is configured.
type ImageHandler struct {
	storage links.Uploader
	logger  *slog.Logger
}

func NewImageHandler(storage links.Uploader, logger *slog.Logger) *ImageHandler {
	return &ImageHandler{storage: storage, logger: logger}
}

// Upload accepts a multipart upload (field "image"), optimizes it, and
// stores it under notes/<uuid>.<ext> — UUID-keyed (not note-id-keyed)
// because Tiptap uploads images on paste/drop, which can happen before a new
// note has been saved (no id exists yet). Returns the proxy URL immediately;
// the note row itself never references this endpoint, the returned URL is
// just inserted into the editor's document and persisted as part of
// body_html on save.
func (h *ImageHandler) Upload(w http.ResponseWriter, r *http.Request) {
	// Bound body first (global + this handler). ParseMultipartForm maxMemory
	// is deliberately ≤ maxImageSize so large parts spill to temp files and
	// are cleaned via RemoveAll — addresses gosec G120 (unbounded form parse).
	r.Body = http.MaxBytesReader(w, r.Body, maxImageSize)
	const maxMemory = 1 << 20 // 1 MiB in RAM; rest on disk
	if err := r.ParseMultipartForm(maxMemory); err != nil {
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

	data, err := io.ReadAll(io.LimitReader(file, maxImageSize+1))
	if err != nil {
		h.logger.Error("note image upload: read failed", "err", err)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "read_failed", "failed to read uploaded file"))
		return
	}
	if len(data) == 0 {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "empty_file", "uploaded file is empty"))
		return
	}
	if int64(len(data)) > maxImageSize {
		httperr.Write(w, httperr.New(http.StatusRequestEntityTooLarge, "too_large", "image exceeds 5MB limit"))
		return
	}

	// Detect MIME from the actual bytes — never trust the client-supplied
	// Content-Type (same rationale as links.UploadImage).
	detected := http.DetectContentType(data)
	srcExt, ok := allowedUploadMIMEs[detected]
	if !ok {
		h.logger.Warn("note image upload: rejected MIME", "detected", logsafe.String(detected))
		httperr.Write(w, httperr.New(http.StatusUnsupportedMediaType, "invalid_mime", "file must be a PNG, JPEG, GIF, or WebP image"))
		return
	}

	opt, err := imageopt.Optimize(data, imageopt.Options{MaxDim: imageMaxDim, Quality: imageQuality})
	if err != nil {
		h.logger.Warn("note image upload: optimize failed, storing original", "source_mime", logsafe.String(detected), "err", err)
		opt = imageopt.Result{Data: data, ContentType: detected, Ext: srcExt, SourceMIME: detected}
	}

	key := fmt.Sprintf("notes/%s.%s", uuid.NewString(), opt.Ext)
	if err := h.storage.Upload(r.Context(), key, opt.Data, opt.ContentType); err != nil {
		h.logger.Error("note image upload: storage upload failed", "key", key, "err", err)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "upload_failed", "failed to store image"))
		return
	}

	h.logger.Info("note image uploaded",
		"key", key, "source_mime", opt.SourceMIME,
		"source_bytes", len(data), "stored_bytes", len(opt.Data),
		"resized", opt.Resized, "reencoded", opt.Reencoded,
	)
	httperr.JSON(w, http.StatusOK, map[string]string{"url": "/api/files/" + key})
}
