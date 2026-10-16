package storage

import (
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

	"foldex/internal/ports"
)

// fakeS3 is the smallest S3 surface minio-go speaks for the verbs Client
// uses. A real HTTP conversation (rather than a swapped SDK fake) keeps
// these tests honest about status codes and error envelopes; the RustFS
// integration suite still owns wire compatibility with the real store.
type fakeS3 struct {
	mu           sync.Mutex
	bucketExists bool
	makeCode     string // S3 error code MakeBucket returns; "" means success
	listFails    bool
	objects      map[string]fakeS3Object
}

type fakeS3Object struct {
	body        []byte
	contentType string
	size        int64 // reported size; 0 means len(body)
}

func (o fakeS3Object) reportedSize() int64 {
	if o.size != 0 {
		return o.size
	}
	return int64(len(o.body))
}

func (s *fakeS3) handler(bucket string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		rest := strings.TrimPrefix(r.URL.Path, "/"+bucket)
		if rest == "/" { // bucket-level verbs arrive with a trailing slash
			rest = ""
		}
		key := strings.TrimPrefix(rest, "/")

		switch {
		case rest == "" && r.Method == http.MethodHead:
			if !s.bucketExists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		case rest == "" && r.Method == http.MethodPut:
			if s.makeCode != "" {
				writeS3Error(w, http.StatusForbidden, s.makeCode)
				return
			}
			s.bucketExists = true
			w.WriteHeader(http.StatusOK)
		case rest == "" && r.Method == http.MethodGet:
			if s.listFails {
				// 4xx, not 5xx: minio-go retries InternalError with backoff,
				// which would make this test pay ~4s per call for nothing.
				writeS3Error(w, http.StatusBadRequest, "InvalidArgument")
				return
			}
			s.writeList(w, bucket, r.URL.Query().Get("prefix"))
		case rest == "" && r.Method == http.MethodPost: // multi-delete
			body, _ := io.ReadAll(r.Body)
			var deleted []string
			for k := range s.objects {
				if strings.Contains(string(body), "<Key>"+k+"</Key>") {
					deleted = append(deleted, k)
					delete(s.objects, k)
				}
			}
			sort.Strings(deleted)
			var b strings.Builder
			b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
			for _, k := range deleted {
				fmt.Fprintf(&b, "<Deleted><Key>%s</Key></Deleted>", k)
			}
			b.WriteString("</DeleteResult>")
			_, _ = io.WriteString(w, b.String())
		case r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			s.objects[key] = fakeS3Object{body: body, contentType: r.Header.Get("Content-Type")}
			w.Header().Set("ETag", `"fake-etag"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodHead:
			obj, ok := s.objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", obj.contentType)
			w.Header().Set("Content-Length", strconv.FormatInt(obj.reportedSize(), 10))
			w.Header().Set("ETag", `"fake-etag"`)
			w.Header().Set("Last-Modified", "Mon, 02 Jan 2024 00:00:00 GMT")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet:
			obj, ok := s.objects[key]
			if !ok {
				writeS3Error(w, http.StatusNotFound, "NoSuchKey")
				return
			}
			w.Header().Set("Content-Type", obj.contentType)
			w.Header().Set("Content-Length", strconv.Itoa(len(obj.body)))
			w.Header().Set("ETag", `"fake-etag"`)
			w.Header().Set("Last-Modified", "Mon, 02 Jan 2024 00:00:00 GMT")
			w.Header().Set("Accept-Ranges", "bytes")
			_, _ = w.Write(obj.body)
		case r.Method == http.MethodDelete:
			delete(s.objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
}

func (s *fakeS3) writeList(w http.ResponseWriter, bucket, prefix string) {
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>%s</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`, bucket, prefix, len(keys))
	for _, k := range keys {
		fmt.Fprintf(&b, "<Contents><Key>%s</Key><LastModified>2024-01-02T00:00:00.000Z</LastModified><ETag>\"fake-etag\"</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>", k, s.objects[k].reportedSize())
	}
	b.WriteString("</ListBucketResult>")
	_, _ = io.WriteString(w, b.String())
}

func writeS3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>fake</Message><Resource>/b</Resource><RequestId>1</RequestId></Error>`, code)
}

func fakeS3Client(t *testing.T, s *fakeS3) *Client {
	t.Helper()
	srv := httptest.NewServer(s.handler("test-bucket"))
	t.Cleanup(srv.Close)
	cli, err := newClient(Config{
		Endpoint: strings.TrimPrefix(srv.URL, "http://"),
		// Region skips minio-go's location lookup against the fake.
		AccessKey: "ak", SecretKey: "sk", Bucket: "test-bucket", Region: "us-east-1",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	return cli
}

func seededFakeS3() *fakeS3 {
	return &fakeS3{bucketExists: true, objects: map[string]fakeS3Object{
		"images/1.png":  {body: []byte("png-bytes"), contentType: "image/png"},
		"images/2.png":  {body: []byte("second"), contentType: "image/png"},
		"notes/big.bin": {body: []byte("x"), contentType: "application/octet-stream", size: MaxServeObjectBytes + 1},
	}}
}

func TestClient_ObjectRoundTripThroughFakeS3(t *testing.T) {
	ctx := context.Background()
	s := seededFakeS3()
	cli := fakeS3Client(t, s)

	require.NoError(t, cli.Ping(ctx))

	got, ct, err := cli.GetObject(ctx, "images/1.png")
	require.NoError(t, err)
	assert.Equal(t, []byte("png-bytes"), got)
	assert.Equal(t, "image/png", ct)

	_, _, err = cli.GetObject(ctx, "images/missing.png")
	require.Error(t, err)
	assert.ErrorIs(t, err, ports.ErrObjectNotFound,
		"a real NoSuchKey maps to the port sentinel; an unreachable store must not")

	_, _, err = cli.GetObject(ctx, "notes/big.bin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObjectTooLarge, "the buffered serve path refuses on Stat size alone")

	rc, err := cli.OpenObject(ctx, "images/2.png")
	require.NoError(t, err)
	streamed, err := io.ReadAll(rc)
	_ = rc.Close()
	require.NoError(t, err)
	assert.Equal(t, []byte("second"), streamed)

	require.NoError(t, cli.Upload(ctx, "images/3.png", []byte("third"), "image/png"))
	require.NoError(t, cli.PutObjectStream(ctx, "images/4.png", bytes.NewReader([]byte("fourth")), 6, "image/png"))

	found, err := cli.ExistingObjects(ctx, []string{"images/1.png", "images/3.png", "images/nope.png"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"images/1.png": true, "images/3.png": true}, found)
}

func TestClient_WalkAndListThroughFakeS3(t *testing.T) {
	ctx := context.Background()
	cli := fakeS3Client(t, seededFakeS3())

	items, err := cli.collectListing(ctx)
	require.NoError(t, err, "the real ListObjects arm of eachObject must drain the SDK channel")
	require.Len(t, items, 3)

	var visited []string
	err = cli.WalkObjects(ctx, "images/", func(info ObjectInfo) error {
		visited = append(visited, info.Key)
		assert.Equal(t, "fake-etag", strings.Trim(info.ETag, `"`))
		assert.False(t, info.LastModified.IsZero())
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"images/1.png", "images/2.png"}, visited)

	stop := errors.New("stop the walk")
	visited = nil
	err = cli.WalkObjects(ctx, "images/", func(info ObjectInfo) error {
		visited = append(visited, info.Key)
		return stop
	})
	assert.ErrorIs(t, err, stop)
	assert.Len(t, visited, 1, "a visitor error stops the walk immediately")
}

