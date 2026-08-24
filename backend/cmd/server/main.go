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
	"foldex/internal/mailoutbox"
	"foldex/internal/metrics"
	"foldex/internal/notemedia"
	"foldex/internal/oauthgoogle"
	"foldex/internal/pkg/keyfile"
	"foldex/internal/pkg/logsafe"
	"foldex/internal/pkg/secrets"
	"foldex/internal/policy"
	"foldex/internal/preview"
	"foldex/internal/push"
	"foldex/internal/roleperm"
	"foldex/internal/screenshot"
	"foldex/internal/server"
	"foldex/internal/settings"
	"foldex/internal/stats"
	"foldex/internal/storage"
	"foldex/internal/tracing"
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

	// SHARED_SECRET was removed outright. Operators with the variable still
	// exported would otherwise get a silently ignored setting; one release of
	// explicit warnings beats quiet confusion.
	if os.Getenv("SHARED_SECRET") != "" {
		logger.Warn("SHARED_SECRET is set but has been removed — the variable is ignored; delete it from the environment.")
	}

	if !cfg.AuthEnabled {
		// The one genuinely dangerous combination: no accounts. Every request
		// is attributed to the bootstrap admin, so anyone who can reach the
		// port owns the whole library. validateSecureDefaults already refuses
		// this on a non-loopback bind; warn here for the loopback case.
		logger.Warn("AUTH_ENABLED=0 — /api/* is reachable with no " +
			"credential at all, and every request is attributed to the bootstrap administrator. " +
			"Safe only on a loopback bind; turn AUTH_ENABLED back on before exposing this server.")
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Tracing installs the GLOBAL provider, so it must precede db.New — the
	// pool's otelpgx tracer resolves the provider per query and would pin the
	// no-op otherwise. Setup failure is a warning, never fatal: a telemetry
	// endpoint typo must not take the backend down.
	traceShutdown, err := tracing.Setup(rootCtx, tracing.Config{
		Endpoint:       cfg.OTelEndpoint,
		ServiceName:    "foldex-backend",
		ServiceVersion: os.Getenv("FOLDEX_VERSION"),
	})
	if err != nil {
		logger.Warn("tracing disabled — OTLP setup failed", "err", err)
	} else if traceShutdown != nil {
		logger.Info("tracing enabled", "endpoint", cfg.OTelEndpoint)
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := traceShutdown(ctx); err != nil {
				logger.Warn("tracing shutdown", "err", err)
			}
		}()
	}

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

	// The outbox is what makes mail survive a restart: a message is written in
	// the same transaction as the credential it carries, and the relay drains
	// the table afterwards. A queued row holds a live reset link, so the params
	// are encrypted — under a subkey DERIVED from the auth key rather than the
	// key itself, so the outbox and the TOTP seeds do not share a key.
	outbox, err := mailoutbox.NewFromMasterKey(authKey)
	if err != nil {
		logger.Error("mail outbox", "err", err)
		os.Exit(1)
	}
	// The transport decides who SENDS, and nothing above this line knows which
	// one is configured: the handlers write rows, the relay drains them, and
	// only the sink differs. That is the whole point of the split — an instance
	// with no broker loses horizontal scale and nothing else.
	outboxRepo := mailoutbox.NewRepository(pool)
	var mailSink mailoutbox.Sink = mailoutbox.NewInprocSink(outbox, mail)
	var closeMailSink func()
	var deadLetters *mailoutbox.DeadLetterWatcher
	if cfg.Mail.UsesBroker() {
		amqpCfg := mailoutbox.AMQPConfig{
			URL: cfg.Mail.AMQPURL,
			Topology: mailoutbox.Topology{
				Exchange:   cfg.Mail.AMQPExchange,
				Queue:      cfg.Mail.AMQPQueue,
				RoutingKey: cfg.Mail.AMQPRoutingKey,
			},
			Logger: logger,
		}
		sink, err := mailoutbox.NewAMQPSink(amqpCfg)
		if err != nil {
			logger.Error("mail transport", "err", err)
			os.Exit(1)
		}
		mailSink = sink
		closeMailSink = func() { _ = sink.Close() }
		// Handing delivery to a broker moves the truth about it out of the
		// database. The watcher brings the final outcome back, so a reset link
		// that died on the last rung of the retry ladder still shows as failed
		// instead of reading 'published' forever.
		deadLetters = mailoutbox.NewDeadLetterWatcher(outboxRepo, amqpCfg, logger)
		deadLetters.Start(context.Background())
	}
	mailRelay := mailoutbox.NewRelay(outboxRepo, mailSink, mailoutbox.Options{
		Batch:        cfg.Mail.OutboxBatch,
		PollInterval: time.Duration(cfg.Mail.OutboxPollSec) * time.Second,
	}, logger)
	mailRelay.Start(context.Background())
	if w := cfg.PlaintextBrokerWarning(); w != "" {
		logger.Warn(w)
	}
	logger.Info("mail transport ready", "transport", mailSink.Name(), "driver", mail.Driver())
	authRepo := auth.NewRepository(pool, auth.WithOutbox(outbox))
	cookieOpts := auth.CookieOptions{Secure: cfg.AuthCookieSecure, Domain: cfg.AuthCookieDomain}
	// Built before the middleware because the admin second-factor gate reads it
	// on every /api/admin request (ADR-37 §7.5).
	policyRepo := policy.NewRepository(pool)
	policyRepo.WarnUnenforceableFloor(rootCtx, logger)

	// The configurable half of the RBAC matrix (ADR-42). A failed load leaves
	// the compiled matrix in place and is logged rather than fatal: the stored
	// grants can only ever be a DELTA over locked entries the code guarantees,
	// so booting on the compiled matrix is the historical behaviour, while
	// refusing to boot would make one unreadable table an outage.
	grantsRepo := roleperm.NewRepository(pool)
	if err := grantsRepo.Load(rootCtx); err != nil {
		logger.Error("role permissions load; serving the compiled matrix", "err", err)
	}
	// Bounds how long a revocation takes to reach a replica that did not
	// perform it — and how long THIS process serves a stale matrix if the
	// refresh after its own write fails, which would otherwise be forever.
	_ = grantsRepo.StartReloading(rootCtx, roleperm.DefaultReloadInterval, logger)
	authMW := auth.NewMiddleware(authRepo, cookieOpts, logger,
		cfg.AuthEnabled && cfg.AuthRequire2FAForAdmins,
		auth.WithAdminFactorPolicy(policyRepo.RequiresTOTPForAdmins))
	authTTL := auth.SessionTTL{
		Access:   time.Duration(cfg.AuthAccessTTLMin) * time.Minute,
		Refresh:  time.Duration(cfg.AuthRefreshTTLDays) * 24 * time.Hour,
		Absolute: time.Duration(cfg.AuthAbsoluteTTLDays) * 24 * time.Hour,
		Grace:    time.Duration(cfg.AuthRefreshGraceSec) * time.Second,
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
		Repo: authRepo, MW: authMW, Mailer: mail, Cookies: cookieOpts,
		TTL: authTTL, Logger: logger, BaseURL: cfg.AuthPublicURL,
		Cipher: authCipher, CodeMAC: authCodeMAC, TOTPIssuer: cfg.AuthTOTPIssuer,
		Require2FAForAdmins: cfg.AuthRequire2FAForAdmins,
		Google:              google,
		Policy:              policyRepo,
	})
	adminHandler := auth.NewAdminHandler(authRepo, mail, logger, cfg.AuthPublicURL, policyRepo, grantsRepo)
	// The audit hook is passed as a function so internal/policy never imports
	// internal/auth — auth already imports policy for enforcement, and the other
	// direction would close the cycle.
	policyHandler := policy.NewHandler(policyRepo, logger, adminHandler.AuditPolicyChange, grantsRepo)
	folderHandler := folders.NewHandler(
		folders.NewRepository(pool), folderUnlockKey, settings.NewRepository(pool), grantsRepo,
	)

	// The sweeper prunes the DB rows and every process-local cache that grows
	// with traffic: the rate-limit buckets (keyed by attacker-supplied e-mail on
	// an unauthenticated endpoint), folder unlock attempts, and the last_seen_at
	// throttle map.
	sweeper := auth.NewSweeper(authRepo, logger,
		time.Duration(cfg.AuthSweepIntervalMin)*time.Minute,
		time.Duration(cfg.AuthSweepRetainDays)*24*time.Hour).
		WithInMemory(authHandler.SweepLimiters, folderHandler.SweepLimiters, authMW.SweepTouch)
	sweeper.Start(rootCtx)

	deps := server.Deps{
		Pool:                pool,
		Worker:              worker,
		Logger:              logger,
		Config:              cfg,
		Metrics:             metrics.New(pool),
		Trace:               traceMiddleware(traceShutdown),
		Storage:             storageClient,
		PushHandler:         pushHandler,
		LinkMetadataFetcher: linkMetadataAdapter{f: metadataFetcher},
		FolderUnlockKey:     folderUnlockKey,
		AuthHandler:         authHandler,
		AdminHandler:        adminHandler,
		// Without this the router falls back to the COMPILED matrix and the
		// content, import and backup-restore gates keep enforcing it: a
		// revocation would commit, audit, render as unticked, and change
		// nothing on those routes. The nil-means-compiled default exists for
		// tests; leaving it in force here is what turns a deliberate default
		// into unintended production state.
		Grants:         grantsRepo,
		PolicyHandler:  policyHandler,
		AuthMiddleware: authMW,
		FolderHandler:  folderHandler,
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
	srv := newHTTPServer(cfg.BindAddr+":"+cfg.Port, router)

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
	completed := waitForShutdown(shutCtx, shutdownHooks{
		shutdownHTTP: srv.Shutdown,
		stopMail: func() {
			mailRelay.Stop()
			if deadLetters != nil {
				deadLetters.Stop()
			}
			// After both loops have joined, so no publish is in flight against
			// a connection being torn out from under it.
			if closeMailSink != nil {
				closeMailSink()
			}
		},
		stopWorkers: func() {
			worker.Stop()
			if ccWorker != nil {
				ccWorker.Stop()
			}
		},
		closeScreenshots: screenshotPool.Close,
		waitSweeper:      sweeper.Wait,
		logger:           logger,
	})
	if !completed {
		logger.Error("shutdown deadline exceeded", "err", shutCtx.Err())
	}
	logger.Info("bye")
}

