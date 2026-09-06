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

	"foldex/internal/backup"
	"foldex/internal/changecheck"
	"foldex/internal/config"
	"foldex/internal/links"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/logsafe"
	"foldex/internal/preview"
	"foldex/internal/push"
	"foldex/internal/screenshot"
	"foldex/internal/server"
	"foldex/internal/stats"
	"foldex/internal/storage"
	"foldex/internal/tracing"
)

func main() {
	logger := slog.New(logsafe.NewRedactHandler(
		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}
	warnDeprecatedEnv(logger, cfg)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rt, err := boot(rootCtx, logger, cfg)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
	defer rt.close()

	srv := newHTTPServer(cfg.BindAddr+":"+cfg.Port, server.New(rt.deps))
	go serveHTTP(srv, logger, stop)

	<-rootCtx.Done()
	logger.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	hooks := rt.hooks()
	hooks.shutdownHTTP = srv.Shutdown
	if !waitForShutdown(shutCtx, hooks) {
		logger.Error("shutdown deadline exceeded", "err", shutCtx.Err())
	}
	logger.Info("bye")
}

func warnDeprecatedEnv(logger *slog.Logger, cfg config.Config) {
	if os.Getenv("SHARED_SECRET") != "" {
		logger.Warn("SHARED_SECRET is set but has been removed — the variable is ignored; delete it from the environment.")
	}
	if !cfg.AuthEnabled {
		logger.Warn("AUTH_ENABLED=0 — /api/* is reachable with no " +
			"credential at all, and every request is attributed to the bootstrap administrator. " +
			"Safe only on a loopback bind; turn AUTH_ENABLED back on before exposing this server.")
	}
}

func serveHTTP(srv *http.Server, logger *slog.Logger, stop context.CancelFunc) {
	logger.Info("server starting", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("listen failed", "err", err)
		stop()
	}
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
type linkMetadataAdapter struct {
	f      *preview.Fetcher
	render preview.Renderer
}

func (a linkMetadataAdapter) FetchMetadata(ctx context.Context, pageURL string) (links.URLMetadata, error) {
	r, err := a.f.FetchThenRender(ctx, pageURL, a.render)
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

// screenshotRenderer maps screenshot.Pool.ExtractMetadata onto preview.Renderer
// without pulling the preview package into screenshot (or vice versa).
type screenshotRenderer struct{ p *screenshot.Pool }

func (s screenshotRenderer) ExtractMetadata(ctx context.Context, pageURL string) (preview.Result, error) {
	if s.p == nil {
		return preview.Result{}, errors.New("screenshot pool unavailable")
	}
	// Same pre-gate as the screenshot fallback and the manual capture
	// endpoint (INV-083/085). The HTTP fetch may still hit RFC1918 when
	// PREVIEW_STRICT_SSRF is off; Chromium must not.
	if !preview.IsPublicURL(ctx, pageURL) {
		return preview.Result{}, errors.New("private target")
	}
	md, err := s.p.ExtractMetadata(ctx, pageURL)
	if err != nil {
		return preview.Result{}, err
	}
	return preview.Result{
		Title:       md.Title,
		Description: md.Description,
		FaviconURL:  md.FaviconURL,
		OGImageURL:  md.OGImageURL,
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
type storageStatsAdapter struct {
	c    *storage.Client
	keys func(context.Context, authctx.UserID) ([]string, error)
}

func (a storageStatsAdapter) Stats(ctx context.Context, uid authctx.UserID) (stats.StorageStats, error) {
	var owned map[string]struct{}
	if a.keys != nil {
		list, err := a.keys(ctx, uid)
		if err != nil {
			return stats.StorageStats{}, err
		}
		owned = make(map[string]struct{}, len(list))
		for _, key := range list {
			owned[key] = struct{}{}
		}
	}
	s, err := a.c.StatsOwned(ctx, owned)
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
