package links

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"foldex/internal/folders"
	"foldex/internal/imageopt"
	"foldex/internal/linkimage"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/ports"
)

// FileHandler serves and mutates stored link/note images. It needs only
// object storage, so file routes stay available when no screenshotter
// (Chromium) is configured — capture is the separate ScreenshotHandler.
type FileHandler struct {
	repo       screenshotRepo
	storage    ports.Uploader
	logger     *slog.Logger
	folderGate folders.ContentGate
	// enqueuer re-arms the preview worker when a stored image turns out to be
	// gone. Nil is a supported wiring (the proxy still serves and still
	// invalidates; nothing regenerates until the next boot's requeuePending).
	enqueuer ports.Enqueuer
}

func NewFileHandler(repo screenshotRepo, st ports.Uploader, logger *slog.Logger) *FileHandler {
	return &FileHandler{repo: repo, storage: st, logger: logger}
}

func (h *FileHandler) WithEnqueuer(e ports.Enqueuer) *FileHandler {
	h.enqueuer = e
	return h
}

// WithFolderGate enables the folder-lock check on the MUTATING image
// routes (capture, upload, delete). File READs stay ownership-scoped
// capability URLs: keys carry an unguessable UUID segment, are only
// disclosed inside gated payloads, and <img> tags cannot send the unlock
// header a stricter proxy would demand.
func (h *FileHandler) WithFolderGate(lookup folders.PasswordHashLookup, unlockKey []byte) *FileHandler {
	h.folderGate = folders.NewContentGate(lookup, unlockKey)
	return h
}

func (h *FileHandler) gateFolder(r *http.Request, l Link) error {
	if l.FolderID == nil {
		return nil
	}
	if !h.folderGate.Configured() {
		return folders.ErrGateUnavailable
	}
	return h.folderGate.Check(r.Context(), authctx.MustUser(r.Context()), *l.FolderID, r.Header.Get(folders.UnlockHeader))
}

// ProxyFile proxies a file from object storage to the HTTP client.
// Mounted at GET /api/files/*key. Keys are restricted to the known
// upload/screenshot prefixes so this can't be used to read arbitrary objects
// out of the bucket.
func (h *FileHandler) ProxyFile(w http.ResponseWriter, r *http.Request) {
	h.proxyFile(w, r, chi.URLParam(r, "*"))
}

// ProxyNoteFile is mounted outside required authentication so images embedded
// in the public /n/{slug} page remain readable. The fixed prefix prevents this
// route from reaching id-derived link media, which still requires ownership.
func (h *FileHandler) ProxyNoteFile(w http.ResponseWriter, r *http.Request) {
	key := prefixNotes + chi.URLParam(r, "*")
	if !isValidNoteKey(key) {
		httperr.Write(w, httperr.ErrNotFound)
		return
	}
	h.proxyFile(w, r, key)
}

func (h *FileHandler) proxyFile(w http.ResponseWriter, r *http.Request, key string) {
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
	if strings.HasPrefix(key, prefixNotes) {
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
func (h *FileHandler) authorizeKey(ctx context.Context, key string) error {
	notFound := httperr.New(http.StatusNotFound, "not_found", "file not found")
	switch {
	case strings.HasPrefix(key, prefixNotes):
		if isValidNoteKey(key) {
			return nil
		}
		return notFound
	case strings.HasPrefix(key, prefixScreenshots), strings.HasPrefix(key, prefixImages):
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
// `{prefix}/{digits}.{suffix}`, with the suffix held to the same grammar the
// backup restore enforces (dot-prefixed alphanumeric/hyphen segments): a
// nested path like `screenshots/12.png/extra` must not parse as link 12.
var linkKeySuffixRE = regexp.MustCompile(`^(\.[A-Za-z0-9-]{1,64})+$`)

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
	if !linkKeySuffixRE.MatchString(rest[dot:]) {
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
	const prefix = prefixNotes
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
	return ValidateAbsoluteHTTPURL(pageURL) == nil
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
	for allowed := range imageopt.AllowedUploadMIMEs {
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
func (h *FileHandler) UploadImage(w http.ResponseWriter, r *http.Request) {
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
	link, err := h.repo.Get(r.Context(), uid, id)
	if err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	if err := h.gateFolder(r, link); err != nil {
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
	opt, err := imageopt.AdmitBytes(data, maxSize)
	if err != nil {
		h.logger.Warn("image upload: rejected", "id", id, "err", err)
		status, code, msg := imageopt.AdmitHTTP(err)
		httperr.Write(w, httperr.New(status, code, msg))
		return
	}

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

func (h *FileHandler) deleteUnpublishedLinkImage(key string, id int64) {
	ctx, cancel := context.WithTimeout(context.Background(), captureStorageTimeout)
	defer cancel()
	if err := h.storage.DeleteObject(ctx, key); err != nil {
		h.logger.Warn("unpublished link image cleanup failed", "id", id)
	}
}

func (h *FileHandler) deletePreviousLinkImage(ctx context.Context, previous *string, keepKey string, id int64) {
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
func (h *FileHandler) DeleteImage(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, httperr.ErrBadRequest)
		return
	}
	link, err := h.repo.Get(r.Context(), authctx.MustUser(r.Context()), id)
	if err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	if err := h.gateFolder(r, link); err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	if err := h.repo.ClearOGImage(r.Context(), authctx.MustUser(r.Context()), id); err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	h.logger.Info("image cleared", "id", id)
	w.WriteHeader(http.StatusNoContent)
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
func (h *FileHandler) healMissingObject(ctx context.Context, key string) {
	if !strings.HasPrefix(key, prefixScreenshots) && !strings.HasPrefix(key, prefixImages) {
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
