// Command backup-agent is the operational backup service (ADR-43,
// docs/SDD-OPS-BACKUP.md): scheduled pg_dump of the whole database, encrypted
// with age and shipped to an external S3-compatible bucket, with state in the
// backup_run table and Prometheus metrics on its own port.
//
// It is a separate binary in a separate container because it is the ONLY
// process that may hold the external bucket's credentials: the web-exposed
// backend can request a run (an INSERT), never perform one. Its image derives
// from postgres:X-alpine so pg_dump — and, for the restore drill, a whole
// ephemeral server — ship version-matched with foldex-db.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/backup"
	"foldex/internal/backupagent"
	"foldex/internal/pkg/logsafe"
	"foldex/internal/storage"
)

// shutdownDeadline matches cmd/server and cmd/mailer.
const shutdownDeadline = 12 * time.Second

func main() {
	// Redactor on the ROOT handler before anything logs through it — this
	// process handles the database password and bucket credentials.
	logger := slog.New(logsafe.NewRedactHandler(
		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.SetDefault(logger)

	os.Exit(run(logger))
}

func run(logger *slog.Logger) int {
	cfg, err := backupagent.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DBURL())
	if err != nil {
		logger.Error("database pool", "err", err)
		return 1
	}
	defer pool.Close()

	store, err := storage.New(ctx, storage.Config{
		Endpoint:  cfg.S3Endpoint,
		Region:    cfg.S3Region,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
		Bucket:    cfg.S3Bucket,
		UseSSL:    cfg.S3UseSSL,
	}, logger)
	if err != nil {
		logger.Error("backup target bucket", "err", err)
		return 1
	}

	// The mirror's source is a SECOND client, pointed at the RustFS origin.
	// Same read path the backend uses; the external target above remains the
	// only place the BACKUP_S3_* credentials ever go.
	var mirrorSource backupagent.SourceBucket
	if cfg.MirrorEnabled() {
		// NewReadOnly, never New: a typo'd RUSTFS_BUCKET must fail the boot,
		// not be silently created empty and mirrored "successfully" forever.
		rustfs, err := storage.NewReadOnly(ctx, storage.Config{
			Endpoint:  cfg.RustFSEndpoint,
			AccessKey: cfg.RustFSAccessKey,
			SecretKey: cfg.RustFSSecretKey,
			Bucket:    cfg.RustFSBucket,
			UseSSL:    cfg.RustFSUseSSL,
		}, logger)
		if err != nil {
			logger.Error("mirror source bucket", "err", err)
			return 1
		}
		mirrorSource = backupagent.NewStorageUploader(rustfs)
	}

	agent, err := backupagent.New(cfg, pool, backupagent.NewStorageUploader(store), mirrorSource, logger)
	if err != nil {
		logger.Error("agent", "err", err)
		return 2
	}

	// user_zip is the only job that reads the SOURCE bucket, so its Service —
	// and the RustFS client under it — is constructed only when the job is
	// opted in: an instance without the anchor keeps booting with no RustFS
	// reachable at all.
	if cfg.UserZipAt.Enabled() {
		source, err := storage.NewReadOnly(ctx, storage.Config{
			Endpoint:  cfg.RustFSEndpoint,
			AccessKey: cfg.RustFSAccessKey,
			SecretKey: cfg.RustFSSecretKey,
			Bucket:    cfg.RustFSBucket,
			UseSSL:    cfg.RustFSUseSSL,
		}, logger)
		if err != nil {
			logger.Error("source bucket (user_zip)", "err", err)
			return 1
		}
		svc := backup.NewService(pool, backupagent.NewBackupSourceBucket(source), logger)
		userZip, err := backupagent.NewUserZipJob(cfg, pool, svc, backupagent.NewStorageUploader(store), logger)
		if err != nil {
			logger.Error("user_zip job", "err", err)
			return 2
		}
		agent.RegisterJob(backupagent.JobUserZip, cfg.UserZipAt, userZip.Run)
	}
	if err := agent.CheckSchema(ctx); err != nil {
		logger.Error("schema gate", "err", err)
		return 1
	}

	agent.Start(ctx)
	logger.Info("backup agent ready",
		"dump_at", cfg.DumpAt.String(),
		"drill_at", cfg.DrillAt.String(),
		"mirror_interval_min", cfg.MirrorIntervalMin,
		"userzip_at", cfg.UserZipAt.String(),
		"retention_mode", cfg.RetentionMode,
		"encrypted", len(cfg.AgeRecipients) > 0,
		"metrics_addr", cfg.MetricsAddr)

	<-ctx.Done()
	logger.Info("shutting down")
	done := make(chan struct{})
	go func() {
		agent.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownDeadline):
		logger.Warn("shutdown deadline elapsed; exiting anyway")
	}
	logger.Info("bye")
	return 0
}
