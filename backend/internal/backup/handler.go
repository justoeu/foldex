package backup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

	// Buffer the ZIP so we can set the X-Foldex-Backup-* headers BEFORE the
	// first byte of the body is flushed. The streaming alternative was wrong:
	// the response headers were lost because Export's first Write already
	// committed them. For a personal-scale backup this is a few MB to ~hundreds
	// of MB at the extreme — well within memory budget.
	buf := &bytes.Buffer{}
	rep, err := h.svc.Export(r.Context(), buf)
	if err != nil {
		h.logger.Error("backup export failed", "err", err)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "export_failed", "failed to produce backup"))
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
	w.Header().Set("X-Foldex-Backup-Filename", filename)
	w.Header().Set("X-Foldex-Backup-Counts-Links", fmt.Sprintf("%d", rep.Counts.Links))
	w.Header().Set("X-Foldex-Backup-Counts-Files", fmt.Sprintf("%d", rep.Counts.Files))
	w.Header().Set("X-Foldex-Backup-Duration-Ms", fmt.Sprintf("%d", rep.DurationMs))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		h.logger.Error("backup export: write response failed", "err", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// POST /api/backup/validate

func (h *Handler) validate(w http.ResponseWriter, r *http.Request) {
	zr, cleanup, err := readZipFromRequest(r)
	if err != nil {
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

	zr, cleanup, err := readZipFromRequest(r)
	if err != nil {
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
// readZipFromRequest reads either a raw application/zip body or a multipart
// upload with a `file` field. Buffers in memory up to a 2GB cap.

const maxBackupBytes = int64(2 << 30) // 2 GiB

func readZipFromRequest(r *http.Request) (*zip.Reader, func(), error) {
	ct := r.Header.Get("Content-Type")
	noop := func() {}

	// Hard cap on the entire request body, regardless of transport (raw zip
	// or multipart). Without this, a multipart upload with many parts could
	// individually respect maxBackupBytes per part but still blow through it
	// in aggregate.
	r.Body = http.MaxBytesReader(nil, r.Body, maxBackupBytes)

	if len(ct) >= len("application/zip") && ct[:len("application/zip")] == "application/zip" {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, noop, fmt.Errorf("read body: %w", err)
		}
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, noop, fmt.Errorf("parse zip: %w", err)
		}
		return zr, noop, nil
	}

	// multipart/form-data
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, noop, fmt.Errorf("expected application/zip or multipart/form-data: %w", err)
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, noop, fmt.Errorf("multipart read: %w", err)
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		buf := &bytes.Buffer{}
		if _, err := io.Copy(buf, io.LimitReader(part, maxBackupBytes)); err != nil {
			part.Close()
			return nil, noop, fmt.Errorf("multipart copy: %w", err)
		}
		part.Close()
		zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			return nil, noop, fmt.Errorf("parse zip: %w", err)
		}
		return zr, noop, nil
	}
	return nil, noop, fmt.Errorf("no `file` part in multipart upload")
}

// JSON helper for non-error JSON responses.
var _ = json.Marshal
