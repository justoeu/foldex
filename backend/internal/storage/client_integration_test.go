//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"errors"
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

	var objs []storage.ObjectInfo
	err = cli.WalkObjects(ctx, "images/", func(obj storage.ObjectInfo) error {
		objs = append(objs, obj)
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, objs)
	// ETag and LastModified ride along on every ListObjects page; the mirror
	// job's watermark diff depends on LastModified actually being filled.
	assert.NotEmpty(t, objs[0].ETag)
	assert.False(t, objs[0].LastModified.IsZero(),
		"a zero LastModified would make every watermark comparison copy the whole bucket")

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
	require.NoError(t, cli.Upload(ctx, "images/keep.bin", []byte("keep"), "application/octet-stream"))
	existing, err := cli.ExistingObjects(ctx, []string{"images/1.png", "images/2.bin", "images/missing.bin"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"images/1.png": true, "images/2.bin": true}, existing)

	require.NoError(t, cli.DeleteObjects(ctx, []string{"images/1.png", "images/2.bin", "images/missing.bin"}))
	exists, err = cli.ObjectExists(ctx, "images/1.png")
	require.NoError(t, err)
	assert.False(t, exists)
	exists, err = cli.ObjectExists(ctx, "images/keep.bin")
	require.NoError(t, err)
	assert.True(t, exists, "explicit-key delete must not remove an unrequested tenant key")

	require.NoError(t, cli.DeleteObjectsPrefix(ctx, "images/"))
}

func TestClient_WalkObjectsStreamsAndStopsOnCallbackError(t *testing.T) {
	ep, user, pass := startRustFS(t)
	ctx := context.Background()
	cli := newClientRetry(t, ctx, storage.Config{
		Endpoint: ep, AccessKey: user, SecretKey: pass, Bucket: "walk-objects", UseSSL: false,
	}, discardLogger())
	for i := 0; i < 4; i++ {
		require.NoError(t, cli.Upload(ctx, "images/"+string(rune('a'+i)), []byte("x"), "text/plain"))
	}

	stop := errors.New("stop listing")
	callbacks := 0
	err := cli.WalkObjects(ctx, "images/", func(storage.ObjectInfo) error {
		callbacks++
		return stop
	})
	assert.ErrorIs(t, err, stop)
	assert.Equal(t, 1, callbacks, "listing must stop without materializing the remaining page stream")
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
	_, err := cli.ExistingObjects(ctx, []string{"p/1", "p/2"})
	assert.ErrorIs(t, err, context.Canceled)
	err = cli.DeleteObjects(ctx, []string{"p/1", "p/2"})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestNewReadOnly_RefusesAMissingBucketInsteadOfCreatingIt(t *testing.T) {
	ep, user, pass := startRustFS(t)
	ctx := context.Background()

	_, err := storage.NewReadOnly(ctx, storage.Config{
		Endpoint: ep, AccessKey: user, SecretKey: pass, Bucket: "typo-bucket-name",
	}, discardLogger())
	if err == nil {
		t.Fatal("a bucket that does not exist must refuse — silently creating it is the empty mirror that succeeds forever")
	}

	// And an existing bucket opens normally.
	created, err := storage.New(ctx, storage.Config{
		Endpoint: ep, AccessKey: user, SecretKey: pass, Bucket: "real-bucket",
	}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	_ = created
	ro, err := storage.NewReadOnly(ctx, storage.Config{
		Endpoint: ep, AccessKey: user, SecretKey: pass, Bucket: "real-bucket",
	}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := ro.Upload(ctx, "k", []byte("v"), "text/plain"); err != nil {
		t.Fatal(err)
	}
}
