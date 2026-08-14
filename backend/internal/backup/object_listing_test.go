package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type generatedObjectBucket struct {
	StorageBucket
	count   int
	visited int
}

func (b *generatedObjectBucket) WalkObjects(_ context.Context, prefix string, visit func(ObjectInfo) error) error {
	if prefix != "images/" {
		return nil
	}
	for i := 0; i < b.count; i++ {
		b.visited++
		if err := visit(ObjectInfo{Key: fmt.Sprintf("images/%d.jpg", i), Size: 1}); err != nil {
			return err
		}
	}
	return nil
}

func TestListOwnedObjectsStopsAtArchiveEntryCap(t *testing.T) {
	bucket := &generatedObjectBucket{count: maxBackupFileEntries + 1}
	owned := make(map[string]struct{}, bucket.count)
	for i := 0; i < bucket.count; i++ {
		owned[fmt.Sprintf("images/%d.jpg", i)] = struct{}{}
	}

	listing, err := listOwnedObjects(context.Background(), bucket, owned, 1)
	require.Error(t, err)
	assert.Empty(t, listing.objects)
	assert.Contains(t, err.Error(), "file entries")
	assert.Equal(t, maxBackupFileEntries+1, bucket.visited, "the callback must stop at max+1 rather than materializing an unbounded listing")
}

func TestListOwnedObjectsRejectsPerFileAndExpandedBudgets(t *testing.T) {
	t.Run("per file", func(t *testing.T) {
		bucket := &generatedObjectBucket{count: 1}
		owned := map[string]struct{}{"images/0.jpg": {}}
		// Override the generated size through a focused visitor wrapper.
		wrapped := objectSizeBucket{generatedObjectBucket: bucket, size: maxArchiveFileBytes + 1}
		_, err := listOwnedObjects(context.Background(), wrapped, owned, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max")
	})

	t.Run("expanded total", func(t *testing.T) {
		bucket := &generatedObjectBucket{count: 1}
		owned := map[string]struct{}{"images/0.jpg": {}}
		wrapped := objectSizeBucket{generatedObjectBucket: bucket, size: 1}
		_, err := listOwnedObjects(context.Background(), wrapped, owned, maxArchiveExpandedBytes-maxManifestJSONBytes)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expanded bytes")
	})
}

func TestChecksumManifestBudgetCoversIndentedManifest(t *testing.T) {
	checksums := make(map[string]string, 20_001)
	estimated := int64(0)
	for i := 0; i < 20_000; i++ {
		name := fmt.Sprintf("files/images/%d.jpg", i)
		checksums[name] = "sha256:" + fmt.Sprintf("%064d", i)
		estimated += checksumManifestBytes(name)
	}
	checksums["database.json"] = "sha256:" + fmt.Sprintf("%064d", 0)
	estimated += checksumManifestBytes("database.json")

	manifest, err := json.MarshalIndent(Manifest{Checksums: checksums}, "", "  ")
	require.NoError(t, err)
	assert.LessOrEqual(t, int64(len(manifest)), estimated+manifestFixedHeadroom,
		"the admission estimate must include map indentation before response headers are sent")
}

type objectSizeBucket struct {
	*generatedObjectBucket
	size int64
}

func (b objectSizeBucket) WalkObjects(ctx context.Context, prefix string, visit func(ObjectInfo) error) error {
	return b.generatedObjectBucket.WalkObjects(ctx, prefix, func(object ObjectInfo) error {
		object.Size = b.size
		return visit(object)
	})
}
