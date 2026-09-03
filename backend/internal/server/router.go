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
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/abusepolicy"
	"foldex/internal/auth"
	"foldex/internal/backup"
	"foldex/internal/backupstatus"
	"foldex/internal/config"
	"foldex/internal/depstatus"
	"foldex/internal/entries"
	"foldex/internal/exporter"
	"foldex/internal/folders"
	"foldex/internal/importer"
	"foldex/internal/links"
	"foldex/internal/metrics"
	"foldex/internal/notes"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authgate"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/logsafe"
	"foldex/internal/policy"
	"foldex/internal/push"
	"foldex/internal/redirect"
	"foldex/internal/roleperm"
	"foldex/internal/settings"
	"foldex/internal/stats"
	"foldex/internal/tags"
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
	StorageBucket  backup.StorageBucket // optional — enables /api/backup/* when the object store is up

	// LinkMetadataFetcher gates GET /api/links/url-metadata. When nil the route
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
	// Resolved once, before anything that gates. Nil Deps.Grants means the
	// compiled matrix — see the field's note.
	grants := roleperm.OrDefault(d.Grants)

	r := chi.NewRouter()
	var backupHandler *backup.Handler
	if d.StorageBucket != nil {
		backupHandler = backup.NewHandler(backup.NewService(d.Pool, d.StorageBucket, d.Logger), d.Logger, grants)
	}
	// NOT chi's middleware.RealIP: that one rewrites RemoteAddr from
	// X-Forwarded-For unconditionally, which is correct behind nginx and
	// forgeable on a direct bind. See realip.go.
	trustedNets, badProxies := parseTrustedProxies(d.Config.TrustedProxyIPs)
	for _, entry := range badProxies {
		d.Logger.Error("TRUSTED_PROXY_IPS: ignoring unparseable entry — "+
			"the proxy it names is NOT trusted, so client addresses behind it "+
			"will be recorded as the proxy's own", "entry", logsafe.String(entry))
	}
	// Say out loud who this instance believes, at INFO, on every boot.
	//
	// The warning below only fires when the list is EMPTY, so the far more
	// common question — "we DO have a value; is it the right one?" — had no
	// answer in the logs at all. Answering it meant reading docker-compose.yml,
	// then .env, then the container's environment, and an investigation in this
	// repo lost real time to exactly that: a trail line reading
	// `trusted=false` was diagnosed as a missing default that had in fact been
	// configured for a year, and the SDD carried the wrong conclusion until
	// someone checked the running process.
	//
	// The set is printed as parsed, not as configured, so a CIDR that widened
	// under parsing is visible as what it became.
	if len(trustedNets) > 0 {
		nets := make([]string, 0, len(trustedNets))
		for _, n := range trustedNets {
			nets = append(nets, n.String())
		}
		d.Logger.Info("trusted reverse proxies: X-Forwarded-For is believed ONLY from these",
			"networks", strings.Join(nets, ","), "count", len(trustedNets))
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
	// isTrustedProxy is the CIDR test both the block rail and this file need.
	isTrustedProxy := func(ip string) bool {
		parsed := net.ParseIP(ip)
		return parsed != nil && containsIP(trustedNets, parsed)
	}
	// The permanent blocklist (ADR-46). Mounted immediately after the address is
	// resolved and BEFORE routing, so a blocked caller reaches no handler; and
	// after RequestID would be wrong only in that a refused request would take a
	// number it never uses.
	var blocklist *auth.Blocklist
	if d.AuthRepo != nil {
		blocklist = auth.NewBlocklist(d.AuthRepo.BlockedIPs)
		r.Use(blocklistGate(blocklist))
		if d.AdminHandler != nil {
			d.AdminHandler.WithBlocklist(blocklist, isTrustedProxy)
		}
	}
	r.Use(middleware.RequestID)
	if d.Trace != nil {
		r.Use(d.Trace)
	}
	if d.Metrics != nil {
		// Before Recoverer so a recovered panic is still counted as the 500 it
		// answered; skips /metrics and /healthz internally.
		r.Use(d.Metrics.Instrument)
	}
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
			"Content-Type", "Authorization",
			auth.CSRFHeader, folders.UnlockHeader,
		},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/healthz", healthz(d.Pool))
	if d.Metrics != nil {
		r.Method(http.MethodGet, "/metrics", d.Metrics.Handler(d.Config.MetricsToken))
	}

	// Redirect/view routes outside /api keep the URL short, avoid CORS
	// preflight, and stay session-less — both /go/{id-or-slug} (link
	// redirect) and /n/{id-or-slug} (note render) are public share
	// surfaces resolved by slug.
	linksRepo := links.NewRepository(d.Pool)
	notesRepo := notes.NewRepository(d.Pool).WithStorage(d.Storage)
	var fileHandler *links.ScreenshotHandler
	if d.Screenshotter != nil && d.Storage != nil {
		if d.ScreenshotURL == nil {
			d.Logger.Error("server: Screenshotter is set but ScreenshotURL is nil — refusing to mount /api/links/{id}/screenshot without an SSRF gate")
			panic("server: Screenshotter is set but ScreenshotURL is nil — refusing to mount /api/links/{id}/screenshot without an SSRF gate")
		}
		// The worker is wired here so a stored image that turns out to be gone
		// re-arms its own preview — see healMissingObject.
		fileHandler = links.NewScreenshotHandler(linksRepo, d.Screenshotter, d.Storage, d.ScreenshotURL, d.Logger).
			WithEnqueuer(d.Worker)
	}
	// Both public share routes resolve with NO session, so a numeric id there
	// is an enumeration oracle across every tenant — see ADR-32. Off unless the
	// operator opts in for the sake of already-shared /go/42 links.
	// A Group, not r.Use: chi requires every middleware to be registered before
	// any route on the same mux, and /healthz is already registered above. The
	// narrower scope is what we want anyway — the coalescer decides nothing
	// outside these two public surfaces.
	r.Group(func(pub chi.Router) {
		pub.Use(newClickCoalescer(d.AbusePolicy).middleware)
		redirect.NewHandler(linksRepo, d.Config.PublicNumericIDs).Mount(pub)
		notes.NewPublicHandler(notesRepo, d.Config.PublicNumericIDs).Mount(pub)
	})
	if fileHandler != nil {
		// Public note HTML references these exact URLs. This one narrow
		// UUID-keyed read is deliberately session-less; all other object
		// keys remain in the principal-scoped /api group below.
		r.Get("/api/files/notes/*", fileHandler.ProxyNoteFile)
	}

	r.Route("/api", func(api chi.Router) {
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
			if d.Config.AuthEnabled {
				pr.Use(d.AuthMiddleware.Authenticate)
			} else {
				pr.Use(bootstrapPrincipal(d.Pool, d.Logger))
			}

			// The authenticated write quota (SDD-ABUSE-DEFENSE §5.3). Directly
			// below principal resolution, because it keys on the principal;
			// above everything else, because a request it refuses must cost
			// nothing further. No role is exempt, the owner included.
			// The recorder is what makes an API-quota lockout visible in the
			// anomaly panel; without it a throttled principal is a 429 in the
			// access log and nothing in the trail. Nil when no repository is
			// wired: the quota still enforces, it just says nothing.
			pr.Use(newAPIQuota(d.AbusePolicy, quotaAuditor(d.AuthRepo, d.Logger)).middleware)

			// Content auditing (ADR-46). Inside the principal group so the
			// actor is resolved, and above every content route so a route
			// added later is covered without anyone having to remember — the
			// same reason credential redaction lives at the root log handler
			// rather than at each call site. It records nothing for a method
			// or route pattern outside contentAuditActions, so mounting it
			// broadly costs a map lookup on mutations only.
			if d.AuthRepo != nil {
				pr.Use(contentAudit(func(r *http.Request, rec auth.AuditRecord) {
					// WithoutCancel, not the request context. The handler has
					// already committed by the time this runs, so a client that
					// hangs up after a successful DELETE would otherwise take
					// the audit row with it — cancelled context, write aborted,
					// nothing but a log line left. That is not a lost log
					// entry, it is a way to delete a row without being
					// recorded, available to anyone who closes the connection
					// fast enough. The deadline keeps a hung database from
					// holding the goroutine open indefinitely.
					ctx, cancel := context.WithTimeout(
						context.WithoutCancel(r.Context()), auditWriteTimeout)
					defer cancel()
					if err := d.AuthRepo.Audit(ctx, rec.WithRequest(r)); err != nil {
						d.Logger.Error("content audit write", "err", err, "action", rec.Action)
					}
				}))
				// The caller reading their OWN activity. Not under /admin:
				// this needs no administrative permission and must work for a
				// viewer, and it is the only projection that returns the
				// subject column.
				if d.AuthHandler != nil {
					// RejectAPIToken for INV-023's reason: a token is scoped to
					// CONTENT, and this feed carries the account's sign-in
					// ORIGINS — the addresses and devices it was used from.
					// That is account metadata, which is why every other
					// surface returning it (/api/admin, /api/settings, the
					// session half of /api/auth) refuses a bearer credential
					// too. A token pasted into an extension's configuration
					// must not read the owner's movements.
					pr.With(authgate.RejectAPIToken).Get("/activity", d.AuthHandler.ListOwnActivity)
				}
			}

			// Optional-dependency reachability for the SPA footer. Session
			// only: a content-scoped API token is not an operator console,
			// and the payload is reconnaissance of which extras this
			// instance runs. Always 200 — a down store is the body, not
			// the status; 503 here would hide the footer that exists to
			// say so.
			pr.With(authgate.RejectAPIToken).Get("/status", statusHandler(d.DepStatus))

			if d.AdminHandler != nil {
				// RequireAdmin answers 404 (not 403) for a non-admin — see
				// auth.Middleware.RequireAdmin. With AUTH_ENABLED=0 the bootstrap
				// principal is an admin, so the routes stay reachable for the
				// single-user case.
				pr.Route("/admin", func(ar chi.Router) {
					// Role gate FIRST, token gate second, and the order is the
					// whole point: a non-admin — token or not — must get the
					// same 404 as a route that does not exist. Rejecting tokens
					// first would answer 403 to any token holder, confirming
					// /api/admin exists to accounts that are not supposed to
					// know. An ADMIN presenting a token gets the 403, which
					// tells them something true about their own credential.
					if d.Config.AuthEnabled {
						ar.Use(d.AuthMiddleware.RequireAdmin)
					} else {
						ar.Use(authgate.RequireAdmin)
					}
					ar.Use(authgate.RejectAPIToken)
					d.AdminHandler.Mount(ar)

					// ADR-47 — os limites de abuso e o painel de anomalias.
					// Construído aqui pela razão do backupstatus: precisa só do
					// pool, do hook de auditoria e dos grants.
					//
					// O cache é o MESMO objeto que a cota e o login leem
					// (d.AbusePolicy), e isso é o que faz o Invalidate() do PUT
					// significar alguma coisa. Um cache próprio aqui compilaria,
					// passaria nos testes, e deixaria a tela salvando um valor
					// que só entraria em vigor depois do TTL — o tipo de defeito
					// que se manifesta como "salvei e não mudou nada".
					auth.NewAbuseHandler(auth.NewRepository(d.Pool),
						abusepolicy.NewRepository(d.Pool), d.AbusePolicy,
						d.Logger, d.AdminHandler.AuditPolicyChange, grants).Mount(ar)
					if d.PolicyHandler != nil {
						ar.Route("/policy", d.PolicyHandler.Mount)
					}
					// The operational backup surface (ADR-43 PR5). Built here
					// rather than injected: it needs only the pool, the live
					// grants and the audit hook, all of which this closure
					// already holds — an optional Deps field would be one more
					// nil that silently unmounts a route.
					ar.Route("/backup", backupstatus.NewHandler(
						backupstatus.NewRepository(d.Pool), d.Logger,
						d.AdminHandler.AuditBackupRun, d.AdminHandler.AuditBackupSchedule, grants,
					).Mount)
				})
			}

			// The viewer role is read-only over its OWN library, and this is
			// where that means something. The gate is method-aware and mounted
			// on the group, so a mutating route added to any of these packages
			// later is refused without anyone having to remember — the same
			// reason credential redaction lives at the root log handler rather
			// than at each call site.
			//
			// /folders and /backup are deliberately absent: both answer POST to
			// operations that only read, so they gate per route below.
			writeGate := authgate.RequireWrite(grants, authctx.PermContentWrite)

			pr.Route("/tags", func(tr chi.Router) {
				tr.Use(writeGate)
				tags.NewHandler(tags.NewRepository(d.Pool)).Mount(tr)
			})
			settingsRepo := settings.NewRepository(d.Pool)
			// /settings is ENTIRELY the master recovery password, which can
			// clear any folder's password. That is a credential operation,
			// not content, so a bearer token has no business here — and
			// setting a master needs no proof when none is configured yet,
			// so a leaked token would otherwise be: set a master, then reset
			// every locked folder and read it.
			pr.Route("/settings", func(sr chi.Router) {
				sr.Use(authgate.RejectAPIToken)
				// A viewer holds its library read-only, and the master password
				// is what RESETS a locked folder's password — setting or
				// clearing it is a mutation of the caller's own recovery
				// credential, not a read. Reading the status stays open, which
				// is why the gate is method-aware rather than blanket.
				sr.Use(writeGate)
				settings.NewHandler(settingsRepo).Mount(sr)
			})
			foldersRepo := folders.NewRepository(d.Pool)
			folderHandler := d.FolderHandler
			if folderHandler == nil {
				folderHandler = folders.NewHandler(foldersRepo, d.FolderUnlockKey, settingsRepo, grants)
			}
			pr.Route("/folders", folderHandler.Mount)

			pr.Route("/links", func(lr chi.Router) {
				lr.Use(writeGate)
				links.NewHandler(linksRepo, d.Worker).
					WithMetadataFetcher(d.LinkMetadataFetcher).
					WithFolderGate(foldersRepo, d.FolderUnlockKey).
					Mount(lr)
			})

			// d.Storage is optional — when nil, Handler.Delete's image cleanup is
			// a no-op (CRUD itself doesn't need storage). The actual upload route
			// (POST /api/notes/images) is mounted further below, gated the same
			// way links' image upload is.
			pr.Route("/notes", func(nr chi.Router) {
				nr.Use(writeGate)
				notes.NewHandler(notesRepo, d.Storage).
					WithFolderGate(foldersRepo, d.FolderUnlockKey).
					Mount(nr)
			})
			pr.Route("/entries", entries.NewHandler(entries.NewRepository(d.Pool), foldersRepo, d.FolderUnlockKey).Mount)

			// Screenshot and file-proxy endpoints are only registered when both
			// a Screenshotter and Storage implementation are provided.
			if fileHandler != nil {
				// These hang off /api directly rather than inside the /links
				// group, so they do not inherit its write gate and each needs it
				// named. ProxyFile stays open: it is the read path, and note
				// media is deliberately reachable without a session at all.
				pr.With(writeGate).Post("/links/{id}/screenshot", fileHandler.CaptureAndStore)
				pr.With(writeGate).Post("/links/{id}/image", fileHandler.UploadImage)
				pr.With(writeGate).Delete("/links/{id}/image", fileHandler.DeleteImage)
				pr.Get("/files/*", fileHandler.ProxyFile)

				// Note inline-image upload lives in this same gate (rather than its
				// own `d.Storage != nil` check) so it can never be mounted without
				// ProxyFile also being mounted — an uploaded note image would
				// otherwise have nowhere to be served back from.
				nih := notes.NewImageHandler(d.Storage, notesRepo, d.Logger)
				pr.With(writeGate).Post("/notes/images", nih.Upload)
			} else {
				// Keep the mutating image routes on the mux when the object
				// store is down. Omitting them made Chi answer an empty 404,
				// which the SPA surfaced as "Request failed with status code
				// 404" on Save after picking a file. 503 with an envelope is
				// the honest answer; GET /files stays unmounted because there
				// is nothing to serve.
				unavailable := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					httperr.Write(w, httperr.New(http.StatusServiceUnavailable, "storage_unavailable", "object store is unavailable"))
				})
				pr.With(writeGate).Post("/links/{id}/screenshot", unavailable)
				pr.With(writeGate).Post("/links/{id}/image", unavailable)
				pr.With(writeGate).Delete("/links/{id}/image", unavailable)
				pr.With(writeGate).Post("/notes/images", unavailable)
			}

			pr.Route("/import", func(ir chi.Router) {
				ir.Use(authgate.RequireWrite(grants, authctx.PermImportRun))
				importer.NewHandler(d.Pool, d.Worker).Mount(ir)
			})
			pr.Route("/export", exporter.NewHandler(d.Pool).Mount)
			statsHandler := stats.NewHandler(stats.NewRepository(d.Pool))
			if d.StorageStatter != nil {
				statsHandler = statsHandler.WithStorage(d.StorageStatter)
			}
			pr.Route("/stats", statsHandler.Mount)
			if backupHandler != nil {
				// Backup export is every row and every file the caller owns, in
				// one download. A bearer token pasted into an extension's
				// configuration must not be able to produce that, and restore
				// must not be able to overwrite it.
				pr.Route("/backup", func(br chi.Router) {
					br.Use(authgate.RejectAPIToken)
					backupHandler.Mount(br)
				})
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
