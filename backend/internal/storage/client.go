package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"foldex/internal/ports"
)

// Client wraps an S3-compatible client (RustFS via minio-go) and exposes a minimal interface for foldex.
type Client struct {
	mc     *minio.Client
	bucket string
	logger *slog.Logger
}

// Config holds S3-compatible object-store connection parameters (RustFS).
type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
	// Region is required by real AWS S3 outside us-east-1; RustFS and MinIO
	// ignore it, so the zero value keeps every existing caller unchanged.
	Region string
}

// New creates a Client, ensures the bucket exists, and returns it.
func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: create s3 client: %w", err)
	}

	exists, err := mc.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("storage: check bucket %q at %s: %w", cfg.Bucket, cfg.Endpoint, err)
	}
	if !exists {
		mkErr := mc.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region})
		if mkErr != nil {
			errCode := minio.ToErrorResponse(mkErr).Code
			// Tolerate "already exists" and permission-denied codes — the bucket
			// may exist but the credentials may lack s3:ListAllMyBuckets.
			toleratedCodes := map[string]bool{
				"BucketAlreadyOwnedByYou": true,
				"BucketAlreadyExists":     true,
				"AccessDenied":            true,
				"NoSuchBucket":            true, // some S3 servers on create
			}
			if !toleratedCodes[errCode] {
				return nil, fmt.Errorf("storage: make bucket %q at %s (code=%s): %w", cfg.Bucket, cfg.Endpoint, errCode, mkErr)
			}
			logger.Warn("storage: bucket create returned tolerated error, assuming bucket exists", "bucket", cfg.Bucket, "s3_error_code", errCode)
		} else {
			logger.Info("storage: created bucket", "bucket", cfg.Bucket)
		}
	}

	return &Client{mc: mc, bucket: cfg.Bucket, logger: logger}, nil
}

// NewReadOnly builds a client for a bucket this process only READS — the
// mirror's source. It never creates the bucket: with New, a typo'd
// RUSTFS_BUCKET would be silently created empty and the mirror would succeed
// forever copying nothing — the exact silent non-backup ADR-43 exists to
// kill. A missing bucket here is a configuration error and fails the boot.
func NewReadOnly(ctx context.Context, cfg Config, logger *slog.Logger) (*Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: create s3 client: %w", err)
	}
	exists, err := mc.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("storage: check bucket %q at %s: %w", cfg.Bucket, cfg.Endpoint, err)
	}
	if !exists {
		return nil, fmt.Errorf("storage: bucket %q does not exist at %s — refusing to read from a bucket that would have to be created (check the name)", cfg.Bucket, cfg.Endpoint)
	}
	return &Client{mc: mc, bucket: cfg.Bucket, logger: logger}, nil
}

// Ping reports whether the bucket endpoint answers. The error may name the
// host — callers that surface status to a client must not forward it.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.mc.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("storage: ping: %w", err)
	}
	return nil
}

// Upload stores data at key inside the configured bucket.
// contentType should be a MIME type like "image/png".
func (c *Client) Upload(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("storage: upload %q: %w", key, err)
	}
	c.logger.Info("storage: uploaded object")
	return nil
}

// MaxServeObjectBytes is the hard ceiling for buffered GetObject reads used by
// the image proxy. Upload paths already cap images at 5 MiB; 16 MiB leaves
// headroom for legacy objects while blocking multi-GB backup keys from being
// fully buffered. Large objects must use OpenObject (streaming).
const MaxServeObjectBytes int64 = 16 << 20

// ErrObjectTooLarge is returned when Stat reports a size above MaxServeObjectBytes
// or when a stream exceeds that ceiling mid-read.
var ErrObjectTooLarge = ports.ErrObjectTooLarge

// checkServeSize rejects objects that would blow the buffered-serve budget.
func checkServeSize(size int64) error {
	if size < 0 || size > MaxServeObjectBytes {
		return fmt.Errorf("%w: size=%d max=%d", ErrObjectTooLarge, size, MaxServeObjectBytes)
	}
	return nil
}

