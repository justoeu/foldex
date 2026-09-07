package server

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"foldex/internal/abusepolicy"
	"foldex/internal/auth"
	"foldex/internal/backup"
	"foldex/internal/backupstatus"
	"foldex/internal/entries"
	"foldex/internal/exporter"
	"foldex/internal/folders"
	"foldex/internal/importer"
	"foldex/internal/links"
	"foldex/internal/notes"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authgate"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/logsafe"
	"foldex/internal/redirect"
	"foldex/internal/settings"
	"foldex/internal/stats"
	"foldex/internal/tags"
)

func mountFrontDoor(r chi.Router, d Deps) {
	trustedNets, badProxies := parseTrustedProxies(d.Config.TrustedProxyIPs)
	for _, entry := range badProxies {
		d.Logger.Error("TRUSTED_PROXY_IPS: ignoring unparseable entry — "+
			"the proxy it names is NOT trusted, so client addresses behind it "+
			"will be recorded as the proxy's own", "entry", logsafe.String(entry))
	}
	if len(trustedNets) > 0 {
		nets := make([]string, 0, len(trustedNets))
		for _, n := range trustedNets {
			nets = append(nets, n.String())
		}
		d.Logger.Info("trusted reverse proxies: X-Forwarded-For is believed ONLY from these",
			"networks", strings.Join(nets, ","), "count", len(trustedNets))
	}
	if len(trustedNets) == 0 && !isLoopbackBind(d.Config.BindAddr) {
		d.Logger.Warn("TRUSTED_PROXY_IPS is empty on a non-loopback bind — if a reverse " +
			"proxy sits in front, every request will be attributed to it and the per-IP " +
			"rate limits will apply to all users as one")
	}
	r.Use(trustedProxyRealIP(trustedNets))
	isTrustedProxy := func(ip string) bool {
		parsed := net.ParseIP(ip)
		return parsed != nil && containsIP(trustedNets, parsed)
	}
	if d.AuthRepo != nil {
		blocklist := auth.NewBlocklist(d.AuthRepo.BlockedIPs)
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
		r.Use(d.Metrics.Instrument)
	}
	r.Use(middleware.Recoverer)
	r.Use(defaultBodyLimit)
	r.Use(slogRequest(d.Logger))
	mountCORS(r, d)
	r.Get("/healthz", healthz(d.Pool))
	if d.Metrics != nil {
		r.Method(http.MethodGet, "/metrics", d.Metrics.Handler(d.Config.MetricsToken))
	}
}

