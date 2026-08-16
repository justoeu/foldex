//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"foldex/internal/storage"
)

// Pin must stay in lockstep with docker-compose.services.yml (CLAUDE.md §1).
const rustfsImage = "rustfs/rustfs:1.0.0-rc.2@sha256:7d6d361c49c08d427250fb59aae5d78df83d644c3405d9ccf4b21cda0b0692d0"

const (
	rustfsUser = "rustfsadmin"
	rustfsPass = "rustfsadmin"
)

// startRustFS boots a single-disk RustFS container and returns host:port + creds.
func startRustFS(t *testing.T) (endpoint, user, pass string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	req := testcontainers.ContainerRequest{
		Image:        rustfsImage,
		ExposedPorts: []string{"9000/tcp"},
		Env: map[string]string{
			"RUSTFS_ACCESS_KEY":               rustfsUser,
			"RUSTFS_SECRET_KEY":               rustfsPass,
			"RUSTFS_ADDRESS":                  "0.0.0.0:9000",
			"RUSTFS_CONSOLE_ENABLE":           "false",
			"RUSTFS_VOLUMES":                  "/data",
			"RUSTFS_UNSAFE_BYPASS_DISK_CHECK": "true",
		},
		WaitingFor: wait.ForHTTP("/health").
			WithPort("9000/tcp").
			WithStartupTimeout(2 * time.Minute),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })

	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "9000/tcp")
	require.NoError(t, err)
	return fmt.Sprintf("%s:%s", host, port.Port()), rustfsUser, rustfsPass
}

// newClientRetry absorbs the short interval between the health endpoint and
// S3 bucket/IAM readiness.
func newClientRetry(t *testing.T, ctx context.Context, cfg storage.Config, logger *slog.Logger) *storage.Client {
	t.Helper()
	var last error
	for i := 0; i < 40; i++ {
		cli, err := storage.New(ctx, cfg, logger)
		if err == nil {
			return cli
		}
		last = err
		time.Sleep(250 * time.Millisecond)
	}
	require.NoError(t, last)
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
