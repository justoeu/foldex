package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/backup"
	"foldex/internal/config"
	"foldex/internal/entries"
	"foldex/internal/exporter"
	"foldex/internal/folders"
	"foldex/internal/importer"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/logsafe"
	"foldex/internal/push"
	"foldex/internal/redirect"
	"foldex/internal/settings"
	"foldex/internal/stats"
	"foldex/internal/tags"
)

// Body size ceilings for defaultBodyLimit middleware. net/http.Server has no
// MaxRequestBodyBytes field in Go 1.26, so path-aware MaxBytesReader is the
// defense-in-depth global cap. Per-handler caps (JSON 64 KiB, images 5 MiB)
// still apply on top.
const (
	maxBodyDefault = 1 << 20 // 1 MiB — JSON / non-upload
	maxBodyImage   = 6 << 20 // 5 MiB image + multipart overhead
	maxBodyImport  = 100 << 20
	maxBodyBackup  = 2 << 30 // match backup maxBackupBytes
)

// These interfaces are defined here to keep the router decoupled from the
// concrete storage and screenshot packages (which pull in heavy dependencies).
// The concrete implementations satisfy them at wiring time in main.go.

// Deps groups the runtime dependencies the router needs. Worker is kept as an
// interface (links.Enqueuer) so router tests can inject a no-op without
// pulling in the preview package's Docker-bound dependencies.
type Deps struct {
	Pool           *pgxpool.Pool
	Worker         links.Enqueuer
	Logger         *slog.Logger
	Config         config.Config
	Screenshotter  links.Screenshotter  // optional — nil disables the endpoint
	Storage        links.Uploader       // optional — nil disables the endpoint
	ScreenshotURL  links.URLPolicy      // required iff Screenshotter is set — gates the SSRF surface
	StorageStatter stats.StorageStatter // optional — surfaces bucket usage on /stats/storage
	StorageBucket  backup.StorageBucket // optional — enables /api/backup/* when the object store is up

	// LinkMetadataFetcher gates GET /api/links/url-metadata. When nil the route
	// is still registered but responds 503 — the dialog falls back to manual
	// title entry without breaking the create flow.
	LinkMetadataFetcher links.MetadataFetcher

	// Web Push wiring. Setting PushHandler also mounts /api/push/vapid-key
	// (kept inside /api so it inherits the SHARED_SECRET guard — see CLAUDE.md
	// §4 invariant). Leaving it nil keeps the routes off entirely.
	PushHandler *push.Handler

	// FolderUnlockKey is the HMAC secret for folder-password unlock tokens
	// (see folders.LoadOrGenerateFolderUnlockKey) — shared between the
	// folders handler (mints tokens, gates list(parent_id=X)) and the
	// entries handler (gates list(folder_id=X)) so a token issued by one
	// verifies against the other.
	FolderUnlockKey []byte
}

func New(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(defaultBodyLimit)
	r.Use(slogRequest(d.Logger))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   d.Config.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "X-Foldex-Secret", folders.UnlockHeader},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/healthz", healthz(d.Pool))

	// Redirect/view routes outside /api keep the URL short, avoid CORS
	// preflight, and stay reachable without the SHARED_SECRET guard — both
	// /go/{id-or-slug} (link redirect) and /n/{id-or-slug} (note render) are
	// meant to be shareable the same way.
	notesRepo := notes.NewRepository(d.Pool)
	redirect.NewHandler(links.NewRepository(d.Pool)).Mount(r)
	notes.NewPublicHandler(notesRepo).Mount(r)

	r.Route("/api", func(api chi.Router) {
		if d.Config.SharedSecret != "" {
			api.Use(sharedSecretGuard(d.Config.SharedSecret))
		}
		// Principal resolution. Until the auth stack lands (PR2+), AUTH_ENABLED
		// is false and every request is attributed to the bootstrap admin, so a
		// single-user deployment behaves exactly as it did before migration
		// 000017 — the segmentation is in place and exercised, but invisible.
		api.Use(bootstrapPrincipal(d.Pool, d.Logger))
		api.Route("/tags", tags.NewHandler(tags.NewRepository(d.Pool)).Mount)
		settingsRepo := settings.NewRepository(d.Pool)
		api.Route("/settings", settings.NewHandler(settingsRepo).Mount)
		foldersRepo := folders.NewRepository(d.Pool)
		api.Route("/folders", folders.NewHandler(foldersRepo, d.FolderUnlockKey, settingsRepo).Mount)

		linksRepo := links.NewRepository(d.Pool)
		api.Route("/links", links.NewHandler(linksRepo, d.Worker).
			WithMetadataFetcher(d.LinkMetadataFetcher).
			WithFolderGate(foldersRepo, d.FolderUnlockKey).
			Mount)

		// d.Storage is optional — when nil, Handler.Delete's image cleanup is
		// a no-op (CRUD itself doesn't need storage). The actual upload route
		// (POST /api/notes/images) is mounted further below, gated the same
		// way links' image upload is.
		api.Route("/notes", notes.NewHandler(notesRepo, d.Storage).
			WithFolderGate(foldersRepo, d.FolderUnlockKey).
			Mount)
		api.Route("/entries", entries.NewHandler(entries.NewRepository(d.Pool), foldersRepo, d.FolderUnlockKey).Mount)

		// Screenshot and file-proxy endpoints are only registered when both
		// a Screenshotter and Storage implementation are provided.
		if d.Screenshotter != nil && d.Storage != nil {
			// Boot-time validation: mounting the screenshot endpoint without
			// the URL policy wired would still fail closed at request time,
			// but a hard exit at startup surfaces the misconfig immediately
			// instead of leaving every request returning 500 in production.
			if d.ScreenshotURL == nil {
				d.Logger.Error("server: Screenshotter is set but ScreenshotURL is nil — refusing to mount /api/links/{id}/screenshot without an SSRF gate")
				panic("server: Screenshotter is set but ScreenshotURL is nil — refusing to mount /api/links/{id}/screenshot without an SSRF gate")
			}
			sh := links.NewScreenshotHandler(linksRepo, d.Screenshotter, d.Storage, d.ScreenshotURL, d.Logger)
			api.Post("/links/{id}/screenshot", sh.CaptureAndStore)
			api.Post("/links/{id}/image", sh.UploadImage)
			api.Delete("/links/{id}/image", sh.DeleteImage)
			api.Get("/files/*", sh.ProxyFile)

			// Note inline-image upload lives in this same gate (rather than its
			// own `d.Storage != nil` check) so it can never be mounted without
			// ProxyFile also being mounted — an uploaded note image would
			// otherwise have nowhere to be served back from.
			nih := notes.NewImageHandler(d.Storage, d.Logger)
			api.Post("/notes/images", nih.Upload)
		}

		api.Route("/import", importer.NewHandler(d.Pool, d.Worker).Mount)
		api.Route("/export", exporter.NewHandler(d.Pool).Mount)
		statsHandler := stats.NewHandler(stats.NewRepository(d.Pool))
		if d.StorageStatter != nil {
			statsHandler = statsHandler.WithStorage(d.StorageStatter)
		}
		api.Route("/stats", statsHandler.Mount)
		if d.StorageBucket != nil {
			api.Route("/backup", backup.NewHandler(backup.NewService(d.Pool, d.StorageBucket, d.Logger), d.Logger).Mount)
		}
		if d.PushHandler != nil {
			api.Route("/push", d.PushHandler.Mount)
		}
	})

	return r
}

