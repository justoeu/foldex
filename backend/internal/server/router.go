package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/auth"
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

	// Auth stack (ADR-30). All three are optional so router tests can build a
	// router without a mailer or a session store; leaving them nil simply keeps
	// /api/auth and /api/admin unmounted.
	AuthHandler    *auth.Handler
	AdminHandler   *auth.AdminHandler
	AuthMiddleware *auth.Middleware

	// FolderUnlockKey is the HMAC secret for folder-password unlock tokens
	// (see folders.LoadOrGenerateFolderUnlockKey) — shared between the
	// folders handler (mints tokens, gates list(parent_id=X)) and the
	// entries handler (gates list(folder_id=X)) so a token issued by one
	// verifies against the other.
	FolderUnlockKey []byte
}

func New(d Deps) http.Handler {
	r := chi.NewRouter()
	// NOT chi's middleware.RealIP: that one rewrites RemoteAddr from
	// X-Forwarded-For unconditionally, which is correct behind nginx and
	// forgeable on a direct bind. See realip.go.
	trustedNets, badProxies := parseTrustedProxies(d.Config.TrustedProxyIPs)
	for _, entry := range badProxies {
		d.Logger.Error("TRUSTED_PROXY_IPS: ignoring unparseable entry — "+
			"the proxy it names is NOT trusted, so client addresses behind it "+
			"will be recorded as the proxy's own", "entry", logsafe.String(entry))
	}
	// Empty on a network-reachable bind almost always means a proxy in front
	// that nobody told us about — and then EVERY request is attributed to that
	// proxy, collapsing the per-IP login bucket into one global budget where
	// 20 bad passwords lock out every user at once. Loopback binds are silent:
	// there is nothing in front of them.
	if len(trustedNets) == 0 && !isLoopbackBind(d.Config.BindAddr) {
		d.Logger.Warn("TRUSTED_PROXY_IPS is empty on a non-loopback bind — if a reverse " +
			"proxy sits in front, every request will be attributed to it and the per-IP " +
			"rate limits will apply to all users as one")
	}
	r.Use(trustedProxyRealIP(trustedNets))
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(defaultBodyLimit)
	r.Use(slogRequest(d.Logger))
	// AllowCredentials is required from PR2 on: the session lives in cookies, so
	// a cross-origin SPA (the dev setup, where web is :9088 and API is :9089)
	// cannot send it without this. It is safe here only because the CORS origin
	// list is explicit — the wildcard is refused below, since the Fetch spec
	// forbids `*` together with credentials and browsers would reject every
	// preflight rather than fail loudly at boot.
	//
	// PUT joins the method list to fix a pre-existing bug: settings/handler.go
	// has always mounted PUT /master-password, which no cross-origin caller
	// could reach. It went unnoticed because production is same-origin behind
	// nginx, where CORS never runs.
	corsOrigins := d.Config.CORSOrigins
	if containsWildcard(corsOrigins) {
		// Same-origin (nginx) never sends an Origin that needs matching, so the
		// practical effect is limited to cross-origin dev — which must name its
		// origin explicitly once credentials are in play.
		corsOrigins = []string{"http://localhost:9088", "https://localhost:9444"}
		d.Logger.Warn("CORS_ORIGINS=* is incompatible with credentialed requests; " +
			"falling back to the default local web origins — set CORS_ORIGINS explicitly")
	}
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: corsOrigins,
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{
			"Content-Type", "Authorization", "X-Foldex-Secret",
			auth.CSRFHeader, folders.UnlockHeader,
		},
		AllowCredentials: true,
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
		api.Use(auth.VaryCookie)

		// The auth surface mounts OUTSIDE the principal middleware — most of it
		// exists precisely to establish a principal, so requiring one would be
		// circular. It is registered even when AUTH_ENABLED=0 so the SPA's
		// /api/auth/me call resolves either way; with auth off it simply
		// reports the bootstrap admin as signed in.
		if d.AuthHandler != nil {
			api.Route("/auth", d.AuthHandler.Mount)
		}

		// Everything below needs a principal, so it lives in a Group rather than
		// behind api.Use: chi requires every middleware to be registered before
		// any route on the same mux, and /api/auth above is a route. A Group gets
		// its own middleware stack, which is exactly the scoping we want anyway —
		// the auth surface must NOT inherit Authenticate.
		api.Group(func(pr chi.Router) {
			// Principal resolution. With AUTH_ENABLED=0 every request is attributed
			// to the bootstrap admin, so a single-user deployment behaves exactly as
			// it did before migration 000017 — the segmentation is in place and
			// exercised, but invisible. With it on, the session middleware resolves
			// the fx_at cookie and enforces CSRF.
			if d.Config.AuthEnabled && d.AuthMiddleware != nil {
				pr.Use(d.AuthMiddleware.Authenticate)
			} else {
				pr.Use(bootstrapPrincipal(d.Pool, d.Logger))
			}

			if d.AdminHandler != nil {
				// RequireAdmin answers 404 (not 403) for a non-admin — see
				// auth.Middleware.RequireAdmin. With AUTH_ENABLED=0 the bootstrap
				// principal is an admin, so the routes stay reachable for the
				// single-user case.
				pr.Route("/admin", func(ar chi.Router) {
					ar.Use(requireAdmin(d.AuthMiddleware))
					d.AdminHandler.Mount(ar)
				})
			}

			pr.Route("/tags", tags.NewHandler(tags.NewRepository(d.Pool)).Mount)
			settingsRepo := settings.NewRepository(d.Pool)
			pr.Route("/settings", settings.NewHandler(settingsRepo).Mount)
			foldersRepo := folders.NewRepository(d.Pool)
			pr.Route("/folders", folders.NewHandler(foldersRepo, d.FolderUnlockKey, settingsRepo).Mount)

			linksRepo := links.NewRepository(d.Pool)
			pr.Route("/links", links.NewHandler(linksRepo, d.Worker).
				WithMetadataFetcher(d.LinkMetadataFetcher).
				WithFolderGate(foldersRepo, d.FolderUnlockKey).
				Mount)

			// d.Storage is optional — when nil, Handler.Delete's image cleanup is
			// a no-op (CRUD itself doesn't need storage). The actual upload route
			// (POST /api/notes/images) is mounted further below, gated the same
			// way links' image upload is.
			pr.Route("/notes", notes.NewHandler(notesRepo, d.Storage).
				WithFolderGate(foldersRepo, d.FolderUnlockKey).
				Mount)
			pr.Route("/entries", entries.NewHandler(entries.NewRepository(d.Pool), foldersRepo, d.FolderUnlockKey).Mount)

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
				pr.Post("/links/{id}/screenshot", sh.CaptureAndStore)
				pr.Post("/links/{id}/image", sh.UploadImage)
				pr.Delete("/links/{id}/image", sh.DeleteImage)
				pr.Get("/files/*", sh.ProxyFile)

				// Note inline-image upload lives in this same gate (rather than its
				// own `d.Storage != nil` check) so it can never be mounted without
				// ProxyFile also being mounted — an uploaded note image would
				// otherwise have nowhere to be served back from.
				nih := notes.NewImageHandler(d.Storage, d.Logger)
				pr.Post("/notes/images", nih.Upload)
			}

			pr.Route("/import", importer.NewHandler(d.Pool, d.Worker).Mount)
			pr.Route("/export", exporter.NewHandler(d.Pool).Mount)
			statsHandler := stats.NewHandler(stats.NewRepository(d.Pool))
			if d.StorageStatter != nil {
				statsHandler = statsHandler.WithStorage(d.StorageStatter)
			}
			pr.Route("/stats", statsHandler.Mount)
			if d.StorageBucket != nil {
				pr.Route("/backup", backup.NewHandler(backup.NewService(d.Pool, d.StorageBucket, d.Logger), d.Logger).Mount)
			}
			if d.PushHandler != nil {
				pr.Route("/push", d.PushHandler.Mount)
			}
		})
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

// requireAdmin adapts the auth middleware's role gate, falling back to an
// in-line check when the auth stack is not wired.
//
// The fallback is NOT permissive: it reads the principal the bootstrap
// middleware installed and applies the same rule. Defaulting to "allow" when
// the middleware happens to be nil would make a wiring mistake into an
// authorization bypass — the exact failure mode a fail-closed design exists to
// prevent.
func requireAdmin(mw *auth.Middleware) func(http.Handler) http.Handler {
	if mw != nil {
		return mw.RequireAdmin
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := authctx.FromContext(r.Context())
			if !ok || !p.Role.IsAdmin() {
				httperr.Write(w, httperr.ErrNotFound)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// containsWildcard reports whether the configured origin list includes "*",
// which the Fetch spec forbids alongside credentialed requests.
// isLoopbackBind reports whether the listen address is local-only.
func isLoopbackBind(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func containsWildcard(origins []string) bool {
	for _, o := range origins {
		if o == "*" {
			return true
		}
	}
	return false
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
