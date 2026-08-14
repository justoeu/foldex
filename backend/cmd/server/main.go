package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"foldex/internal/auth"
	"foldex/internal/backup"
	"foldex/internal/changecheck"
	"foldex/internal/config"
	"foldex/internal/db"
	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/mailer"
	"foldex/internal/notemedia"
	"foldex/internal/oauthgoogle"
	"foldex/internal/pkg/keyfile"
	"foldex/internal/pkg/logsafe"
	"foldex/internal/pkg/secrets"
	"foldex/internal/preview"
	"foldex/internal/push"
	"foldex/internal/screenshot"
	"foldex/internal/server"
	"foldex/internal/stats"
	"foldex/internal/storage"
)

func main() {
	// Every logger in the process descends from this one, so the redactor wraps
	// the ROOT handler rather than being applied per call site. Nothing here
	// logs a credential today; this exists so that the next log line added in a
	// hurry — during an incident, by whoever is debugging — cannot make one
	// permanent. See internal/pkg/logsafe.RedactHandler.
	logger := slog.New(logsafe.NewRedactHandler(
		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}

	// SHARED_SECRET predates accounts. It is a perimeter header: except for the
	// UUID-keyed note-media read required by public notes, it gates /api/* and
	// identifies nobody, so it can neither tell two users apart nor scope a
	// single row. Real authentication replaced it in ADR-30; what is left is a
	// second lock on the front door, and it is on its way out.
	switch {
	case cfg.SharedSecret != "":
		logger.Warn("SHARED_SECRET is DEPRECATED and will be removed in a future release. " +
			"It authenticates nobody and identifies nobody — AUTH_ENABLED does both. " +
			"Keep it only while older browser extensions are still configured with it.")
	case !cfg.AuthEnabled:
		// The one genuinely dangerous combination: no accounts AND no perimeter.
		// Every request is attributed to the bootstrap admin, so anyone who can
		// reach the port owns the whole library.
		logger.Warn("AUTH_ENABLED=0 and SHARED_SECRET is empty — /api/* is reachable with no " +
			"credential at all, and every request is attributed to the bootstrap administrator. " +
			"Safe only on a loopback bind; turn AUTH_ENABLED back on before exposing this server.")
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.New(rootCtx, cfg.DBURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Refuse to run against an un-migrated database. Migrations are applied
	// deliberately in this project, so an image upgrade can outrun the schema —
	// and with authentication on, that failure is both delayed and
	// irreversible unless it is caught here. See db.CheckSchemaVersion.
	if err := db.CheckSchemaVersion(rootCtx, pool); err != nil {
		logger.Error("schema check failed", "err", err)
		os.Exit(1)
	}

	worker := preview.NewWorker(pool, cfg.PreviewConcurrency, time.Duration(cfg.PreviewTimeoutSec)*time.Second, logger)

	// Dedicated Fetcher for the synchronous GET /api/links/url-metadata
	// endpoint that pre-fills the LinkDialog Title/Description. Reuses
	// preview.NewFetcher so the SSRF guards (IMDS always blocked, RFC1918
	// gated by PREVIEW_STRICT_SSRF) are identical to the async worker —
	// per CLAUDE.md §4 we never re-roll a second HTTP client / SSRF posture.
	metadataFetcher := preview.NewFetcher(time.Duration(cfg.PreviewTimeoutSec) * time.Second)
	screenshotPool := screenshot.NewPool()

	// Object store (RustFS / S3 API) is optional — if it cannot be reached, we
	// log a warning and disable screenshot/upload endpoints rather than
	// refusing to start.
	var storageClient *storage.Client
	sc, err := storage.New(rootCtx, storage.Config{
		Endpoint:  cfg.ObjectStore.Endpoint,
		AccessKey: cfg.ObjectStore.AccessKey,
		SecretKey: cfg.ObjectStore.SecretKey,
		Bucket:    cfg.ObjectStore.Bucket,
		UseSSL:    cfg.ObjectStore.UseSSL,
	}, logger)
	if err != nil {
		logger.Warn("object store unavailable — screenshot endpoints disabled", "err", err)
	} else {
		storageClient = sc
		notemedia.NewSweeper(pool, storageClient, logger).Start(rootCtx)
	}

	// Wire the screenshot fallback before starting the worker. When the object
	// store is up the worker will, after each preview, capture a screenshot
	// for links that have no og:image AND resolve to a public host.
	if storageClient != nil {
		worker.WithScreenshotFallback(screenshotPool, storageClient)
	}
	worker.Start(rootCtx)

	// VAPID + push.Sender — loaded BEFORE the changecheck worker so we can
	// inject the sender as its Notify dep. A keypair load failure is fatal
	// only when an operator pinned VAPID_PUBLIC_KEY/PRIVATE_KEY partially;
	// otherwise the autogen path keeps booting going.
	vapid, err := push.LoadOrGenerate(
		cfg.VAPIDPublicKey, cfg.VAPIDPrivateKey, cfg.VAPIDSubject,
		cfg.VAPIDStatePath, cfg.VAPIDAutoGenerate, logger,
	)
	if err != nil {
		logger.Error("vapid setup failed", "err", err)
		os.Exit(1)
	}
	pushRepo := push.NewRepository(pool)
	pushSender := push.NewSender(vapid, pushRepo, logger)
	pushHandler := push.NewHandler(vapid, pushRepo, pushSender)

	// Folder-unlock-token HMAC secret — same load-or-generate shape as VAPID
	// above. A failure here is fatal only when the operator pinned
	// FOLDER_UNLOCK_KEY to something invalid; the autogen path always boots.
	folderUnlockKey, err := folders.LoadOrGenerateFolderUnlockKey(
		cfg.FolderUnlockKey, cfg.FolderUnlockKeyPath, cfg.FolderUnlockAutoGenerate, logger,
	)
	if err != nil {
		logger.Error("folder unlock key setup failed", "err", err)
		os.Exit(1)
	}

	// Change-check worker is opt-in per link (link.check_interval). When the
	// kill-switch is off OR no link is opted in the worker still runs but its
	// scan returns an empty list, so the cost is essentially a goroutine + a
	// ticker.
	var ccWorker *changecheck.Worker
	if cfg.ChangeCheckEnabled {
		ccFetcher := preview.NewFetcher(time.Duration(cfg.ChangeCheckFetchTimeoutSec) * time.Second)
		ccWorker = changecheck.New(
			links.NewRepository(pool),
			ccFetcher,
			pushSenderAdapter{s: pushSender},
			changecheck.Options{
				Concurrency:  cfg.ChangeCheckConcurrency,
				ScanInterval: time.Duration(cfg.ChangeCheckScanIntervalSec) * time.Second,
				FetchTimeout: time.Duration(cfg.ChangeCheckFetchTimeoutSec) * time.Second,
			},
			logger,
		)
		ccWorker.Start(rootCtx)
	}

	// Auth stack (ADR-30). Wired unconditionally so /api/auth/me answers even
	// with AUTH_ENABLED=0 — what the flag gates is whether the session
	// middleware REPLACES the bootstrap principal, not whether the endpoints
	// exist. A missing /api/auth would make the SPA's boot probe 404 and leave
	// it unable to tell "auth is off" from "backend is broken".
	mail, err := mailer.New(mailer.Config{
		Driver:             cfg.Mail.Driver,
		Host:               cfg.Mail.Host,
		Port:               cfg.Mail.Port,
		Username:           cfg.Mail.Username,
		Password:           cfg.Mail.Password,
		From:               cfg.Mail.From,
		FromName:           cfg.Mail.FromName,
		STARTTLS:           cfg.Mail.STARTTLS,
		TLS:                cfg.Mail.TLS,
		InsecureSkipVerify: cfg.Mail.InsecureSkipVerify,
	}, logger)
	if err != nil {
		logger.Error("mailer setup failed", "err", err)
		os.Exit(1)
	}
	mailDispatcher := mailer.NewDispatcher(context.Background(), mail, mailer.DispatcherOptions{
		Workers: mailer.DefaultDispatcherWorkers, QueueSize: mailer.DefaultDispatcherQueueSize,
	}, logger)
	authRepo := auth.NewRepository(pool)
	cookieOpts := auth.CookieOptions{Secure: cfg.AuthCookieSecure, Domain: cfg.AuthCookieDomain}
	authMW := auth.NewMiddleware(authRepo, cookieOpts, logger,
		cfg.AuthEnabled && cfg.AuthRequire2FAForAdmins)
	authTTL := auth.SessionTTL{
		Access:   time.Duration(cfg.AuthAccessTTLMin) * time.Minute,
		Refresh:  time.Duration(cfg.AuthRefreshTTLDays) * 24 * time.Hour,
		Absolute: time.Duration(cfg.AuthAbsoluteTTLDays) * 24 * time.Hour,
		Grace:    time.Duration(cfg.AuthRefreshGraceSec) * time.Second,
	}
	// The TOTP seed-encryption key. AllowEphemeral is FALSE: unlike the folder
	// unlock key, a regenerated one makes every stored seed undecryptable and
	// locks every 2FA user out of their own account permanently. Better to
	// refuse to boot than to succeed and destroy the seeds at the next restart.
	authKey, err := keyfile.Load(keyfile.Config{
		Name:           "auth encryption key",
		EnvVar:         "AUTH_ENCRYPTION_KEY",
		PathVar:        "AUTH_ENCRYPTION_KEY_PATH",
		EnvValue:       cfg.AuthEncryptionKey,
		Path:           cfg.AuthEncryptionKeyPath,
		AutoGenerate:   cfg.AuthEncryptionAutoGen,
		AllowEphemeral: false,
	}, logger)
	if err != nil {
		logger.Error("auth encryption key", "err", err)
		os.Exit(1)
	}
	authCipher, err := secrets.NewCipher(authKey)
	if err != nil {
		logger.Error("auth cipher", "err", err)
		os.Exit(1)
	}
	authCodeMAC, err := auth.NewCodeMAC(authKey)
	if err != nil {
		logger.Error("auth code MAC", "err", err)
		os.Exit(1)
	}

	// Built unconditionally. With no client credentials it reports itself
	// disabled, /api/auth/me advertises google_oauth:false so the SPA hides
	// the button, and the routes answer a readable "not configured" rather
	// than 404 — which is what an operator who set only one of the two
	// variables needs to see.
	google := oauthgoogle.New(oauthgoogle.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  cfg.GoogleRedirectURL(),
	})
	if google.Enabled() {
		logger.Info("google oauth enabled", "redirect_uri", cfg.GoogleRedirectURL())
	}

	authHandler := auth.NewHandler(auth.HandlerConfig{
		Repo: authRepo, MW: authMW, Mailer: mail, MailDispatcher: mailDispatcher, Cookies: cookieOpts,
		TTL: authTTL, Logger: logger, BaseURL: cfg.AuthPublicURL,
		Cipher: authCipher, CodeMAC: authCodeMAC, TOTPIssuer: cfg.AuthTOTPIssuer,
		Require2FAForAdmins: cfg.AuthRequire2FAForAdmins,
		Google:              google,
	})
	adminHandler := auth.NewAdminHandler(authRepo, mail, mailDispatcher, logger, cfg.AuthPublicURL)

	// The sweeper prunes the DB rows AND the two process-local caches that grow
	// with traffic: the rate-limit buckets (keyed by attacker-supplied e-mail on
	// an unauthenticated endpoint) and the last_seen_at throttle map. Neither is
	// trimmed anywhere else, so leaving them off this ticker is a memory leak.
	sweeper := auth.NewSweeper(authRepo, logger,
		time.Duration(cfg.AuthSweepIntervalMin)*time.Minute,
		time.Duration(cfg.AuthSweepRetainDays)*24*time.Hour).
		WithInMemory(authHandler.SweepLimiters, authMW.SweepTouch)
	sweeper.Start(rootCtx)

	deps := server.Deps{
		Pool:                pool,
		Worker:              worker,
		Logger:              logger,
		Config:              cfg,
		Storage:             storageClient,
		PushHandler:         pushHandler,
		LinkMetadataFetcher: linkMetadataAdapter{f: metadataFetcher},
		FolderUnlockKey:     folderUnlockKey,
		AuthHandler:         authHandler,
		AdminHandler:        adminHandler,
		AuthMiddleware:      authMW,
	}
	if storageClient != nil {
		deps.Screenshotter = screenshotPool
		// SSRF gate for the manual /api/links/{id}/screenshot endpoint. Same
		// helper the preview worker uses for its fallback path — rejects
		// IMDS, RFC1918, loopback, link-local, IPv6 ULA, and non-http(s)
		// schemes. Without this, the endpoint becomes a read-anywhere
		// primitive (file:///etc/passwd → screenshot → /api/files).
		deps.ScreenshotURL = preview.IsPublicURL
		deps.StorageStatter = storageStatsAdapter{c: storageClient}
		deps.StorageBucket = backupStorageAdapter{c: storageClient}
	}

	router := server.New(deps)

	// NOTE: http.Server has no MaxRequestBodyBytes in Go 1.26. Absolute body
	// ceilings live in server.defaultBodyLimit (path-aware MaxBytesReader:
	// 1 MiB default, 6 MiB images, 100 MiB import, 2 GiB backup) plus the
	// per-handler caps already on JSON/multipart routes.
	srv := &http.Server{
		// BindAddr defaults to 127.0.0.1 (single-user threat model). Override
		// via BACKEND_BIND only when fronting with a reverse proxy AND
		// SHARED_SECRET is set — config.validateSecureDefaults refuses the
		// "wide open" combo at boot.
		Addr:              cfg.BindAddr + ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		// Generous body timeouts so backup restore (up to a few hundred MB)
		// doesn't get killed mid-upload on slower networks. Headers still
		// have the short 5s lid — slow-loris doesn't apply to bodies because
		// we either stream or LimitReader at the handler level.
		ReadTimeout:  10 * time.Minute,
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Info("server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", "err", err)
			stop()
		}
	}()

	<-rootCtx.Done()

	logger.Info("shutting down")

	shutCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var shutdownWG sync.WaitGroup
	shutdownWG.Add(3)
	go func() {
		defer shutdownWG.Done()
		if err := srv.Shutdown(shutCtx); err != nil {
			logger.Error("graceful shutdown failed", "err", err)
		}
		// Drain HTTP first so no handler can hold an unpublished queue
		// reservation when dispatcher cancellation joins its workers.
		mailDispatcher.Stop()
	}()
	go func() {
		defer shutdownWG.Done()
		worker.Stop()
		if ccWorker != nil {
			ccWorker.Stop()
		}
	}()
	// Cancel Chromium concurrently with HTTP/worker drain so all subsystems
	// share one container shutdown budget instead of paying serial deadlines.
	go func() {
		defer shutdownWG.Done()
		screenshotPool.Close()
	}()
	shutdownDone := make(chan struct{})
	go func() {
		shutdownWG.Wait()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-shutCtx.Done():
		logger.Error("shutdown deadline exceeded", "err", shutCtx.Err())
	}
	logger.Info("bye")
}

// linkMetadataAdapter bridges *preview.Fetcher (returning preview.Result) to
// links.MetadataFetcher (returning links.URLMetadata). The two shapes are
// field-for-field identical — the adapter exists to keep the links package
// from depending on preview directly.
type linkMetadataAdapter struct{ f *preview.Fetcher }

func (a linkMetadataAdapter) FetchMetadata(ctx context.Context, pageURL string) (links.URLMetadata, error) {
	r, err := a.f.Fetch(ctx, pageURL)
	if err != nil {
		return links.URLMetadata{}, err
	}
	return links.URLMetadata{
		Title:       r.Title,
		Description: r.Description,
		FaviconURL:  r.FaviconURL,
		OGImageURL:  r.OGImageURL,
	}, nil
}

// pushSenderAdapter bridges *push.Sender (which speaks push.Notification) to
// changecheck.Sender (which speaks changecheck.Notification). Both shapes
// are field-for-field identical — the adapter exists to avoid an import
// cycle between the two packages.
type pushSenderAdapter struct{ s *push.Sender }

func (a pushSenderAdapter) Notify(ctx context.Context, n changecheck.Notification) error {
	return a.s.Notify(ctx, push.Notification{
		LinkID: n.LinkID,
		Title:  n.Title,
		URL:    n.URL,
		Kind:   n.Kind,
		// UserID is what scopes the fan-out to the link owner. Dropping it here
		// would compile fine and silently deliver to nobody (user 0 owns no
		// subscriptions) — TestChangeCheckPushGoesOnlyToTheLinkOwner locks it.
		UserID: n.UserID,
	})
}

// storageStatsAdapter bridges storage.Client to the stats.StorageStatter
// contract without making the storage package depend on stats.
type storageStatsAdapter struct{ c *storage.Client }

func (a storageStatsAdapter) Stats(ctx context.Context) (stats.StorageStats, error) {
	s, err := a.c.Stats(ctx)
	if err != nil {
		return stats.StorageStats{}, err
	}
	return stats.StorageStats{Objects: s.Objects, TotalBytes: s.TotalBytes}, nil
}

// backupStorageAdapter wires *storage.Client to the narrow contract
// backup.Service expects. Kept in main so the storage package stays
// dependency-free of backup.
type backupStorageAdapter struct{ c *storage.Client }

func (a backupStorageAdapter) WalkObjects(ctx context.Context, prefix string, visit func(backup.ObjectInfo) error) error {
	return a.c.WalkObjects(ctx, prefix, func(object storage.ObjectInfo) error {
		return visit(backup.ObjectInfo{Key: object.Key, Size: object.Size})
	})
}

func (a backupStorageAdapter) OpenObject(ctx context.Context, key string) (io.ReadCloser, error) {
	return a.c.OpenObject(ctx, key)
}

func (a backupStorageAdapter) PutObjectStream(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	return a.c.PutObjectStream(ctx, key, r, size, contentType)
}

func (a backupStorageAdapter) ExistingObjects(ctx context.Context, keys []string) (map[string]bool, error) {
	return a.c.ExistingObjects(ctx, keys)
}

func (a backupStorageAdapter) DeleteObjects(ctx context.Context, keys []string) error {
	return a.c.DeleteObjects(ctx, keys)
}
