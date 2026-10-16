package backupagent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backup"
	"foldex/internal/storage"
)

// The adapters take the concrete *storage.Client, so the cheapest honest
// fake is a stub S3 endpoint: a real client over httptest, with the handful
// of S3 verbs the adapter surface exercises (PUT/GET/HEAD object, LIST,
// multi-delete). failAll flips every verb to a 403 so the error side of the
// delegation is observable without a bucket.

const stubBucket = "bucket"
const stubETag = `"deadbeef"`

var stubModified = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type stubS3 struct {
	mu       sync.Mutex
	objects  map[string][]byte
	putTypes map[string]string
	failAll  bool
}

func newStubS3(failAll bool) *stubS3 {
	return &stubS3{objects: map[string][]byte{}, putTypes: map[string]string{}, failAll: failAll}
}

func (s *stubS3) seed(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = append([]byte(nil), data...)
}

func (s *stubS3) get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objects[key]
	return b, ok
}

func (s *stubS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.failAll {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidAccessKeyId</Code><Message>stub refuses</Message></Error>`)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/"+stubBucket)
	key := strings.TrimPrefix(rest, "/")
	switch {
	case r.Method == http.MethodHead && key == "":
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && key == "" && r.URL.Query().Has("location"):
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
	case r.Method == http.MethodGet && key == "":
		s.list(w, r)
	case r.Method == http.MethodPost && key == "" && r.URL.Query().Has("delete"):
		s.multiDelete(w, r)
	case r.Method == http.MethodPut && key != "":
		body, err := readPutBody(r)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		s.objects[key] = body
		s.putTypes[key] = r.Header.Get("Content-Type")
		s.mu.Unlock()
		w.Header().Set("ETag", stubETag)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodHead && key != "":
		data, ok := s.get(key)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", stubETag)
		w.Header().Set("Last-Modified", stubModified.Format(http.TimeFormat))
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && key != "":
		data, ok := s.get(key)
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>gone</Message></Error>`)
			return
		}
		w.Header().Set("ETag", stubETag)
		w.Header().Set("Last-Modified", stubModified.Format(http.TimeFormat))
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		_, _ = w.Write(data)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

// readPutBody decodes the SigV4 streaming (aws-chunked) framing minio-go
// wraps PutObject payloads in: `<hex>;chunk-signature=…\r\n<bytes>\r\n`
// repeated, closed by a zero chunk. Plain bodies pass through.
func readPutBody(r *http.Request) ([]byte, error) {
	streaming := strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") ||
		strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING")
	if !streaming {
		return io.ReadAll(r.Body)
	}
	br := bufio.NewReader(r.Body)
	var out bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeText, _, _ := strings.Cut(strings.TrimSpace(line), ";")
		size, err := strconv.ParseInt(sizeText, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("stub: bad chunk header %q: %w", line, err)
		}
		if size == 0 {
			return out.Bytes(), nil
		}
		if _, err := io.CopyN(&out, br, size); err != nil {
			return nil, err
		}
		if _, err := br.ReadString('\n'); err != nil { // trailing CRLF
			return nil, err
		}
	}
}

func (s *stubS3) list(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	s.mu.Lock()
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	sizes := make(map[string]int, len(keys))
	for _, k := range keys {
		sizes[k] = len(s.objects[k])
	}
	s.mu.Unlock()

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	fmt.Fprintf(&b, "<Name>%s</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>", stubBucket, prefix, len(keys))
	for _, k := range keys {
		fmt.Fprintf(&b, "<Contents><Key>%s</Key><LastModified>%s</LastModified><ETag>%s</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>",
			k, stubModified.Format("2006-01-02T15:04:05.000Z"), stubETag, sizes[k])
	}
	b.WriteString("</ListBucketResult>")
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprint(w, b.String())
}