// bootstrapPrincipal attributes every request to the single bootstrap admin.
//
// It exists ONLY while AUTH_ENABLED is false (PR1–PR3 of ADR-30). Repositories
// now require an explicit owner, and a zero UserID would match no rows — so
// something has to supply one until real authentication does. Resolution is
// "the oldest admin", which on an upgraded install is the row migration 000017
// created and adopted every pre-existing row into.
//
// The lookup is cached after the first success: it is the same row on every
// request, and a per-request SELECT on the hot path buys nothing. A failure is
// NOT cached, so a database that comes up late recovers on the next request.
func bootstrapPrincipal(pool *pgxpool.Pool, logger *slog.Logger) func(http.Handler) http.Handler {
	var (
		mu     sync.Mutex
		cached authctx.UserID
	)
	resolve := func(ctx context.Context) (authctx.UserID, error) {
		mu.Lock()
		defer mu.Unlock()
		if cached != 0 {
			return cached, nil
		}
		var id int64
		if err := pool.QueryRow(ctx,
			`SELECT id FROM app_user WHERE role = 'admin' ORDER BY id LIMIT 1`).Scan(&id); err != nil {
			return 0, err
		}
		cached = authctx.UserID(id)
		return cached, nil
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, err := resolve(r.Context())
			if err != nil {
				logger.Error("bootstrap principal unavailable", "err", err)
				httperr.Write(w, httperr.New(http.StatusServiceUnavailable,
					"principal_unavailable", "no bootstrap administrator is available"))
				return
			}
			next.ServeHTTP(w, r.WithContext(authctx.WithPrincipal(r.Context(), authctx.Principal{
				UserID: uid,
				Role:   authctx.RoleAdmin,
				Via:    authctx.ViaSession,
			})))
		})
	}
}

func healthz(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		// Healthz is intentionally public (no SHARED_SECRET gate) so external
		// probes can check liveness. Surface only the boolean state — the raw
		// `pool.Ping` error can carry internal host/DSN text that doesn't
		// belong in a response an unauthenticated caller can read.
		body := map[string]any{"status": "ok", "db": "ok"}
		status := http.StatusOK
		if err := pool.Ping(ctx); err != nil {
			body["status"] = "degraded"
			body["db"] = "unreachable"
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
}

func sharedSecretGuard(expected string) func(http.Handler) http.Handler {
	// HMAC both sides to a fixed-length digest before comparing. The raw
	// subtle.ConstantTimeCompare returns 0 immediately when the lengths
	// differ, leaking the secret length to a remote timing attacker.
	// HMAC-SHA256 always yields 32 bytes, so the compare is now length-
	// uniform. The HMAC key is fixed — we're not authenticating a payload,
	// just normalizing the inputs to a constant size before comparison.
	const compareKey = "foldex/shared-secret/compare"
	expectedSum := hmac256(compareKey, expected)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get("X-Foldex-Secret")
			gotSum := hmac256(compareKey, got)
			if !hmac.Equal(gotSum, expectedSum) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"invalid or missing secret"}}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func hmac256(key, msg string) []byte {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

func slogRequest(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("http",
				"method", r.Method,
				"path_class", logsafe.HTTPPath(r.URL.Path),
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// defaultBodyLimit applies a path-aware MaxBytesReader before handlers run.
// Go 1.26's http.Server has no MaxRequestBodyBytes; this is the global
// absolute ceiling so a future handler that forgets its own cap cannot
// body-bomb within ReadTimeout.
func defaultBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, bodyLimitForPath(r.URL.Path))
		}
		next.ServeHTTP(w, r)
	})
}

func bodyLimitForPath(path string) int64 {
	switch {
	case strings.HasPrefix(path, "/api/backup"):
		return maxBodyBackup
	case strings.HasPrefix(path, "/api/import"):
		return maxBodyImport
	case strings.HasSuffix(path, "/image") || strings.HasSuffix(path, "/images") || strings.Contains(path, "/notes/images"):
		return maxBodyImage
	default:
		return maxBodyDefault
	}
}
