package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeepOwnedLinkKeys_ForeignIDDoesNotAppear(t *testing.T) {
	candidates := map[string][]int64{
		"screenshots/99.jpg": {99},
	}
	owned := map[int64]struct{}{12: {}}
	seen := map[string]struct{}{}
	require.NoError(t, keepOwnedLinkKeys(candidates, owned, func(key string) error {
		seen[key] = struct{}{}
		return nil
	}))
	assert.Empty(t, seen)
}

func TestKeepOwnedLinkKeys_TwoHitsKeepOwnedID(t *testing.T) {
	candidates := map[string][]int64{}
	require.NoError(t, recordLinkCandidateURLs(`/api/files/screenshots/12.jpg`, candidates))
	require.NoError(t, recordLinkCandidateURLs(`https://evil.example/x /api/files/screenshots/12.jpg`, candidates))
	owned := map[int64]struct{}{12: {}}
	seen := map[string]struct{}{}
	require.NoError(t, keepOwnedLinkKeys(candidates, owned, func(key string) error {
		seen[key] = struct{}{}
		return nil
	}))
	assert.Equal(t, map[string]struct{}{"screenshots/12.jpg": {}}, seen)
}
