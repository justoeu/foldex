//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/storage"
)

func TestClient_UploadGetListDelete(t *testing.T) {
	ep, user, pass := startRustFS(t)
	ctx := context.Background()
	logger := discardLogger()

	cli := newClientRetry(t, ctx, storage.Config{
		Endpoint:  ep,
		AccessKey: user,
		SecretKey: pass,
		Bucket:    "foldex-test",
		UseSSL:    false,
	}, logger)

	payload := []byte("hello-rustfs")
	require.NoError(t, cli.Upload(ctx, "images/1.png", payload, "image/png"))

	got, ct, err := cli.GetObject(ctx, "images/1.png")
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	assert.Equal(t, "image/png", ct)

	exists, err := cli.ObjectExists(ctx, "images/1.png")
	require.NoError(t, err)
	assert.True(t, exists)

	objs, err := cli.ListObjects(ctx, "images/")
	require.NoError(t, err)
	require.NotEmpty(t, objs)

	st, err := cli.Stats(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, st.Objects, int64(1))
	assert.GreaterOrEqual(t, st.TotalBytes, int64(len(payload)))

	rc, err := cli.OpenObject(ctx, "images/1.png")
	require.NoError(t, err)
	b, err := io.ReadAll(rc)
	_ = rc.Close()
	require.NoError(t, err)
	assert.Equal(t, payload, b)

	require.NoError(t, cli.PutObjectStream(ctx, "images/2.bin", bytes.NewReader([]byte("xyz")), 3, "application/octet-stream"))

	require.NoError(t, cli.DeleteObject(ctx, "images/1.png"))
	exists, err = cli.ObjectExists(ctx, "images/1.png")
	require.NoError(t, err)
	assert.False(t, exists)

	require.NoError(t, cli.DeleteObjectsPrefix(ctx, "images/"))
}

func TestClient_MissingObjectPaths(t *testing.T) {
	ep, user, pass := startRustFS(t)
	ctx := context.Background()
	logger := discardLogger()

	cli := newClientRetry(t, ctx, storage.Config{
		Endpoint:  ep,
		AccessKey: user,
		SecretKey: pass,
		Bucket:    "foldex-missing",
		UseSSL:    false,
	}, logger)

	_, _, err := cli.GetObject(ctx, "no/such/key")
	require.Error(t, err)

	_, err = cli.OpenObject(ctx, "no/such/key")
	require.Error(t, err)

	exists, err := cli.ObjectExists(ctx, "no/such/key")
	require.NoError(t, err)
	assert.False(t, exists)

	require.NoError(t, cli.DeleteObject(ctx, "no/such/key"))
	require.NoError(t, cli.DeleteObjectsPrefix(ctx, "nothing/"))

	cli2 := newClientRetry(t, ctx, storage.Config{
		Endpoint:  ep,
		AccessKey: user,
		SecretKey: pass,
		Bucket:    "foldex-missing",
		UseSSL:    false,
	}, logger)
	require.NoError(t, cli2.Upload(ctx, "a/b.bin", []byte("z"), "application/octet-stream"))
	require.NoError(t, cli2.DeleteObjectsPrefix(ctx, "a/"))
}

func TestClient_NewCreatesBucket(t *testing.T) {
	ep, user, pass := startRustFS(t)
	ctx := context.Background()
	logger := discardLogger()
	cli := newClientRetry(t, ctx, storage.Config{
		Endpoint:  ep,
		AccessKey: user,
		SecretKey: pass,
		Bucket:    "brand-new-bucket",
		UseSSL:    false,
	}, logger)
	require.NoError(t, cli.Upload(ctx, "x", []byte("1"), "text/plain"))
	st, err := cli.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), st.Objects)
}

func TestClient_CancelledContextOnPrefixDelete(t *testing.T) {
	ep, user, pass := startRustFS(t)
	logger := discardLogger()
	cli := newClientRetry(t, context.Background(), storage.Config{
		Endpoint: ep, AccessKey: user, SecretKey: pass, Bucket: "cancel-bucket", UseSSL: false,
	}, logger)
	require.NoError(t, cli.Upload(context.Background(), "p/1", []byte("a"), "text/plain"))
	require.NoError(t, cli.Upload(context.Background(), "p/2", []byte("b"), "text/plain"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = cli.DeleteObjectsPrefix(ctx, "p/")
}
