package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/abusepolicy"
	"foldex/internal/auth"
	"foldex/internal/changecheck"
	"foldex/internal/config"
	"foldex/internal/db"
	"foldex/internal/depstatus"
	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/mailer"
	"foldex/internal/mailoutbox"
	"foldex/internal/metrics"
	"foldex/internal/notemedia"
	"foldex/internal/oauthgoogle"
	"foldex/internal/pkg/keyfile"
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

type handles struct {
	ctx    context.Context
	logger *slog.Logger
	cfg    config.Config

	traceShutdown func(context.Context) error
	pool          *pgxpool.Pool
	storage       *storage.Client
	vapid         push.VAPIDKeys
	folderUnlock  []byte
	mail          mailHandle
	cipher        cipherHandle
	grants        *roleperm.Repository
	abuse         *abusepolicy.Cache
}

type mailHandle struct {
	sender      mailer.Mailer
	outbox      *mailoutbox.Outbox
	relay       *mailoutbox.Relay
	deadLetters *mailoutbox.DeadLetterWatcher
	closeSink   func()
	amqpCfg     mailoutbox.AMQPConfig
}

type cipherHandle struct {
	key     []byte
	cipher  *secrets.Cipher
	codeMAC *auth.CodeMAC
}

type runtime struct {
	logger         *slog.Logger
	deps           server.Deps
	worker         *preview.Worker
	ccWorker       *changecheck.Worker
	screenshotPool *screenshot.Pool
	mail           mailHandle
	sweeper        *auth.Sweeper
	traceShutdown  func(context.Context) error
	pool           *pgxpool.Pool
}

func boot(ctx context.Context, logger *slog.Logger, cfg config.Config) (*runtime, error) {
	h := &handles{ctx: ctx, logger: logger, cfg: cfg}
	h.traceShutdown = loadTracing(h)
	var err error
	if h.pool, err = loadPool(h); err != nil {
		return nil, err
	}
	h.storage = loadStorage(h)
	if h.vapid, err = loadVAPID(h); err != nil {
		h.pool.Close()
		return nil, err
	}
	if h.folderUnlock, err = loadFolderUnlock(h); err != nil {
		h.pool.Close()
		return nil, err
	}
	if h.cipher, err = loadAuthCipher(h); err != nil {
		h.pool.Close()
		return nil, err
	}
	if h.mail, err = loadMail(h); err != nil {
		h.pool.Close()
		return nil, err
	}
	h.grants = loadGrants(h)
	h.abuse = loadAbuseCache(h)
	return assembleDeps(h)
}

func loadTracing(h *handles) func(context.Context) error {
	shutdown, err := tracing.Setup(h.ctx, tracing.Config{
		Endpoint:       h.cfg.OTelEndpoint,
		ServiceName:    "foldex-backend",
		ServiceVersion: os.Getenv("FOLDEX_VERSION"),
	})
	if err != nil {
		h.logger.Warn("tracing disabled — OTLP setup failed", "err", err)
		return nil
	}
	if shutdown != nil {
		h.logger.Info("tracing enabled", "endpoint", h.cfg.OTelEndpoint)
	}
	return shutdown
}

func loadPool(h *handles) (*pgxpool.Pool, error) {
	pool, err := db.New(h.ctx, h.cfg.DBURL)
	if err != nil {
		return nil, fmt.Errorf("db connect failed: %w", err)
	}
	if err := db.CheckSchemaVersion(h.ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("schema check failed: %w", err)
	}
	return pool, nil
}

func loadStorage(h *handles) *storage.Client {
	sc, err := storage.New(h.ctx, storage.Config{
		Endpoint:  h.cfg.ObjectStore.Endpoint,
		AccessKey: h.cfg.ObjectStore.AccessKey,
		SecretKey: h.cfg.ObjectStore.SecretKey,
		Bucket:    h.cfg.ObjectStore.Bucket,
		UseSSL:    h.cfg.ObjectStore.UseSSL,
	}, h.logger)
	if err != nil {
		h.logger.Warn("object store unavailable — screenshot endpoints disabled", "err", err)
		return nil
	}
	notemedia.NewSweeper(h.pool, sc, h.logger).Start(h.ctx)
	return sc
}

