package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/backup"
	"foldex/internal/config"
	"foldex/internal/exporter"
	"foldex/internal/folders"
	"foldex/internal/importer"
	"foldex/internal/links"
	"foldex/internal/push"
	"foldex/internal/redirect"
	"foldex/internal/stats"
	"foldex/internal/tags"
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
	StorageBucket  backup.StorageBucket // optional — enables /api/backup/* when MinIO is up

	// LinkMetadataFetcher gates GET /api/links/url-metadata. When nil the route
	// is still registered but responds 503 — the dialog falls back to manual
	// title entry without breaking the create flow.
	LinkMetadataFetcher links.MetadataFetcher

	// Web Push wiring. Setting PushHandler also mounts /api/push/vapid-key
	// (kept inside /api so it inherits the SHARED_SECRET guard — see CLAUDE.md
	// §4 invariant). Leaving it nil keeps the routes off entirely.
	PushHandler *push.Handler
}

func New(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(slogRequest(d.Logger))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   d.Config.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "X-Foldex-Secret"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/healthz", healthz(d.Pool))

	// Redirect outside /api keeps the URL short and avoids CORS preflight.
	redirect.NewHandler(links.NewRepository(d.Pool)).Mount(r)

	r.Route("/api", func(api chi.Router) {
		if d.Config.SharedSecret != "" {
			api.Use(sharedSecretGuard(d.Config.SharedSecret))
		}
		api.Route("/tags", tags.NewHandler(tags.NewRepository(d.Pool)).Mount)
		api.Route("/folders", folders.NewHandler(folders.NewRepository(d.Pool)).Mount)

		linksRepo := links.NewRepository(d.Pool)
		api.Route("/links", links.NewHandler(linksRepo, d.Worker).WithMetadataFetcher(d.LinkMetadataFetcher).Mount)

		// Screenshot and file-proxy endpoints are only registered when both
		// a Screenshotter and Storage implementation are provided.
		if d.Screenshotter != nil && d.Storage != nil {
			// Boot-time validation: mounting the screenshot endpoint without
			// the URL policy wired would still fail closed at request time,
			// but a hard panic at startup surfaces the misconfig immediately
			// instead of leaving every request returning 500 in production.
			if d.ScreenshotURL == nil {
				panic("server: Screenshotter is set but ScreenshotURL is nil — refusing to mount /api/links/{id}/screenshot without an SSRF gate")
			}
			sh := links.NewScreenshotHandler(linksRepo, d.Screenshotter, d.Storage, d.ScreenshotURL, d.Logger)
			api.Post("/links/{id}/screenshot", sh.CaptureAndStore)
			api.Post("/links/{id}/image", sh.UploadImage)
			api.Delete("/links/{id}/image", sh.DeleteImage)
			api.Get("/files/*", sh.ProxyFile)
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

func healthz(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		body := map[string]any{"status": "ok", "db": "ok"}
		status := http.StatusOK
		if err := pool.Ping(ctx); err != nil {
			body["status"] = "degraded"
			body["db"] = err.Error()
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
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}
