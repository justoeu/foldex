package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/abusepolicy"
	"foldex/internal/auth"
	"foldex/internal/backup"
	"foldex/internal/backupstatus"
	"foldex/internal/config"
	"foldex/internal/depstatus"
	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/metrics"
	"foldex/internal/notes"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authgate"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/logsafe"
	"foldex/internal/policy"
	"foldex/internal/push"
	"foldex/internal/roleperm"
	"foldex/internal/stats"
	"foldex/internal/tracing"
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
	StorageBucket  backup.StorageBucket // optional — nil keeps /api/backup/* mounted as 503 storage_unavailable

	// LinkMetadataFetcher gates POST /api/links/url-metadata. When nil the route
	// is still registered but responds 503 — the dialog falls back to manual
	// title entry without breaking the create flow.
	LinkMetadataFetcher links.MetadataFetcher

	// Web Push wiring. Setting PushHandler also mounts /api/push/vapid-key
	// (inside /api, behind the auth stack). Leaving it nil keeps the routes
	// off entirely.
	PushHandler *push.Handler

	// Auth stack (ADR-30). The handlers are optional and control whether their
	// route groups mount. AuthMiddleware is required whenever AuthEnabled is true.
	AuthHandler  *auth.Handler
	AdminHandler *auth.AdminHandler
	// AuthRepo is the SAME repository the handlers above were built with, not a
	// second one over the same pool. The audit trail's writer and the
	// blocklist's reader both live on it, and two instances would be two
	// prepared-statement caches for one table — harmless today and exactly the
	// kind of duplicate that drifts. Nil unmounts content auditing and the
	// blocklist gate, which is what the router-level tests want.
	AuthRepo *auth.Repository
	// PolicyHandler serves the owner-configurable instance rules. Nil leaves the
	// routes unmounted and every rule at its compiled-in floor.
	PolicyHandler *policy.Handler

	// BackupArtifacts is the bridge to the backup agent (ADR-48), or NIL when
	// the operator never configured it — which is the default and leaves
	// INV-171's wall exactly where it was. The download routes 404 without it.
	BackupArtifacts *backupstatus.ArtifactClient
	// AbusePolicy is the live rate-limit policy (ADR-47 / SDD-ABUSE-DEFENSE).
	// It is read per request so an owner tightening a limit does not have to
	// restart the instance being defended. Nil — the zero-value Deps every
	// router test uses — enforces the COMPILED DEFAULTS, never "no limit": an
	// unwired dependency must not switch a defence off silently.
	AbusePolicy    *abusepolicy.Cache
	AuthMiddleware *auth.Middleware
	FolderHandler  *folders.Handler

	// Grants is the configured RBAC matrix (ADR-42). Nil means the compiled
	// one — the zero-value Deps used across router tests — so a suite that does
	// not care about configured permissions gets the historical behaviour
	// rather than a router where nobody can do anything.
	Grants authgate.Grants

	// Metrics wires the Prometheus collectors (internal/metrics). When set,
	// the instrumentation middleware mounts before everything that can answer
	// a request, and GET /metrics is served behind Config.MetricsToken. Nil
	// keeps both off — the zero-value Deps used across router tests.
	Metrics *metrics.Metrics

	// Trace is the distributed-tracing middleware (tracing.Middleware) —
	// mounted before Metrics so the span covers the whole request and the
	// request logger can stamp trace_id. Nil (the default, and every test's
	// zero-value Deps) keeps tracing off; main only sets it when
	// OTEL_EXPORTER_OTLP_ENDPOINT is configured.
	Trace func(http.Handler) http.Handler

	// DepStatus is the optional-dependency snapshot the signed-in footer
	// reads (object store, mail broker). Nil answers `{resources:[]}` —
	// the zero-value Deps used across router tests.
	DepStatus *depstatus.Checker

	// FolderUnlockKey is the HMAC secret for folder-password unlock tokens
	// (see folders.LoadOrGenerateFolderUnlockKey) — shared between the
	// folders handler (mints tokens, gates list(parent_id=X)) and the links,
	// notes, and entries handlers (gate list(folder_id=X)) so a token issued by
	// one verifies against the others.
	FolderUnlockKey []byte
}

func New(d Deps) http.Handler {
	if d.Config.AuthEnabled && d.AuthMiddleware == nil {
		panic("server: AUTH_ENABLED requires AuthMiddleware")
	}
	grants := roleperm.OrDefault(d.Grants)
	r := chi.NewRouter()
	mountFrontDoor(r, d)

	linksRepo := links.NewRepository(d.Pool)
	notesRepo := notes.NewRepository(d.Pool).WithStorage(d.Storage).WithLogger(d.Logger)
	fileHandler := newFileHandler(d, linksRepo)
	publicShareRoutes(r, d, linksRepo, notesRepo, fileHandler)
	mountAPI(r, d, grants, linksRepo, notesRepo, fileHandler)
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
			// ACTIVE, not merely admin. Without the status filter this resolves to
			// the still-`pending` bootstrap placeholder on a fresh database, or to
			// a DISABLED administrator on an instance where someone was removed —
			// and every request would then be attributed to an account that is
			// not supposed to be able to sign in at all. This is the documented
			// escape hatch out of a lockout, so it has to land somewhere real.
			// Owner sorts first so a single-administrator instance — the common
			// shape for this escape hatch — resolves to the account that holds
			// every permission, rather than to an admin that cannot reach the
			// owner-only policy routes.
			`SELECT id FROM app_user WHERE role IN ('owner', 'admin') AND status = 'active'
			 ORDER BY (role = 'owner') DESC, id LIMIT 1`).Scan(&id); err != nil {
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
			ctx := authctx.WithPrincipal(r.Context(), authctx.Principal{
				UserID: uid,
				// Owner, not admin: with AUTH_ENABLED=0 anyone who can reach the
				// port owns the library anyway, and attributing requests to a role
				// that cannot change policy would make the escape hatch unable to
				// fix the very lockout it exists for.
				Role: authctx.RoleOwner,
				Via:  authctx.ViaSession,
			})
			// Every request under AUTH_ENABLED=0 is attributed to this account,
			// so its spans say "owner" for traffic nobody signed in for. That is
			// the escape hatch working as documented, not identity being wrong.
			tracing.AnnotatePrincipal(ctx)
			next.ServeHTTP(w, r.WithContext(ctx))
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

func statusHandler(c *depstatus.Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := c.Snapshot(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	}
}

func healthz(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		// Healthz is intentionally public so external probes can check
		// liveness. Surface only the boolean state — the raw
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

func slogRequest(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			attrs := []any{
				"method", r.Method,
				"path_class", logsafe.HTTPPath(r.URL.Path),
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur_ms", time.Since(start).Milliseconds(),
			}
			// trace_id is the Loki→Tempo link in Grafana (derived field).
			// Present only when tracing is on and the span is valid.
			if tid := tracing.TraceID(r.Context()); tid != "" {
				attrs = append(attrs, "trace_id", tid)
			}
			logger.Info("http", attrs...)
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
