package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type openObjectBucket struct {
	StorageBucket
	data []byte
}

func (b openObjectBucket) OpenObject(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(b.data)), nil
}

type restoreStageBucket struct {
	StorageBucket
	deleteErr   error
	existingErr error
	deleteCalls int
}

func (b *restoreStageBucket) DeleteObjects(context.Context, []string) error {
	b.deleteCalls++
	return b.deleteErr
}

func (b *restoreStageBucket) ExistingObjects(context.Context, []string) (map[string]bool, error) {
	return nil, b.existingErr
}

func TestBoundedWriterRejectsMaxPlusOneWithoutPartialWrite(t *testing.T) {
	var dst strings.Builder
	w := &boundedWriter{w: &dst, max: 5}
	n, err := w.Write([]byte("1234"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)

	n, err = w.Write([]byte("56"))
	require.Error(t, err)
	assert.Zero(t, n)
	assert.Equal(t, "1234", dst.String())
}

func TestStreamObjectRejectsSizeChangedAfterListing(t *testing.T) {
	service := &Service{storage: openObjectBucket{data: []byte("changed-size")}}
	var output bytes.Buffer
	zw := zip.NewWriter(&output)
	err := service.streamObjectIntoZip(context.Background(), zw, "files/images/1.jpg", "images/1.jpg", 4, map[string]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "changed after listing")
}

func TestRunRestoreObjectTasksCancelsSiblingsOnFailure(t *testing.T) {
	sentinel := errors.New("stop uploads")
	var started, exited atomic.Int64
	tasks := make([]restoreObjectTask, 32)
	tasks[0] = func(context.Context) (restoreObjectResult, error) {
		return restoreObjectResult{}, sentinel
	}
	for i := 1; i < len(tasks); i++ {
		tasks[i] = func(ctx context.Context) (restoreObjectResult, error) {
			started.Add(1)
			<-ctx.Done()
			exited.Add(1)
			return restoreObjectResult{}, ctx.Err()
		}
	}

	_, err := runRestoreObjectTasks(context.Background(), tasks)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, started.Load(), exited.Load(), "all admitted sibling operations must observe cancellation before return")
	assert.LessOrEqual(t, started.Load(), int64(restoreObjectConcurrency-1), "failure admitted work beyond the fixed pool")
}

func TestApplyFilesPlansBeforeWipeAndPropagatesStageErrors(t *testing.T) {
	t.Run("invalid_plan_precedes_wipe", func(t *testing.T) {
		bucket := &restoreStageBucket{}
		service := &Service{storage: bucket}
		zr := zipReaderWithEntries(t, struct {
			name string
			body []byte
		}{name: "files/../evil", body: []byte("x")})

		_, err := service.applyFiles(context.Background(), 1, zr, newIDMapping(), ModeWipe, []string{"images/1.jpg"}, nil)

		require.ErrorContains(t, err, "path traversal")
		assert.Zero(t, bucket.deleteCalls)
	})

	t.Run("wipe_delete", func(t *testing.T) {
		sentinel := errors.New("delete failed")
		bucket := &restoreStageBucket{deleteErr: sentinel}
		service := &Service{storage: bucket}

		_, err := service.applyFiles(context.Background(), 1, zipReaderWithEntries(t), newIDMapping(), ModeWipe, []string{"images/1.jpg"}, nil)

		assert.ErrorIs(t, err, sentinel)
		assert.Equal(t, 1, bucket.deleteCalls)
	})

	t.Run("skip_existing_lookup", func(t *testing.T) {
		sentinel := errors.New("list failed")
		bucket := &restoreStageBucket{existingErr: sentinel}
		service := &Service{storage: bucket}
		mapping := newIDMapping()
		mapping.linkMap[1] = 2
		zr := zipReaderWithEntries(t, struct {
			name string
			body []byte
		}{name: "files/images/1.jpg", body: []byte("x")})

		_, err := service.applyFiles(context.Background(), 1, zr, mapping, ModeSkip, nil, nil)

		assert.ErrorIs(t, err, sentinel)
	})
}

func TestHasAllowedPrefix(t *testing.T) {
	cases := []struct {
		key string
		ok  bool
	}{
		{"screenshots/1.png", true},
		{"images/42.jpg", true},
		{"screenshots/", true},
		{"images/", true},
		{"other/1.png", false},
		{"", false},
		{"/screenshots/1.png", false}, // leading slash
		{"sshots/1.png", false},       // partial match
		{"screenshotsfoo.png", false}, // no trailing slash
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			assert.Equal(t, tc.ok, hasAllowedPrefix(tc.key))
		})
	}
}

func TestContentTypeFor(t *testing.T) {
	cases := []struct {
		key string
		ct  string
	}{
		{"images/1.png", "image/png"},
		{"images/1.PNG", "image/png"}, // case-insensitive ext
		{"images/1.jpg", "image/jpeg"},
		{"images/1.jpeg", "image/jpeg"},
		{"images/1.gif", "image/gif"},
		{"images/1.webp", "image/webp"},
		{"images/1.bin", "application/octet-stream"},
		{"images/no-ext", "application/octet-stream"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			assert.Equal(t, tc.ct, contentTypeFor(tc.key))
		})
	}
}
