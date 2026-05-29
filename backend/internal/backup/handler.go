package backup

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/httperr"
)

type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, logger: logger}
}

func (h *Handler) Mount(r chi.Router) {
	r.Post("/", h.export)
	r.Post("/validate", h.validate)
	r.Post("/restore", h.restore)
}

// ────────────────────────────────────────────────────────────────────────────
// POST /api/backup — stream ZIP

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	filename := fmt.Sprintf("foldex-backup-%s.zip", stamp)

	// Streaming export. The Service computes counts up front (snapshot read
	// + bucket listings under REPEATABLE READ) and calls onCountsReady
	// BEFORE the first zip byte; the hook flushes response headers, then
	// every entry streams straight to w. X-Foldex-Backup-Duration-Ms used to
	// land in the headers but the duration is only known after the zip is
	// closed — clients that need it can derive from request start.
	headersWritten := false
	rep, err := h.svc.Export(r.Context(), w, func(c Counts) error {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		w.Header().Set("X-Foldex-Backup-Filename", filename)
		w.Header().Set("X-Foldex-Backup-Counts-Links", fmt.Sprintf("%d", c.Links))
		w.Header().Set("X-Foldex-Backup-Counts-Files", fmt.Sprintf("%d", c.Files))
		w.WriteHeader(http.StatusOK)
		headersWritten = true
		return nil
	})
	if err != nil {
		// If headers already shipped, the response is in progress; the best
		// we can do is log and let the chunked stream truncate (the client
		// zip parser will surface a corrupt-archive error).
		h.logger.Error("backup export failed", "err", err, "headers_written", headersWritten)
		if !headersWritten {
			httperr.Write(w, httperr.New(http.StatusInternalServerError, "export_failed", "failed to produce backup"))
		}
		return
	}
	h.logger.Info("backup export ok",
		"filename", filename,
		"links", rep.Counts.Links, "files", rep.Counts.Files,
		"duration_ms", rep.DurationMs,
	)
}

// ────────────────────────────────────────────────────────────────────────────
// POST /api/backup/validate

func (h *Handler) validate(w http.ResponseWriter, r *http.Request) {
	zr, cleanup, err := readZipFromRequest(w, r)
	if err != nil {
		if errors.Is(err, ErrPayloadTooLarge) {
			httperr.Write(w, httperr.New(http.StatusRequestEntityTooLarge, "payload_too_large", err.Error()))
			return
		}
		httperr.JSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "bad_zip", "message": err.Error()}})
		return
	}
	defer cleanup()
	v, err := h.svc.Validate(r.Context(), zr)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, v)
}

// ────────────────────────────────────────────────────────────────────────────
// POST /api/backup/restore?mode=…

func (h *Handler) restore(w http.ResponseWriter, r *http.Request) {
	modeStr := r.URL.Query().Get("mode")
	if modeStr == "" {
		modeStr = string(ModeSkip)
	}
	mode := ConflictMode(modeStr)
	if !mode.Valid() {
		httperr.JSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "bad_mode", "message": fmt.Sprintf("mode %q is not one of wipe|skip|duplicate", modeStr)}})
		return
	}

	zr, cleanup, err := readZipFromRequest(w, r)
	if err != nil {
		if errors.Is(err, ErrPayloadTooLarge) {
			httperr.Write(w, httperr.New(http.StatusRequestEntityTooLarge, "payload_too_large", err.Error()))
			return
		}
		httperr.JSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "bad_zip", "message": err.Error()}})
		return
	}
	defer cleanup()
	rep, err := h.svc.Restore(r.Context(), zr, mode)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, rep)
}

// ────────────────────────────────────────────────────────────────────────────
// readZipFromRequest streams either a raw application/zip body or a multipart
// upload with a `file` field to a temp file on disk, then opens it as a
// zip.Reader (which only needs a ReaderAt). Streaming to disk keeps heap usage
// bounded at O(1) regardless of backup size — a multi-GB upload used to
// allocate the same multi-GB on the heap.

const maxBackupBytes = int64(2 << 30) // 2 GiB

func readZipFromRequest(w http.ResponseWriter, r *http.Request) (*zip.Reader, func(), error) {
	ct := r.Header.Get("Content-Type")
	noop := func() {}

	// Hard cap on the entire request body, regardless of transport (raw zip
	// or multipart). Applies to both branches below — multipart parts that
	// would individually pass maxBackupBytes still trip this when summed.
	// Passing the real ResponseWriter (not nil) lets the cap surface as a
	// 413 instead of a 500 when streamToTempZip wraps the limit error.
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupBytes)

	if strings.HasPrefix(ct, "application/zip") {
		return streamToTempZip(r.Body)
	}

	// multipart/form-data
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, noop, fmt.Errorf("expected application/zip or multipart/form-data: %w", err)
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, noop, fmt.Errorf("multipart read: %w", err)
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		defer part.Close()
		return streamToTempZip(io.LimitReader(part, maxBackupBytes))
	}
	return nil, noop, fmt.Errorf("no `file` part in multipart upload")
}

// ErrPayloadTooLarge is returned by streamToTempZip when the body exceeded
// maxBackupBytes. Callers map it to 413 instead of a generic 500.
var ErrPayloadTooLarge = fmt.Errorf("backup: upload exceeds %d-byte limit", maxBackupBytes)

// streamToTempZip copies src to a temp file, opens it as a zip.Reader, and
// returns a cleanup that closes + removes the temp file. The temp file lives
// only for the duration of the restore — successful and failed paths both go
// through the cleanup closure. Permissions default to 0600 via os.CreateTemp.
func streamToTempZip(src io.Reader) (*zip.Reader, func(), error) {
	tmp, err := os.CreateTemp("", "foldex-backup-*.zip")
	if err != nil {
		return nil, func() {}, fmt.Errorf("create temp: %w", err)
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	n, err := io.Copy(tmp, src)
	if err != nil {
		cleanup()
		// http.MaxBytesError signals the body cap was tripped — surface as a
		// typed sentinel so the handler can return 413 instead of 500.
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, func() {}, ErrPayloadTooLarge
		}
		return nil, func() {}, fmt.Errorf("copy upload to temp: %w", err)
	}
	if n == 0 {
		cleanup()
		return nil, func() {}, fmt.Errorf("upload is empty")
	}
	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("parse zip: %w", err)
	}
	return zr, cleanup, nil
}