type shutdownHooks struct {
	shutdownHTTP     func(context.Context) error
	stopMail         func()
	stopWorkers      func()
	closeScreenshots func()
	waitSweeper      func()
	logger           *slog.Logger
}

func waitForShutdown(ctx context.Context, hooks shutdownHooks) bool {
	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		if err := hooks.shutdownHTTP(ctx); err != nil && hooks.logger != nil {
			hooks.logger.Error("graceful shutdown failed", "err", err)
		}
		// Drain HTTP first so no handler can hold an unpublished queue
		// reservation when dispatcher cancellation joins its workers.
		hooks.stopMail()
	}()
	go func() {
		defer wg.Done()
		hooks.stopWorkers()
	}()
	// Cancel Chromium concurrently with HTTP/worker drain so all subsystems
	// share one process-wide shutdown budget.
	go func() {
		defer wg.Done()
		hooks.closeScreenshots()
	}()
	go func() {
		defer wg.Done()
		hooks.waitSweeper()
	}()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

const requestTimeout = 2 * time.Minute

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		// BindAddr defaults to 127.0.0.1 (single-user threat model). Override
		// via BACKEND_BIND only when fronting with a reverse proxy —
		// config.validateSecureDefaults refuses the "wide open" combo at
		// boot (non-loopback bind with AUTH_ENABLED=0).
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       requestTimeout,
		WriteTimeout:      requestTimeout,
		IdleTimeout:       60 * time.Second,
	}
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

// traceMiddleware maps tracing.Setup's result onto Deps.Trace: only a
// successful Setup (non-nil shutdown) mounts the middleware; otherwise the
// router keeps tracing off and requests pay nothing.
func traceMiddleware(shutdown func(context.Context) error) func(http.Handler) http.Handler {
	if shutdown == nil {
		return nil
	}
	return tracing.Middleware
}