func TestClient_DeleteThroughFakeS3(t *testing.T) {
	ctx := context.Background()
	s := seededFakeS3()
	cli := fakeS3Client(t, s)

	require.NoError(t, cli.DeleteObjects(ctx, nil), "an empty delete set is a no-op")
	require.NoError(t, cli.DeleteObject(ctx, "images/1.png"))
	require.NoError(t, cli.DeleteObjects(ctx, []string{"images/2.png", "images/2.png"}))

	s.mu.Lock()
	defer s.mu.Unlock()
	assert.NotContains(t, s.objects, "images/1.png")
	assert.NotContains(t, s.objects, "images/2.png")
	assert.Contains(t, s.objects, "notes/big.bin")
}

func TestClient_ListFailureThroughFakeS3(t *testing.T) {
	cli := fakeS3Client(t, &fakeS3{bucketExists: true, listFails: true, objects: map[string]fakeS3Object{}})

	_, err := cli.collectListing(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage: list objects")

	_, err = cli.ExistingObjects(context.Background(), []string{"images/1.png"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list existing objects")

	err = cli.WalkObjects(context.Background(), "", func(ObjectInfo) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list objects under")
}

func TestEnsureBucket_CreateToleratedAndRefusedCodes(t *testing.T) {
	ctx := context.Background()

	// Bucket already there: no create is issued.
	require.NoError(t, fakeS3Client(t, seededFakeS3()).ensureBucket(ctx))

	// Missing bucket, create succeeds.
	s := &fakeS3{objects: map[string]fakeS3Object{}}
	require.NoError(t, fakeS3Client(t, s).ensureBucket(ctx))
	assert.True(t, s.bucketExists)

	// Create refused with a tolerated code (bucket exists but List is denied).
	tolerated := &fakeS3{makeCode: "BucketAlreadyOwnedByYou", objects: map[string]fakeS3Object{}}
	require.NoError(t, fakeS3Client(t, tolerated).ensureBucket(ctx))

	// Any other code is a real provisioning failure.
	refused := &fakeS3{makeCode: "InvalidBucketName", objects: map[string]fakeS3Object{}}
	err := fakeS3Client(t, refused).ensureBucket(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "code=InvalidBucketName")
}

func TestNewReadOnly_RefusesToInventTheBucket(t *testing.T) {
	ctx := context.Background()

	_, err := NewReadOnly(ctx, Config{Endpoint: ""}, nil)
	require.Error(t, err, "a malformed endpoint fails before any probe")

	s := &fakeS3{objects: map[string]fakeS3Object{}}
	srv := httptest.NewServer(s.handler("ghost"))
	t.Cleanup(srv.Close)
	cfg := Config{Endpoint: strings.TrimPrefix(srv.URL, "http://"), AccessKey: "ak", SecretKey: "sk", Bucket: "ghost", Region: "us-east-1"}
	_, err = NewReadOnly(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist",
		"the mirror source must fail on a typo'd bucket instead of copying nothing forever")
	assert.False(t, s.bucketExists, "read-only construction must never create the bucket")

	s.bucketExists = true
	cli, err := NewReadOnly(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.NotNil(t, cli)
}

func TestClient_DeadEndpointFailsFastAndWrapped(t *testing.T) {
	// newClient, not NewDeferred: no bucket probe (and its SDK retries)
	// stands between this test and the four calls it wants to fail.
	cli, err := newClient(Config{
		Endpoint: "127.0.0.1:1", AccessKey: "a", SecretKey: "b", Bucket: "bucket", Region: "us-east-1",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	assert.Error(t, cli.Upload(ctx, "k", []byte("x"), "text/plain"))
	assert.Error(t, cli.PutObjectStream(ctx, "k", bytes.NewReader([]byte("x")), 1, "text/plain"))
	_, _, getErr := cli.GetObject(ctx, "k")
	require.Error(t, getErr)
	assert.NotErrorIs(t, getErr, ports.ErrObjectNotFound,
		"an unreachable store is not 'key missing' — the proxy would regenerate previews on it")
	assert.Error(t, cli.DeleteObject(ctx, "k"))
}

func TestCollectListing_ErrorItemAndCancelledContext(t *testing.T) {
	boom := errors.New("list exploded")
	c := &Client{listFn: func(context.Context) <-chan listedObject {
		ch := make(chan listedObject, 2)
		ch <- listedObject{Key: "a", Size: 1}
		ch <- listedObject{Err: boom}
		close(ch)
		return ch
	}}
	_, err := c.collectListing(context.Background())
	assert.ErrorIs(t, err, boom)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c = &Client{listFn: func(context.Context) <-chan listedObject {
		ch := make(chan listedObject)
		close(ch)
		return ch
	}}
	_, err = c.collectListing(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestClient_LogHelpersTolerateNilEverything(t *testing.T) {
	var nilClient *Client
	assert.NotPanics(t, func() {
		nilClient.logWarn("x")
		nilClient.logInfo("x")
		(&Client{}).logWarn("x")
		(&Client{}).logInfo("x")
	})

	var buf bytes.Buffer
	cli := &Client{logger: slog.New(slog.NewTextHandler(&buf, nil))}
	cli.logInfo("hello info")
	assert.Contains(t, buf.String(), "hello info")
}
