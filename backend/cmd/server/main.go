package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"foldex/internal/backup"
	"foldex/internal/config"
	"foldex/internal/db"
	"foldex/internal/preview"
	"foldex/internal/screenshot"
	"foldex/internal/server"
	"foldex/internal/stats"
	"foldex/internal/storage"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}

	if cfg.SharedSecret == "" {
		logger.Warn("SHARED_SECRET not set — /api/* is reachable without authentication. " +
			"Safe for localhost-only deployments; set SHARED_SECRET before exposing this server to any network.")
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.New(rootCtx, cfg.DBURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	worker := preview.NewWorker(pool, cfg.PreviewConcurrency, time.Duration(cfg.PreviewTimeoutSec)*time.Second, logger)

	// MinIO storage is optional — if it cannot be reached, we log a warning
	// and disable the screenshot endpoints rather than refusing to start.
	var storageClient *storage.Client
	sc, err := storage.New(rootCtx, storage.Config{
		Endpoint:  cfg.MinIO.Endpoint,
		AccessKey: cfg.MinIO.AccessKey,
		SecretKey: cfg.MinIO.SecretKey,
		Bucket:    cfg.MinIO.Bucket,
		UseSSL:    cfg.MinIO.UseSSL,
	}, logger)
	if err != nil {
		logger.Warn("minio unavailable — screenshot endpoints disabled", "err", err)
	} else {
		storageClient = sc
	}

	// Wire the screenshot fallback before starting the worker. When MinIO is
	// up the worker will, after each preview, capture a screenshot for links
	// that have no og:image AND resolve to a public host.
	if storageClient != nil {
		worker.WithScreenshotFallback(screenshotFunc(screenshot.Capture), storageClient)
	}
	worker.Start(rootCtx)

	deps := server.Deps{
		Pool:    pool,
		Worker:  worker,
		Logger:  logger,
		Config:  cfg,
		Storage: storageClient,
	}
	if storageClient != nil {
		deps.Screenshotter = screenshotFunc(screenshot.Capture)
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

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
	worker.Stop()
	logger.Info("bye")
}

// screenshotFunc is a function adapter that satisfies links.Screenshotter.
type screenshotFunc func(ctx context.Context, pageURL string) ([]byte, error)

func (f screenshotFunc) Capture(ctx context.Context, pageURL string) ([]byte, error) {
	return f(ctx, pageURL)
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

func (a backupStorageAdapter) ListObjects(ctx context.Context, prefix string) ([]backup.ObjectInfo, error) {
	in, err := a.c.ListObjects(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]backup.ObjectInfo, len(in))
	for i, o := range in {
		out[i] = backup.ObjectInfo{Key: o.Key, Size: o.Size}
	}
	return out, nil
}

func (a backupStorageAdapter) OpenObject(ctx context.Context, key string) (io.ReadCloser, error) {
	return a.c.OpenObject(ctx, key)
}

func (a backupStorageAdapter) PutObjectStream(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	return a.c.PutObjectStream(ctx, key, r, size, contentType)
}

func (a backupStorageAdapter) ObjectExists(ctx context.Context, key string) (bool, error) {
	return a.c.ObjectExists(ctx, key)
}

func (a backupStorageAdapter) DeleteObjectsPrefix(ctx context.Context, prefix string) error {
	return a.c.DeleteObjectsPrefix(ctx, prefix)
}
