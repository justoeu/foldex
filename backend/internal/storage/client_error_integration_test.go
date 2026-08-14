//go:build integration

package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Pin must stay in lockstep with docker-compose.services.yml (CLAUDE.md §1).
const rustfsImage = "rustfs/rustfs:1.0.0-beta.12"

func startRustFSInternal(t *testing.T) (endpoint, user, pass string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	const u, p = "rustfsadmin", "rustfsadmin"
	req := testcontainers.ContainerRequest{
		Image:        rustfsImage,
		ExposedPorts: []string{"9000/tcp"},
		Env: map[string]string{
			"RUSTFS_ACCESS_KEY":               u,
			"RUSTFS_SECRET_KEY":               p,
			"RUSTFS_ADDRESS":                  "0.0.0.0:9000",
			"RUSTFS_CONSOLE_ENABLE":           "false",
			"RUSTFS_VOLUMES":                  "/data",
			"RUSTFS_UNSAFE_BYPASS_DISK_CHECK": "true",
		},
		WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
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
	return fmt.Sprintf("%s:%s", host, port.Port()), u, p
}

func newInternalClient(t *testing.T) *Client {
	t.Helper()
	ep, user, pass := startRustFSInternal(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var last error
	for i := 0; i < 40; i++ {
		cli, err := New(ctx, Config{
			Endpoint: ep, AccessKey: user, SecretKey: pass, Bucket: "err-bucket", UseSSL: false,
		}, logger)
		if err == nil {
			return cli
		}
		last = err
		time.Sleep(250 * time.Millisecond)
	}
	require.NoError(t, last)
	return nil
}

func TestClient_ErrorPaths_WrongBucket(t *testing.T) {
	cli := newInternalClient(t)
	ctx := context.Background()
	cli.bucket = "definitely-missing-bucket-xyz"

	err := cli.Upload(ctx, "k", []byte("x"), "text/plain")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage:")

	err = cli.PutObjectStream(ctx, "k", bytes.NewReader([]byte("x")), 1, "text/plain")
	require.Error(t, err)

	_, _, err = cli.GetObject(ctx, "k")
	require.Error(t, err)

	_, err = cli.OpenObject(ctx, "k")
	require.Error(t, err)

	_, err = cli.ObjectExists(ctx, "k")
	if err != nil {
		assert.Contains(t, err.Error(), "storage:")
	}

	err = cli.DeleteObject(ctx, "k")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage:")

	_, err = cli.Stats(ctx)
	require.Error(t, err)

	err = cli.WalkObjects(ctx, "", func(ObjectInfo) error { return nil })
	require.Error(t, err)

	_ = cli.DeleteObjectsPrefix(ctx, "p/")
}

func TestClient_DeleteObject_NoSuchKeyTolerated(t *testing.T) {
	cli := newInternalClient(t)
	ctx := context.Background()
	require.NoError(t, cli.Upload(ctx, "once", []byte("1"), "text/plain"))
	require.NoError(t, cli.DeleteObject(ctx, "once"))
	require.NoError(t, cli.DeleteObject(ctx, "once"))
}

func TestNew_ExistingBucketSecondCall(t *testing.T) {
	ep, user, pass := startRustFSInternal(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{Endpoint: ep, AccessKey: user, SecretKey: pass, Bucket: "twice", UseSSL: false}
	var cli *Client
	var last error
	for i := 0; i < 40; i++ {
		cli, last = New(ctx, cfg, logger)
		if last == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	require.NoError(t, last)
	cli2, err := New(ctx, cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, cli)
	require.NotNil(t, cli2)
}

func TestNew_ConcurrentMakeBucketRace(t *testing.T) {
	ep, user, pass := startRustFSInternal(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	probe := Config{Endpoint: ep, AccessKey: user, SecretKey: pass, Bucket: "probe-ready", UseSSL: false}
	for i := 0; i < 40; i++ {
		if _, err := New(ctx, probe, logger); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	cfg := Config{Endpoint: ep, AccessKey: user, SecretKey: pass, Bucket: "race-bucket", UseSSL: false}
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := New(ctx, cfg, logger)
			errCh <- err
		}()
	}
	for i := 0; i < 8; i++ {
		require.NoError(t, <-errCh)
	}
}

func TestDeleteObjectsPrefix_WrongBucketErrors(t *testing.T) {
	cli := newInternalClient(t)
	cli.bucket = "no-such-bucket-for-prefix"
	_ = cli.DeleteObjectsPrefix(context.Background(), "x/")
}

func TestGetObject_ReadFullSuccess(t *testing.T) {
	cli := newInternalClient(t)
	ctx := context.Background()
	payload := bytes.Repeat([]byte("abcd"), 4096)
	require.NoError(t, cli.Upload(ctx, "big.bin", payload, "application/octet-stream"))
	got, ct, err := cli.GetObject(ctx, "big.bin")
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	assert.Equal(t, "application/octet-stream", ct)
}