func loadVAPID(h *handles) (push.VAPIDKeys, error) {
	vapid, err := push.LoadOrGenerate(
		h.cfg.VAPIDPublicKey, h.cfg.VAPIDPrivateKey, h.cfg.VAPIDSubject,
		h.cfg.VAPIDStatePath, h.cfg.VAPIDAutoGenerate, h.logger,
	)
	if err != nil {
		return push.VAPIDKeys{}, fmt.Errorf("vapid setup failed: %w", err)
	}
	return vapid, nil
}

func loadFolderUnlock(h *handles) ([]byte, error) {
	key, err := folders.LoadOrGenerateFolderUnlockKey(
		h.cfg.FolderUnlockKey, h.cfg.FolderUnlockKeyPath, h.cfg.FolderUnlockAutoGenerate, h.logger,
	)
	if err != nil {
		return nil, fmt.Errorf("folder unlock key setup failed: %w", err)
	}
	return key, nil
}

func loadMail(h *handles) (mailHandle, error) {
	mail, err := mailer.New(mailer.Config{
		Driver:             h.cfg.Mail.Driver,
		Host:               h.cfg.Mail.Host,
		Port:               h.cfg.Mail.Port,
		Username:           h.cfg.Mail.Username,
		Password:           h.cfg.Mail.Password,
		From:               h.cfg.Mail.From,
		FromName:           h.cfg.Mail.FromName,
		STARTTLS:           h.cfg.Mail.STARTTLS,
		TLS:                h.cfg.Mail.TLS,
		InsecureSkipVerify: h.cfg.Mail.InsecureSkipVerify,
	}, h.logger)
	if err != nil {
		return mailHandle{}, fmt.Errorf("mailer setup failed: %w", err)
	}
	outbox, err := mailoutbox.NewFromMasterKey(h.cipher.key)
	if err != nil {
		return mailHandle{}, fmt.Errorf("mail outbox: %w", err)
	}
	outboxRepo := mailoutbox.NewRepository(h.pool)
	var sink mailoutbox.Sink = mailoutbox.NewInprocSink(outbox, mail)
	mh := mailHandle{sender: mail, outbox: outbox}
	if h.cfg.Mail.UsesBroker() {
		mh.amqpCfg = mailoutbox.AMQPConfig{
			URL: h.cfg.Mail.AMQPURL,
			Topology: mailoutbox.Topology{
				Exchange:   h.cfg.Mail.AMQPExchange,
				Queue:      h.cfg.Mail.AMQPQueue,
				RoutingKey: h.cfg.Mail.AMQPRoutingKey,
			},
			Logger: h.logger,
		}
		amqpSink, err := mailoutbox.NewAMQPSink(mh.amqpCfg)
		if err != nil {
			return mailHandle{}, fmt.Errorf("mail transport: %w", err)
		}
		sink = amqpSink
		mh.closeSink = func() { _ = amqpSink.Close() }
		mh.deadLetters = mailoutbox.NewDeadLetterWatcher(outboxRepo, mh.amqpCfg, h.logger)
		mh.deadLetters.Start(context.Background())
	}
	mh.relay = mailoutbox.NewRelay(outboxRepo, sink, mailoutbox.Options{
		Batch:        h.cfg.Mail.OutboxBatch,
		PollInterval: time.Duration(h.cfg.Mail.OutboxPollSec) * time.Second,
	}, h.logger)
	mh.relay.Start(context.Background())
	if w := h.cfg.PlaintextBrokerWarning(); w != "" {
		h.logger.Warn(w)
	}
	h.logger.Info("mail transport ready", "transport", sink.Name(), "driver", mail.Driver())
	return mh, nil
}

func loadAuthCipher(h *handles) (cipherHandle, error) {
	authKey, err := keyfile.Load(keyfile.Config{
		Name:           "auth encryption key",
		EnvVar:         "AUTH_ENCRYPTION_KEY",
		PathVar:        "AUTH_ENCRYPTION_KEY_PATH",
		EnvValue:       h.cfg.AuthEncryptionKey,
		Path:           h.cfg.AuthEncryptionKeyPath,
		AutoGenerate:   h.cfg.AuthEncryptionAutoGen,
		AllowEphemeral: false,
	}, h.logger)
	if err != nil {
		return cipherHandle{}, fmt.Errorf("auth encryption key: %w", err)
	}
	authCipher, err := secrets.NewCipher(authKey)
	if err != nil {
		return cipherHandle{}, fmt.Errorf("auth cipher: %w", err)
	}
	authCodeMAC, err := auth.NewCodeMAC(authKey)
	if err != nil {
		return cipherHandle{}, fmt.Errorf("auth code MAC: %w", err)
	}
	return cipherHandle{key: authKey, cipher: authCipher, codeMAC: authCodeMAC}, nil
}

