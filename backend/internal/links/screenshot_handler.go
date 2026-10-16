package links

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"foldex/internal/folders"
	"foldex/internal/imageopt"
	"foldex/internal/linkimage"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/semaphore"
	"foldex/internal/ports"
)

// allowedFilePrefixes is the closed set of object-key prefixes ProxyFile is
// allowed to serve. Keeps the proxy from being a generic read-any-key object-store
// gateway. "notes/" holds inline images uploaded through the note rich-text
// editor (notes.ImageHandler) — ProxyFile is shared infrastructure so notes
// reuses it rather than standing up a second file-serving endpoint.
const (
	prefixScreenshots = "screenshots/"
	prefixImages      = "images/"
	prefixNotes       = "notes/"
)

var allowedFilePrefixes = []string{prefixScreenshots, prefixImages, prefixNotes}

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

// ScreenshotHandler handles screenshot capture; file serving/mutation is
// the embedded FileHandler, so existing call sites keep one mount point.
type ScreenshotHandler struct {
	*FileHandler
	screenshotter Screenshotter
	urlPolicy     URLPolicy
	captureSlots  *semaphore.Keyed[authctx.UserID]
	captureInit   sync.Once
}

// WithEnqueuer wires the preview worker so a missing object can heal itself.
func (h *ScreenshotHandler) WithEnqueuer(e ports.Enqueuer) *ScreenshotHandler {
	h.FileHandler.WithEnqueuer(e)
	return h
}

// WithFolderGate enables the folder-lock check on the MUTATING image routes.
func (h *ScreenshotHandler) WithFolderGate(lookup folders.PasswordHashLookup, unlockKey []byte) *ScreenshotHandler {
	h.FileHandler.WithFolderGate(lookup, unlockKey)
	return h
}

// NewScreenshotHandler creates a ScreenshotHandler. urlPolicy gates
// CaptureAndStore — pass preview.IsPublicURL from main.go. A nil policy is
// treated as "deny all", which fails closed.
func NewScreenshotHandler(repo screenshotRepo, sc Screenshotter, st ports.Uploader, urlPolicy URLPolicy, logger *slog.Logger) *ScreenshotHandler {
	return &ScreenshotHandler{
		FileHandler:   NewFileHandler(repo, st, logger),
		screenshotter: sc,
		urlPolicy:     urlPolicy,
		captureSlots:  semaphore.NewKeyed[authctx.UserID](maxCaptureInFlight, maxCapturePerUser),
	}
}

// CaptureAndStore captures a screenshot of the link's URL, optimizes it, saves
// it to object storage under an operation-owned link key, and publishes that URL.
func (h *ScreenshotHandler) CaptureAndStore(w http.ResponseWriter, r *http.Request) {
	id, uid, link, ok := h.admitCapture(w, r)
	if !ok {
		return
	}
	defer h.releaseCapture(uid)
	png, opt, ok := h.captureOptimized(w, r, id, link)
	if !ok {
		return
	}
	h.publishScreenshot(w, r, uid, id, link, png, opt)
}

func (h *ScreenshotHandler) admitCapture(w http.ResponseWriter, r *http.Request) (int64, authctx.UserID, Link, bool) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return 0, 0, Link{}, false
	}
	uid := authctx.MustUser(r.Context())
	if !h.acquireCapture(uid) {
		w.Header().Set("Retry-After", "5")
		httperr.Write(w, httperr.New(http.StatusTooManyRequests, "screenshot_busy", "too many screenshot captures in flight"))
		return 0, 0, Link{}, false
	}
	link, err := h.repo.Get(r.Context(), uid, id)
	if err != nil {
		h.releaseCapture(uid)
		httperr.Write(w, repositoryHTTPError(err))
		return 0, 0, Link{}, false
	}
	if err := h.gateFolder(r, link); err != nil {
		h.releaseCapture(uid)
		httperr.Write(w, repositoryHTTPError(err))
		return 0, 0, Link{}, false
	}
	if !isHTTPScheme(link.URL) {
		h.releaseCapture(uid)
		h.logger.Warn("screenshot rejected: non-http scheme", "id", id)
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_scheme", "screenshot target must use http or https"))
		return 0, 0, Link{}, false
	}
	return id, uid, link, true
}

func (h *ScreenshotHandler) captureOptimized(w http.ResponseWriter, r *http.Request, id int64, link Link) ([]byte, imageopt.Result, bool) {
	if h.urlPolicy == nil {
		h.logger.Error("screenshot rejected: URLPolicy not configured", "id", id)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "policy_unconfigured", "screenshot policy is not configured"))
		return nil, imageopt.Result{}, false
	}
	capCtx, cancel := context.WithTimeout(r.Context(), captureTimeout)
	defer cancel()
	policyCtx, policyCancel := context.WithTimeout(capCtx, capturePolicyTimeout)
	allowed := h.urlPolicy(policyCtx, link.URL)
	policyCancel()
	if !allowed {
		h.logger.Warn("screenshot rejected: non-public target", "id", id)
		httperr.Write(w, httperr.New(http.StatusBadRequest, "private_target", "screenshot target must resolve to a public address"))
		return nil, imageopt.Result{}, false
	}
	png, err := h.screenshotter.Capture(capCtx, link.URL)
	if err != nil {
		h.logger.Error("screenshot capture failed", "id", id, "reason", screenshotOperationErrorReason(err))
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "screenshot_failed", "failed to capture screenshot"))
		return nil, imageopt.Result{}, false
	}
	opt, err := imageopt.OptimizeForStore(png)
	if err != nil {
		h.logger.Warn("screenshot rejected: optimize failed", "id", id, "err", err)
		status, code, msg := imageopt.RejectHTTP(err)
		httperr.Write(w, httperr.New(status, code, msg))
		return nil, imageopt.Result{}, false
	}
	return png, opt, true
}

func (h *ScreenshotHandler) publishScreenshot(w http.ResponseWriter, r *http.Request, uid authctx.UserID, id int64, link Link, png []byte, opt imageopt.Result) {
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
		h.cleanupUnpublishedScreenshot(id, stored.Key)
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
	httperr.JSON(w, http.StatusOK, map[string]string{"url": stored.URL})
}

func (h *ScreenshotHandler) cleanupUnpublishedScreenshot(id int64, key string) {
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), captureStorageTimeout)
	cleanupErr := h.storage.DeleteObject(cleanupCtx, key)
	cleanupCancel()
	if cleanupErr != nil {
		h.logger.Warn("screenshot orphan cleanup failed", "id", id)
	}
}

// ensureCaptureSlots lazily builds the admission counter for handlers
// assembled as bare struct literals in tests, which never pass through
// NewScreenshotHandler.
func (h *ScreenshotHandler) ensureCaptureSlots() {
	h.captureInit.Do(func() {
		if h.captureSlots == nil {
			h.captureSlots = semaphore.NewKeyed[authctx.UserID](maxCaptureInFlight, maxCapturePerUser)
		}
	})
}

func (h *ScreenshotHandler) acquireCapture(uid authctx.UserID) bool {
	h.ensureCaptureSlots()
	return h.captureSlots.TryAcquire(uid)
}

func (h *ScreenshotHandler) releaseCapture(uid authctx.UserID) {
	h.ensureCaptureSlots()
	h.captureSlots.Release(uid)
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