func (s *stubS3) multiDelete(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	// The Delete XML is a flat list of <Key> elements; the stub bucket's keys
	// never contain markup, so a scan is exact.
	for _, part := range strings.Split(string(body), "<Key>") {
		if i := strings.Index(part, "</Key>"); i >= 0 {
			delete(s.objects, part[:i])
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></DeleteResult>`)
}

func stubStorageClient(t *testing.T, stub *stubS3) *storage.Client {
	t.Helper()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	// NewDeferred tolerates the failing stub's ensureBucket error, which is
	// what lets one constructor serve both the happy and the error client.
	cli, err := storage.NewDeferred(context.Background(), storage.Config{
		Endpoint:  strings.TrimPrefix(srv.URL, "http://"),
		AccessKey: "ak",
		SecretKey: "sk",
		Bucket:    stubBucket,
		UseSSL:    false,
	}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NotNil(t, cli)
	return cli
}

func TestStorageUploaderDelegatesPutOpenWalkDelete(t *testing.T) {
	ctx := context.Background()
	stub := newStubS3(false)
	up := NewStorageUploader(stubStorageClient(t, stub))
	require.NotNil(t, up)

	require.NoError(t, up.PutObjectStream(ctx, "db/a.dump", strings.NewReader("alpha"), 5, "application/octet-stream"))
	require.NoError(t, up.PutObjectStream(ctx, "db/b.dump", strings.NewReader("beta!"), 5, "application/octet-stream"))
	require.NoError(t, up.PutObjectStream(ctx, "other/c.bin", strings.NewReader("x"), 1, "application/octet-stream"))
	got, ok := stub.get("db/a.dump")
	require.True(t, ok)
	assert.Equal(t, "alpha", string(got), "bytes must reach the bucket unchanged")

	rc, err := up.OpenObject(ctx, "db/b.dump")
	require.NoError(t, err)
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, "beta!", string(data))

	var walked []ObjectInfo
	require.NoError(t, up.WalkObjects(ctx, "db/", func(o ObjectInfo) error {
		walked = append(walked, o)
		return nil
	}))
	require.Len(t, walked, 2, "walk is prefix-scoped")
	assert.Equal(t, "db/a.dump", walked[0].Key)
	assert.EqualValues(t, 5, walked[0].Size)
	assert.Equal(t, "deadbeef", walked[0].ETag, "adapter must carry ETag across (minio unquotes it)")
	assert.True(t, stubModified.Equal(walked[0].LastModified), "adapter must carry LastModified across")

	require.NoError(t, up.DeleteObjects(ctx, []string{"db/a.dump"}))
	_, ok = stub.get("db/a.dump")
	assert.False(t, ok, "deleted key must be gone from the bucket")
	_, ok = stub.get("db/b.dump")
	assert.True(t, ok, "sibling keys survive a targeted delete")

	// A deleted object surfaces on Read (OpenObject deliberately skips HEAD).
	rc, err = up.OpenObject(ctx, "db/a.dump")
	require.NoError(t, err)
	_, err = io.ReadAll(rc)
	_ = rc.Close()
	assert.Error(t, err)
}

func TestStorageUploaderWalkStopsOnVisitError(t *testing.T) {
	ctx := context.Background()
	stub := newStubS3(false)
	stub.seed("p/1", []byte("1"))
	stub.seed("p/2", []byte("2"))
	up := NewStorageUploader(stubStorageClient(t, stub))

	sentinel := errors.New("visitor stops")
	visits := 0
	err := up.WalkObjects(ctx, "p/", func(ObjectInfo) error {
		visits++
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, 1, visits, "a visitor error aborts the walk")
}

func TestStorageUploaderPropagatesClientErrors(t *testing.T) {
	ctx := context.Background()
	up := NewStorageUploader(stubStorageClient(t, newStubS3(true)))

	err := up.PutObjectStream(ctx, "k", strings.NewReader("v"), 1, "text/plain")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage: put stream")

	err = up.WalkObjects(ctx, "", func(ObjectInfo) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage: list objects")

	err = up.DeleteObjects(ctx, []string{"k"})
	assert.Error(t, err)

	rc, err := up.OpenObject(ctx, "k")
	require.NoError(t, err, "OpenObject is lazy: the failure lands on Read")
	_, err = io.ReadAll(rc)
	_ = rc.Close()
	assert.Error(t, err)

	// An empty delete never touches the network, so even a refusing store
	// cannot make it fail.
	assert.NoError(t, up.DeleteObjects(ctx, nil))
}

func TestBackupSourceBucketDelegatesAllMethods(t *testing.T) {
	ctx := context.Background()
	stub := newStubS3(false)
	bucket := NewBackupSourceBucket(stubStorageClient(t, stub))
	require.NotNil(t, bucket)
	var _ backup.StorageBucket = bucket

	require.NoError(t, bucket.PutObjectStream(ctx, "screens/1.png", strings.NewReader("img1"), 4, "image/png"))
	require.NoError(t, bucket.PutObjectStream(ctx, "screens/2.png", strings.NewReader("img22"), 5, "image/png"))

	rc, err := bucket.OpenObject(ctx, "screens/1.png")
	require.NoError(t, err)
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, "img1", string(data))

	var walked []backup.ObjectInfo
	require.NoError(t, bucket.WalkObjects(ctx, "screens/", func(o backup.ObjectInfo) error {
		walked = append(walked, o)
		return nil
	}))
	assert.Equal(t, []backup.ObjectInfo{
		{Key: "screens/1.png", Size: 4},
		{Key: "screens/2.png", Size: 5},
	}, walked, "the backup bucket shape carries Key and Size only")

	existing, err := bucket.ExistingObjects(ctx, []string{"screens/1.png", "screens/missing.png"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"screens/1.png": true}, existing,
		"only keys actually present are reported")

	require.NoError(t, bucket.DeleteObjects(ctx, []string{"screens/1.png"}))
	existing, err = bucket.ExistingObjects(ctx, []string{"screens/1.png", "screens/2.png"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"screens/2.png": true}, existing)
}

func TestBackupSourceBucketWalkStopsOnVisitError(t *testing.T) {
	ctx := context.Background()
	stub := newStubS3(false)
	stub.seed("s/1", []byte("1"))
	stub.seed("s/2", []byte("2"))
	bucket := NewBackupSourceBucket(stubStorageClient(t, stub))

	sentinel := errors.New("visitor stops")
	err := bucket.WalkObjects(ctx, "s/", func(backup.ObjectInfo) error { return sentinel })
	assert.ErrorIs(t, err, sentinel)
}

func TestBackupSourceBucketPropagatesClientErrors(t *testing.T) {
	ctx := context.Background()
	bucket := NewBackupSourceBucket(stubStorageClient(t, newStubS3(true)))

	err := bucket.PutObjectStream(ctx, "k", strings.NewReader("v"), 1, "text/plain")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage: put stream")

	_, err = bucket.ExistingObjects(ctx, []string{"k"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage: list existing objects")

	err = bucket.WalkObjects(ctx, "", func(backup.ObjectInfo) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage: list objects")

	assert.Error(t, bucket.DeleteObjects(ctx, []string{"k"}))

	rc, err := bucket.OpenObject(ctx, "k")
	require.NoError(t, err)
	_, err = io.ReadAll(rc)
	_ = rc.Close()
	assert.Error(t, err)
}