func loadGrants(h *handles) *roleperm.Repository {
	grantsRepo := roleperm.NewRepository(h.pool)
	if err := grantsRepo.Load(h.ctx); err != nil {
		h.logger.Error("role permissions load; serving the compiled matrix", "err", err)
	}
	_ = grantsRepo.StartReloading(h.ctx, roleperm.DefaultReloadInterval, h.logger)
	return grantsRepo
}

func loadAbuseCache(h *handles) *abusepolicy.Cache {
	return abusepolicy.NewCache(abusepolicy.NewRepository(h.pool), abusepolicy.DefaultTTL, h.logger)
}

func assembleDeps(h *handles) (*runtime, error) {
	worker := preview.NewWorker(h.pool, h.cfg.PreviewConcurrency, time.Duration(h.cfg.PreviewTimeoutSec)*time.Second, h.logger)
	screenshotPool := screenshot.NewPool()
	metadataFetcher := preview.NewFetcher(time.Duration(h.cfg.PreviewTimeoutSec) * time.Second)
	if h.storage != nil {
		worker.WithScreenshotFallback(screenshotPool, h.storage)
	}
	worker.Start(h.ctx)

	pushRepo := push.NewRepository(h.pool)
	pushSender := push.NewSender(h.vapid, pushRepo, h.logger)
	pushHandler := push.NewHandler(h.vapid, pushRepo, pushSender)

	var ccWorker *changecheck.Worker
	if h.cfg.ChangeCheckEnabled {
		ccFetcher := preview.NewFetcher(time.Duration(h.cfg.ChangeCheckFetchTimeoutSec) * time.Second)
		ccWorker = changecheck.New(
			links.NewRepository(h.pool),
			ccFetcher,
			pushSenderAdapter{s: pushSender},
			changecheck.Options{
				Concurrency:  h.cfg.ChangeCheckConcurrency,
				ScanInterval: time.Duration(h.cfg.ChangeCheckScanIntervalSec) * time.Second,
				FetchTimeout: time.Duration(h.cfg.ChangeCheckFetchTimeoutSec) * time.Second,
			},
			h.logger,
		)
		ccWorker.Start(h.ctx)
	}

	authRepo := auth.NewRepository(h.pool, auth.WithOutbox(h.mail.outbox))
	cookieOpts := auth.CookieOptions{Secure: h.cfg.AuthCookieSecure, Domain: h.cfg.AuthCookieDomain}
	policyRepo := policy.NewRepository(h.pool)
	policyRepo.WarnUnenforceableFloor(h.ctx, h.logger)

	authMW := auth.NewMiddleware(authRepo, cookieOpts, h.logger,
		h.cfg.AuthEnabled && h.cfg.AuthRequire2FAForAdmins,
		auth.WithAdminFactorPolicy(policyRepo.RequiresTOTPForAdmins))
	authTTL := auth.SessionTTL{
		Access:   time.Duration(h.cfg.AuthAccessTTLMin) * time.Minute,
		Refresh:  time.Duration(h.cfg.AuthRefreshTTLDays) * 24 * time.Hour,
		Absolute: time.Duration(h.cfg.AuthAbsoluteTTLDays) * 24 * time.Hour,
		Grace:    time.Duration(h.cfg.AuthRefreshGraceSec) * time.Second,
	}
	google := oauthgoogle.New(oauthgoogle.Config{
		ClientID:     h.cfg.GoogleClientID,
		ClientSecret: h.cfg.GoogleClientSecret,
		RedirectURL:  h.cfg.GoogleRedirectURL(),
	})
	if google.Enabled() {
		h.logger.Info("google oauth enabled", "redirect_uri", h.cfg.GoogleRedirectURL())
	}

	authHandler := auth.NewHandler(auth.HandlerConfig{
		Repo: authRepo, MW: authMW, Mailer: h.mail.sender, Cookies: cookieOpts,
		TTL: authTTL, Logger: h.logger, BaseURL: h.cfg.AuthPublicURL,
		Cipher: h.cipher.cipher, CodeMAC: h.cipher.codeMAC, TOTPIssuer: h.cfg.AuthTOTPIssuer,
		Require2FAForAdmins: h.cfg.AuthRequire2FAForAdmins,
		Google:              google,
		Policy:              policyRepo,
		Abuse:               h.abuse,
		Grants:              h.grants,
	})
	adminHandler := auth.NewAdminHandler(authRepo, h.mail.sender, h.logger, h.cfg.AuthPublicURL, policyRepo, h.grants)
	policyHandler := policy.NewHandler(policyRepo, h.logger, adminHandler.AuditPolicyChange, h.grants)
	folderHandler := folders.NewHandler(
		folders.NewRepository(h.pool), h.folderUnlock, settings.NewRepository(h.pool), h.grants,
	)

	sweeper := auth.NewSweeper(authRepo, h.logger,
		time.Duration(h.cfg.AuthSweepIntervalMin)*time.Minute,
		time.Duration(h.cfg.AuthSweepRetainDays)*24*time.Hour).
		WithInMemory(authHandler.SweepLimiters, folderHandler.SweepLimiters, authMW.SweepTouch)
	sweeper.Start(h.ctx)

	appMetrics := metrics.New(h.pool)
	depStatus := depstatus.New(depstatus.WithLogger(h.logger))
	if h.storage != nil {
		depStatus.Add(depstatus.ObjectStore, h.storage.Ping)
	} else if h.cfg.ObjectStore.Endpoint != "" {
		depStatus.Add(depstatus.ObjectStore, depstatus.AlwaysUnreachable)
	}
	if h.cfg.Mail.UsesBroker() {
		broker := h.mail.amqpCfg
		depStatus.Add(depstatus.MailBroker, func(ctx context.Context) error {
			return mailoutbox.Ping(ctx, broker)
		})
	}
	if err := appMetrics.Registerer().Register(depStatus); err != nil {
		h.logger.Error("dependency metrics", "err", err)
	}
	depStatus.Start(h.ctx)

	deps := server.Deps{
		Pool:                h.pool,
		Worker:              worker,
		Logger:              h.logger,
		Config:              h.cfg,
		Metrics:             appMetrics,
		DepStatus:           depStatus,
		Trace:               traceMiddleware(h.traceShutdown),
		Storage:             h.storage,
		PushHandler:         pushHandler,
		LinkMetadataFetcher: linkMetadataAdapter{f: metadataFetcher, render: screenshotRenderer{p: screenshotPool}},
		FolderUnlockKey:     h.folderUnlock,
		AuthHandler:         authHandler,
		AdminHandler:        adminHandler,
		AuthRepo:            authRepo,
		Grants:              h.grants,
		PolicyHandler:       policyHandler,
		AuthMiddleware:      authMW,
		FolderHandler:       folderHandler,
		AbusePolicy:         h.abuse,
	}
	if h.storage != nil {
		deps.Screenshotter = screenshotPool
		deps.ScreenshotURL = preview.IsPublicURL
		deps.StorageStatter = storageStatsAdapter{c: h.storage, keys: stats.NewRepository(h.pool).ObjectKeys}
		deps.StorageBucket = backupStorageAdapter{c: h.storage}
	}

	return &runtime{
		logger:         h.logger,
		deps:           deps,
		worker:         worker,
		ccWorker:       ccWorker,
		screenshotPool: screenshotPool,
		mail:           h.mail,
		sweeper:        sweeper,
		traceShutdown:  h.traceShutdown,
		pool:           h.pool,
	}, nil
}

func (m mailHandle) stop() {
	if m.relay != nil {
		m.relay.Stop()
	}
	if m.deadLetters != nil {
		m.deadLetters.Stop()
	}
	if m.closeSink != nil {
		m.closeSink()
	}
}

func (rt *runtime) close() {
	if rt.traceShutdown != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := rt.traceShutdown(ctx); err != nil {
			rt.logger.Warn("tracing shutdown", "err", err)
		}
	}
	if rt.pool != nil {
		rt.pool.Close()
	}
}

func (rt *runtime) hooks() shutdownHooks {
	return shutdownHooks{
		shutdownHTTP: nil,
		stopMail:     rt.mail.stop,
		stopWorkers: func() {
			rt.worker.Stop()
			if rt.ccWorker != nil {
				rt.ccWorker.Stop()
			}
		},
		closeScreenshots: rt.screenshotPool.Close,
		waitSweeper:      rt.sweeper.Wait,
		logger:           rt.logger,
	}
}
