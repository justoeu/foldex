package linkimage

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryUploader struct {
	objects            map[string][]byte
	deleted            []string
	deleteContextError error
	err                error
}

func (u *memoryUploader) Upload(_ context.Context, key string, data []byte, _ string) error {
	if u.err != nil {
		return u.err
	}
	if u.objects == nil {
		u.objects = make(map[string][]byte)
	}
	u.objects[key] = data
	return nil
}

func (u *memoryUploader) DeleteObject(ctx context.Context, key string) error {
	u.deleted = append(u.deleted, key)
	u.deleteContextError = ctx.Err()
	delete(u.objects, key)
	return nil
}

func TestStoreUsesOperationOwnedKey(t *testing.T) {
	uploader := &memoryUploader{}
	first, err := Store(context.Background(), uploader, "screenshots", 42, "jpg", []byte("one"), "image/jpeg")
	require.NoError(t, err)
	second, err := Store(context.Background(), uploader, "screenshots", 42, "jpg", []byte("two"), "image/jpeg")
	require.NoError(t, err)
	assert.NotEqual(t, first.Key, second.Key)
	assert.Regexp(t, `^screenshots/42\.[0-9a-f-]{36}\.jpg$`, first.Key)
	assert.Equal(t, "/api/files/"+first.Key, first.URL)
}

func TestStoreCleansOperationKeyAfterUploadError(t *testing.T) {
	uploader := &memoryUploader{err: errors.New("partial upload")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Store(ctx, uploader, "screenshots", 42, "jpg", []byte("one"), "image/jpeg")
	require.Error(t, err)
	require.Len(t, uploader.deleted, 1)
	assert.Regexp(t, `^screenshots/42\.[0-9a-f-]{36}\.jpg$`, uploader.deleted[0])
	assert.NoError(t, uploader.deleteContextError)
}

func TestLocalKey(t *testing.T) {
	key, ok := LocalKey("/api/files/screenshots/42.version.jpg")
	assert.True(t, ok)
	assert.Equal(t, "screenshots/42.version.jpg", key)
	for _, value := range []string{"https://example.com/api/files/screenshots/42.jpg", "/api/files/notes/x.jpg", "/api/files/screenshots/../x"} {
		_, ok := LocalKey(value)
		assert.False(t, ok, value)
	}
}
