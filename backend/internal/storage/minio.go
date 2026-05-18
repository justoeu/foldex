package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Client wraps a MinIO client and exposes a minimal interface for foldex.
type Client struct {
	mc     *minio.Client
	bucket string
	logger *slog.Logger
}

// Config holds the MinIO connection parameters.
type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// New creates a Client, ensures the bucket exists, and returns it.
func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: create minio client: %w", err)
	}

	exists, err := mc.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("storage: check bucket %q at %s: %w", cfg.Bucket, cfg.Endpoint, err)
	}
	if !exists {
		mkErr := mc.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{})
		if mkErr != nil {
			errCode := minio.ToErrorResponse(mkErr).Code
			// Tolerate "already exists" and permission-denied codes — the bucket
			// may exist but the credentials may lack s3:ListAllMyBuckets.
			toleratedCodes := map[string]bool{
				"BucketAlreadyOwnedByYou": true,
				"BucketAlreadyExists":     true,
				"AccessDenied":            true,
				"NoSuchBucket":            true, // some MinIO versions on create
			}
			if !toleratedCodes[errCode] {
				return nil, fmt.Errorf("storage: make bucket %q at %s (code=%s): %w", cfg.Bucket, cfg.Endpoint, errCode, mkErr)
			}
			logger.Warn("storage: bucket create returned tolerated error, assuming bucket exists", "bucket", cfg.Bucket, "code", errCode)
		} else {
			logger.Info("storage: created bucket", "bucket", cfg.Bucket)
		}
	}

	return &Client{mc: mc, bucket: cfg.Bucket, logger: logger}, nil
}

// Upload stores data at key inside the configured bucket.
// contentType should be a MIME type like "image/png".
func (c *Client) Upload(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("storage: upload %q: %w", key, err)
	}
	c.logger.Info("storage: uploaded object", "key", key, "bytes", len(data))
	return nil
}

// GetObject returns the raw bytes stored at key.
func (c *Client) GetObject(ctx context.Context, key string) ([]byte, string, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("storage: get object %q: %w", key, err)
	}
	defer obj.Close()

	info, err := obj.Stat()
	if err != nil {
		// A MinIO "not found" comes back as an error on Stat, not on GetObject.
		return nil, "", fmt.Errorf("storage: stat object %q: %w", key, err)
	}

	buf := make([]byte, info.Size)
	if _, err := io.ReadFull(obj, buf); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		// Stat may report a stale size if the object was concurrently rewritten.
		// Fall back to draining whatever the reader still has so we return the
		// real payload instead of partial junk.
		n, rerr := readAll(obj, info.Size)
		if rerr != nil {
			return nil, "", fmt.Errorf("storage: read object %q: %w", key, rerr)
		}
		return n, info.ContentType, nil
	}
	return buf, info.ContentType, nil
}

// readAll reads all bytes from an io.Reader when a pre-allocated read fails.
func readAll(obj io.Reader, size int64) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, size))
	if _, err := buf.ReadFrom(obj); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Stats walks every object in the bucket and aggregates count + total bytes.
// Cheap on personal-scale buckets (≤ a few thousand objects); for large
// installs the API call is paginated by the SDK.
type Stats struct {
	Objects    int64 `json:"objects"`
	TotalBytes int64 `json:"total_bytes"`
}

func (c *Client) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	ch := c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Recursive: true})
	for obj := range ch {
		if obj.Err != nil {
			return s, fmt.Errorf("storage: list objects: %w", obj.Err)
		}
		s.Objects++
		s.TotalBytes += obj.Size
	}
	return s, nil
}

// ObjectInfo is the minimal metadata the backup module needs to enumerate the
// bucket without coupling to minio-go's full ObjectInfo type.
type ObjectInfo struct {
	Key  string
	Size int64
}

// ListObjects returns every object under `prefix` (recursive). Use empty
// string to list the entire bucket.
func (c *Client) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	out := make([]ObjectInfo, 0, 32)
	ch := c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true})
	for obj := range ch {
		if obj.Err != nil {
			return nil, fmt.Errorf("storage: list objects under %q: %w", prefix, obj.Err)
		}
		out = append(out, ObjectInfo{Key: obj.Key, Size: obj.Size})
	}
	return out, nil
}

// OpenObject streams a single object. Caller MUST close the returned reader.
// Unlike GetObject which buffers the whole payload in memory, this is the
// path for large objects (e.g. screenshots that the backup module pipes
// straight into a zip entry).
func (c *Client) OpenObject(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("storage: open object %q: %w", key, err)
	}
	// Probe stat so callers see a "not found" error here, not mid-stream.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, fmt.Errorf("storage: stat object %q: %w", key, err)
	}
	return obj, nil
}

// PutObjectStream uploads from a reader with a known size + content-type.
// Used by the backup restore phase where the payload comes off a zip entry.
func (c *Client) PutObjectStream(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key, r, size,
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("storage: put stream %q: %w", key, err)
	}
	c.logger.Info("storage: uploaded object (stream)", "key", key, "bytes", size)
	return nil
}

// ObjectExists returns true if `key` is present in the bucket.
func (c *Client) ObjectExists(ctx context.Context, key string) (bool, error) {
	_, err := c.mc.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	if minio.ToErrorResponse(err).Code == "NoSuchKey" {
		return false, nil
	}
	return false, fmt.Errorf("storage: stat %q: %w", key, err)
}

// DeleteObject removes a single object. A NoSuchKey response is treated as
// success — callers use this to clean up stale key variants and shouldn't
// care if the previous key was already gone.
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	if err := c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil
		}
		return fmt.Errorf("storage: delete %q: %w", key, err)
	}
	return nil
}

// DeleteObjectsPrefix removes every object under `prefix`. Used by
// restore-wipe to clear the bucket before re-uploading.
func (c *Client) DeleteObjectsPrefix(ctx context.Context, prefix string) error {
	keysCh := make(chan minio.ObjectInfo)
	go func() {
		defer close(keysCh)
		listCh := c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true})
		for obj := range listCh {
			if obj.Err != nil {
				continue
			}
			// ctx-aware send so we don't leak this goroutine if RemoveObjects
			// returns early (cancellation, partial failure) and stops draining
			// keysCh.
			select {
			case keysCh <- obj:
			case <-ctx.Done():
				return
			}
		}
	}()
	errCh := c.mc.RemoveObjects(ctx, c.bucket, keysCh, minio.RemoveObjectsOptions{})
	for e := range errCh {
		if e.Err != nil {
			return fmt.Errorf("storage: delete prefix %q (%s): %w", prefix, e.ObjectName, e.Err)
		}
	}
	return nil
}