// GetObject returns the raw bytes stored at key, capped at MaxServeObjectBytes.
func (c *Client) GetObject(ctx context.Context, key string) ([]byte, string, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("storage: get object %q: %w", key, err)
	}
	defer obj.Close()

	info, err := obj.Stat()
	if err != nil {
		// A "not found" comes back as an error on Stat, not on GetObject.
		//
		// Translated to the port's own sentinel rather than left as an S3
		// error: callers decide real things on "this key holds nothing" — the
		// file proxy regenerates a preview from it — and they must not be able
		// to reach that branch on a store that is merely unreachable. The
		// check is on the CODE, not the message, because minio returns the
		// same shape for a timeout with different text.
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, "", fmt.Errorf("storage: stat object %q: %w", key, ports.ErrObjectNotFound)
		}
		return nil, "", fmt.Errorf("storage: stat object %q: %w", key, err)
	}
	if err := checkServeSize(info.Size); err != nil {
		return nil, "", fmt.Errorf("storage: get object %q: %w", key, err)
	}

	// LimitReader caps mid-stream growth if Stat was stale/under-reported.
	limited := io.LimitReader(obj, MaxServeObjectBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", fmt.Errorf("storage: read object %q: %w", key, err)
	}
	if int64(len(buf)) > MaxServeObjectBytes {
		return nil, "", fmt.Errorf("storage: get object %q: %w", key, ErrObjectTooLarge)
	}
	return buf, info.ContentType, nil
}

// readAll reads all bytes from an io.Reader when a pre-allocated read fails.
// Retained for unit tests that exercise the drain helper shape.
func readAll(obj io.Reader, size int64) ([]byte, error) {
	if size > MaxServeObjectBytes {
		size = MaxServeObjectBytes
	}
	if size < 0 {
		size = 0
	}
	limited := io.LimitReader(obj, MaxServeObjectBytes+1)
	buf := bytes.NewBuffer(make([]byte, 0, size))
	if _, err := buf.ReadFrom(limited); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > MaxServeObjectBytes {
		return nil, ErrObjectTooLarge
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
// bucket without coupling to the SDK's full ObjectInfo type. ETag and
// LastModified come free with every ListObjects page; the mirror job diffs by
// LastModified watermark — never by ETag, whose multipart form depends on the
// uploader's part size, not the content (SDD-OPS-BACKUP §11.6).
type ObjectInfo struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
}

// WalkObjects visits objects under prefix as the SDK yields its paginated
// stream. The callback is synchronous: returning an error stops the walk
// without retaining the remaining bucket metadata in memory.
func (c *Client) WalkObjects(ctx context.Context, prefix string, visit func(ObjectInfo) error) error {
	walkCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := c.mc.ListObjects(walkCtx, c.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true})
	for obj := range ch {
		if obj.Err != nil {
			return fmt.Errorf("storage: list objects under %q: %w", prefix, obj.Err)
		}
		if err := visit(ObjectInfo{Key: obj.Key, Size: obj.Size, ETag: obj.ETag, LastModified: obj.LastModified}); err != nil {
			return err
		}
	}
	return ctx.Err()
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
	c.logger.Info("storage: uploaded object (stream)")
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

// ExistingObjects resolves an explicit key set with one paginated LIST per
// top-level namespace instead of one HEAD request per key. Only exact requested
// keys are returned; listing a shared flat namespace never grants ownership.
func (c *Client) ExistingObjects(ctx context.Context, keys []string) (map[string]bool, error) {
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	found := make(map[string]bool, len(wanted))
	for _, prefix := range explicitKeyPrefixes(keys) {
		objects := c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true})
		for object := range objects {
			if object.Err != nil {
				return nil, fmt.Errorf("storage: list existing objects under %q: %w", prefix, object.Err)
			}
			if _, ok := wanted[object.Key]; ok {
				found[object.Key] = true
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return found, nil
}

func explicitKeyPrefixes(keys []string) []string {
	unique := make(map[string]struct{})
	for _, key := range keys {
		prefix := ""
		if slash := strings.IndexByte(key, '/'); slash >= 0 {
			prefix = key[:slash+1]
		}
		unique[prefix] = struct{}{}
	}
	prefixes := make([]string, 0, len(unique))
	for prefix := range unique {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	return prefixes
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

// DeleteObjects removes an explicit key set through S3 multi-delete batches.
// Missing keys are successful, matching DeleteObject's idempotent contract.
func (c *Client) DeleteObjects(ctx context.Context, keys []string) error {
	unique := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		unique[key] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for key := range unique {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	if len(ordered) == 0 {
		return nil
	}
	objects := make(chan minio.ObjectInfo)
	go func() {
		defer close(objects)
		for _, key := range ordered {
			select {
			case objects <- minio.ObjectInfo{Key: key}:
			case <-ctx.Done():
				return
			}
		}
	}()

	var firstErr error
	for result := range c.mc.RemoveObjects(ctx, c.bucket, objects, minio.RemoveObjectsOptions{}) {
		if result.Err == nil || minio.ToErrorResponse(result.Err).Code == "NoSuchKey" {
			continue
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("storage: delete %q: %w", result.ObjectName, result.Err)
		}
	}
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	return firstErr
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
