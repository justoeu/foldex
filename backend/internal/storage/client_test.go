package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/ports"
)

// Client holds a concrete *minio.Client, so we can't swap in a fake. The unit
// tests here drive the helpers and the construction/error paths directly; the
// full PutObject/GetObject surface is covered by client_integration_test.go
// against a real RustFS.

func TestExport_DoesNotStatObjectsAlreadyListed(t *testing.T) {
	payload := []byte("img-bytes")
	var stats atomic.Int64
	objects := []listedObject{
		{Key: "images/1.jpg", Size: int64(len(payload))},
		{Key: "screenshots/2.jpg", Size: int64(len(payload))},
	}
	c := &Client{
		listFn: func(ctx context.Context) <-chan listedObject {
			ch := make(chan listedObject, len(objects))
			for _, object := range objects {
				ch <- object
			}
			close(ch)
			return ch
		},
		statFn: func(context.Context, string) error {
			stats.Add(1)
			return nil
		},
		getFn: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		},
	}

	items, err := c.collectListing(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 2)
	for _, object := range items {
		require.Positive(t, object.Size, "listing already returned Size")
		rc, err := c.OpenObject(context.Background(), object.Key)
		require.NoError(t, err)
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		require.NoError(t, err)
		assert.Equal(t, payload, got)
	}
	assert.Zero(t, stats.Load(), "export must not StatObject per key after List already returned Size")
}

func TestStorageStats_DoesNotRescanWholeBucketOnEveryGet(t *testing.T) {
	const objectCount = 20
	objects := make([]listedObject, 0, objectCount+1)
	for i := 0; i < objectCount; i++ {
		objects = append(objects, listedObject{Key: fmt.Sprintf("screenshots/%d.jpg", i), Size: 10})
	}
	objects = append(objects, listedObject{Key: "screenshots/999.jpg", Size: 50}) // other tenant

	t.Run("second get inside TTL does not re-walk", func(t *testing.T) {
		var yields atomic.Int64
		c := &Client{listFn: countingList(&yields, objects, nil)}
		first, err := c.Stats(context.Background())
		require.NoError(t, err)
		require.Equal(t, int64(objectCount+1), first.Objects)
		require.Equal(t, int64(objectCount)*10+50, first.TotalBytes)
		require.Equal(t, int64(objectCount+1), yields.Load())

		second, err := c.Stats(context.Background())
		require.NoError(t, err)
		assert.Equal(t, first, second)
		assert.Equal(t, int64(objectCount+1), yields.Load(),
			"second Stats inside TTL must not re-walk ListObjects")
	})

	t.Run("cancelled ctx stops the iterator", func(t *testing.T) {
		started := make(chan struct{})
		block := make(chan struct{})
		var yields atomic.Int64
		c := &Client{listFn: countingList(&yields, objects, &listGate{started: started, block: block})}
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			_, err := c.Stats(ctx)
			errCh <- err
		}()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("list never started")
		}
		cancel()
		select {
		case err := <-errCh:
			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
		case <-time.After(2 * time.Second):
			t.Fatal("cancelled Stats did not return")
		}
		assert.Less(t, yields.Load(), int64(objectCount+1), "cancelled ctx must stop the iterator")
		close(block)
	})

	t.Run("concurrent gets share one in-flight listing", func(t *testing.T) {
		started := make(chan struct{})
		block := make(chan struct{})
		var yields atomic.Int64
		c := &Client{listFn: countingList(&yields, objects, &listGate{started: started, block: block})}

		var wg sync.WaitGroup
		errCh := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := c.Stats(context.Background())
				errCh <- err
			}()
		}
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("shared list never started")
		}
		close(block)
		wg.Wait()
		close(errCh)
		for err := range errCh {
			require.NoError(t, err)
		}
		assert.Equal(t, int64(objectCount+1), yields.Load(),
			"concurrent Stats must share one in-flight listing")
	})

	t.Run("does not count other tenants", func(t *testing.T) {
		var yields atomic.Int64
		c := &Client{listFn: countingList(&yields, objects, nil)}
		owned := map[string]struct{}{"screenshots/1.jpg": {}, "screenshots/2.jpg": {}}
		got, err := c.StatsOwned(context.Background(), owned)
		require.NoError(t, err)
		assert.Equal(t, int64(2), got.Objects)
		assert.Equal(t, int64(20), got.TotalBytes)
		_, err = c.StatsOwned(context.Background(), owned)
		require.NoError(t, err)
		assert.Equal(t, int64(objectCount+1), yields.Load(),
			"cached owner stats must not re-walk the shared bucket")
	})
}

