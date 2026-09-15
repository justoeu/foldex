package screenshot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3776Charter_CloseAndAcquireBrowser(t *testing.T) {
	t.Parallel()

	t.Run("nil Close is a no-op", func(t *testing.T) {
		t.Parallel()
		var p *Pool
		p.Close()
	})

	t.Run("idle Close is idempotent and then acquireBrowser is closed", func(t *testing.T) {
		t.Parallel()
		p := NewPool()
		p.Close()
		p.Close()
		_, _, err := p.acquireBrowser(context.Background())
		require.ErrorIs(t, err, errPoolClosed)
	})

	t.Run("Capture after Close reports the pool closed", func(t *testing.T) {
		t.Parallel()
		p := NewPool()
		p.Close()
		_, err := p.Capture(context.Background(), "https://example.com")
		require.ErrorIs(t, err, errPoolClosed)
	})

	t.Run("invalid URL is refused before a browser is acquired", func(t *testing.T) {
		t.Parallel()
		p := NewPool()
		t.Cleanup(p.Close)
		_, err := p.Capture(context.Background(), "file:///etc/passwd")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid http(s) target")
	})
}
