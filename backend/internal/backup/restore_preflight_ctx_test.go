package backup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeSnapshotNotes_StopsWhenTheContextDies(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := sanitizeSnapshotNotes(ctx, &Snapshot{Notes: []NoteRow{{BodyHTML: "<p>x</p>"}}})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestMissingLinkFileWarnings_StopsWhenTheContextDies(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	og := "/api/files/og.png"
	_, err := missingLinkFileWarnings(ctx, &Snapshot{Links: []LinkRow{{OGImageURL: &og}}}, map[string]bool{})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