func mountCORS(r chi.Router, d Deps) {
	corsOrigins := d.Config.CORSOrigins
	if containsWildcard(corsOrigins) {
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
}

func newFileHandler(d Deps, linksRepo *links.Repository) *links.ScreenshotHandler {
	if d.Screenshotter == nil || d.Storage == nil {
		return nil
	}
	if d.ScreenshotURL == nil {
		d.Logger.Error("server: Screenshotter is set but ScreenshotURL is nil — refusing to mount /api/links/{id}/screenshot without an SSRF gate")
		panic("server: Screenshotter is set but ScreenshotURL is nil — refusing to mount /api/links/{id}/screenshot without an SSRF gate")
	}
	return links.NewScreenshotHandler(linksRepo, d.Screenshotter, d.Storage, d.ScreenshotURL, d.Logger).
		WithEnqueuer(d.Worker)
}

func publicShareRoutes(r chi.Router, d Deps, linksRepo *links.Repository, notesRepo *notes.Repository, fileHandler *links.ScreenshotHandler) {
	r.Group(func(pub chi.Router) {
		// /n/ and /go/ resolve viewer-scoped: the owner's session keeps them
		// working, everyone else needs the row's explicit public opt-in. The
		// same Vary the /api surface sets — these responses now differ by
		// caller, and a shared cache must not serve one caller another's.
		// Under AUTH_ENABLED=0 the bootstrap principal plays the viewer, per
		// that escape hatch's "whoever reaches the port owns the library".
		pub.Use(auth.VaryCookie)
		if d.Config.AuthEnabled {
			pub.Use(d.AuthMiddleware.Optional)
		} else {
			pub.Use(bootstrapPrincipal(d.Pool, d.Logger))
		}
		pub.Use(newClickCoalescer(d.AbusePolicy).middleware)
		redirect.NewHandler(linksRepo, d.Config.PublicNumericIDs).Mount(pub)
		notes.NewPublicHandler(notesRepo, d.Config.PublicNumericIDs).Mount(pub)
	})
	if fileHandler != nil {
		r.Get("/api/files/notes/*", fileHandler.ProxyNoteFile)
	}
}

func mountAPI(r chi.Router, d Deps, grants authgate.Grants, linksRepo *links.Repository, notesRepo *notes.Repository, fileHandler *links.ScreenshotHandler) {
	r.Route("/api", func(api chi.Router) {
		api.Use(auth.VaryCookie)
		if d.AuthHandler != nil {
			api.Route("/auth", d.AuthHandler.Mount)
		}
		api.Group(func(pr chi.Router) {
			principalStack(pr, d)
			adminSurface(pr, d, grants)
			contentCRUD(pr, d, grants, linksRepo, notesRepo)
			storageOrUnavailable(pr, d, grants, notesRepo, fileHandler)
		})
	})
}

func principalStack(pr chi.Router, d Deps) {
	if d.Config.AuthEnabled {
		pr.Use(d.AuthMiddleware.Authenticate)
	} else {
		pr.Use(bootstrapPrincipal(d.Pool, d.Logger))
	}
	pr.Use(newAPIQuota(d.AbusePolicy, quotaAuditor(d.AuthRepo, d.Logger)).middleware)
	if d.AuthRepo != nil {
		pr.Use(contentAudit(func(r *http.Request, rec auth.AuditRecord) {
			// WithoutCancel: a client hang-up after a successful DELETE must
			// not take the audit row with it.
			ctx, cancel := context.WithTimeout(
				context.WithoutCancel(r.Context()), auditWriteTimeout)
			defer cancel()
			if err := d.AuthRepo.Audit(ctx, rec.WithRequest(r)); err != nil {
				d.Logger.Error("content audit write", "err", err, "action", rec.Action)
			}
		}))
		if d.AuthHandler != nil {
			// INV-023: a content-scoped token must not read sign-in origins.
			pr.With(authgate.RejectAPIToken).Get("/activity", d.AuthHandler.ListOwnActivity)
		}
	}
	pr.With(authgate.RejectAPIToken).Get("/status", statusHandler(d.DepStatus))
}

func adminSurface(pr chi.Router, d Deps, grants authgate.Grants) {
	if d.AdminHandler == nil {
		return
	}
	pr.Route("/admin", func(ar chi.Router) {
		// Role gate FIRST, token gate second: a non-admin — token or not —
		// must get the same 404 as a missing route. Rejecting tokens first
		// would answer 403 to any token holder and confirm /api/admin exists.
		if d.Config.AuthEnabled {
			ar.Use(d.AuthMiddleware.RequireAdmin)
		} else {
			ar.Use(authgate.RequireAdmin)
		}
		ar.Use(authgate.RejectAPIToken)
		d.AdminHandler.Mount(ar)
		auth.NewAbuseHandler(auth.NewRepository(d.Pool),
			abusepolicy.NewRepository(d.Pool), d.AbusePolicy,
			d.Logger, d.AdminHandler.AuditPolicyChange, grants).Mount(ar)
		if d.PolicyHandler != nil {
			ar.Route("/policy", d.PolicyHandler.Mount)
		}
		ar.Route("/backup", backupstatus.NewHandler(
			backupstatus.NewRepository(d.Pool), d.Logger,
			d.AdminHandler.AuditBackupRun, d.AdminHandler.AuditBackupSchedule, grants,
		).Mount)
	})
}

func contentCRUD(pr chi.Router, d Deps, grants authgate.Grants, linksRepo *links.Repository, notesRepo *notes.Repository) {
	writeGate := authgate.RequireWrite(grants, authctx.PermContentWrite)

	pr.Route("/tags", func(tr chi.Router) {
		tr.Use(writeGate)
		tags.NewHandler(tags.NewRepository(d.Pool)).Mount(tr)
	})
	settingsRepo := settings.NewRepository(d.Pool)
	pr.Route("/settings", func(sr chi.Router) {
		// Master recovery password is a credential, not content — a bearer
		// token must not set one and then reset every locked folder.
		sr.Use(authgate.RejectAPIToken)
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
	pr.Route("/notes", func(nr chi.Router) {
		nr.Use(writeGate)
		notes.NewHandler(notesRepo, d.Storage).
			WithFolderGate(foldersRepo, d.FolderUnlockKey).
			Mount(nr)
	})
	pr.Route("/entries", entries.NewHandler(entries.NewRepository(d.Pool), foldersRepo, d.FolderUnlockKey).Mount)

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
	if d.StorageBucket != nil {
		pr.Route("/backup", func(br chi.Router) {
			// Full-library export/restore is not a content-scoped token job.
			br.Use(authgate.RejectAPIToken)
			backup.NewHandler(backup.NewService(d.Pool, d.StorageBucket, d.Logger), d.Logger, grants).Mount(br)
		})
	}
	if d.PushHandler != nil {
		pr.Route("/push", d.PushHandler.Mount)
	}
}

func storageOrUnavailable(pr chi.Router, d Deps, grants authgate.Grants, notesRepo *notes.Repository, fileHandler *links.ScreenshotHandler) {
	writeGate := authgate.RequireWrite(grants, authctx.PermContentWrite)
	if fileHandler != nil {
		pr.With(writeGate).Post("/links/{id}/screenshot", fileHandler.CaptureAndStore)
		pr.With(writeGate).Post("/links/{id}/image", fileHandler.UploadImage)
		pr.With(writeGate).Delete("/links/{id}/image", fileHandler.DeleteImage)
		pr.Get("/files/*", fileHandler.ProxyFile)
		nih := notes.NewImageHandler(d.Storage, notesRepo, d.Logger)
		pr.With(writeGate).Post("/notes/images", nih.Upload)
		return
	}
	unavailable := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httperr.Write(w, httperr.New(http.StatusServiceUnavailable, "storage_unavailable", "object store is unavailable"))
	})
	pr.With(writeGate).Post("/links/{id}/screenshot", unavailable)
	pr.With(writeGate).Post("/links/{id}/image", unavailable)
	pr.With(writeGate).Delete("/links/{id}/image", unavailable)
	pr.With(writeGate).Post("/notes/images", unavailable)
}