type listGate struct {
	started chan struct{}
	block   chan struct{}
}

func countingList(yields *atomic.Int64, objects []listedObject, gate *listGate) func(context.Context) <-chan listedObject {
	return func(ctx context.Context) <-chan listedObject {
		ch := make(chan listedObject)
		go func() {
			defer close(ch)
			if gate != nil {
				select {
				case gate.started <- struct{}{}:
				default:
				}
				select {
				case <-ctx.Done():
					return
				case <-gate.block:
				}
			}
			for _, obj := range objects {
				if err := ctx.Err(); err != nil {
					return
				}
				yields.Add(1)
				select {
				case <-ctx.Done():
					return
				case ch <- obj:
				}
			}
		}()
		return ch
	}
}

func TestCheckServeSize(t *testing.T) {
	assert.ErrorIs(t, ErrObjectTooLarge, ports.ErrObjectTooLarge)
	require.NoError(t, checkServeSize(0))
	require.NoError(t, checkServeSize(1024))
	require.NoError(t, checkServeSize(MaxServeObjectBytes))
	err := checkServeSize(MaxServeObjectBytes + 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObjectTooLarge)
	err = checkServeSize(-1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObjectTooLarge)
}

func TestExplicitKeyPrefixesAreBoundedByNamespace(t *testing.T) {
	assert.Equal(t, []string{"images/", "notes/", "screenshots/"}, explicitKeyPrefixes([]string{
		"images/1.jpg", "images/2.jpg", "notes/a.png", "screenshots/3.webp",
	}))
	assert.Equal(t, []string{""}, explicitKeyPrefixes([]string{"top-level"}))
	assert.Empty(t, explicitKeyPrefixes(nil))
}

func TestConfigDefaults(t *testing.T) {
	cfg := Config{
		Endpoint:  "localhost:9000",
		AccessKey: "rustfsadmin",
		SecretKey: "rustfsadmin",
		Bucket:    "test-bucket",
		UseSSL:    false,
	}
	assert.Equal(t, "localhost:9000", cfg.Endpoint)
	assert.Equal(t, "test-bucket", cfg.Bucket)
	assert.False(t, cfg.UseSSL)
}

func TestNew_InvalidEndpoint(t *testing.T) {
	// s3 client accepts any endpoint string — connection failure happens at
	// BucketExists, not at construction. We verify that a blank endpoint
	// returns an error from the S3 SDK itself.
	ctx := context.Background()
	_, err := minio.New("", &minio.Options{})
	assert.Error(t, err, "blank endpoint should fail")

	// When called through our New, it propagates.
	_, sErr := New(ctx, Config{Endpoint: ""}, nil)
	assert.Error(t, sErr)
}

func TestNew_ConnectionRefused(t *testing.T) {
	// Port 19999 is almost certainly not listening.
	ctx := context.Background()
	_, err := New(ctx, Config{
		Endpoint:  "127.0.0.1:19999",
		AccessKey: "a",
		SecretKey: "b",
		Bucket:    "bucket",
		UseSSL:    false,
	}, nil)
	// We expect an error because BucketExists will fail.
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "storage:"), "should wrap with storage: prefix")
}
