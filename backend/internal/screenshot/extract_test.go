package screenshot

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractMetadata_RejectsNonHTTPTargetBeforeBrowserLaunch(t *testing.T) {
	pool := NewPool()
	t.Setenv("CHROME_PATH", "/nonexistent/chrome-binary-xyz")
	for _, target := range []string{"file:///etc/passwd", "data:text/html,x", "about:blank", "http:///missing-host"} {
		_, err := pool.ExtractMetadata(context.Background(), target)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid http(s) target", target)
	}
}

func TestExtractMetadata_CancelledContext(t *testing.T) {
	pool := NewPool()
	pool.captureSem <- struct{}{}
	defer func() { <-pool.captureSem }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := pool.ExtractMetadata(ctx, "https://example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait for slot")
}

func TestExtractMetadata_IsolatesAndDisposesEveryContext(t *testing.T) {
	pool := NewPool()
	var configs []browserContextProxy
	var disposed []proto.BrowserBrowserContextID
	pool.createBrowserContext = func(_ context.Context, _ *rod.Browser, proxy browserContextProxy) (proto.BrowserBrowserContextID, error) {
		configs = append(configs, proxy)
		return proto.BrowserBrowserContextID(fmt.Sprintf("extract-%d", len(configs))), nil
	}
	pool.disposeBrowserContext = func(ctx context.Context, _ *rod.Browser, id proto.BrowserBrowserContextID) error {
		require.NoError(t, ctx.Err(), "cleanup must not inherit a cancelled extract context")
		_, hasDeadline := ctx.Deadline()
		assert.True(t, hasDeadline, "context disposal must be bounded")
		disposed = append(disposed, id)
		return nil
	}
	pool.extractBrowserContext = func(_ context.Context, _ *rod.Browser, _ proto.BrowserBrowserContextID, _ string) (PageMetadata, error) {
		return PageMetadata{Title: "from chromium"}, nil
	}

	browser := &rod.Browser{}
	pool.current = &pooledBrowser{browser: browser, stopped: make(chan struct{})}
	pool.generations[pool.current] = struct{}{}
	for range 2 {
		md, err := pool.ExtractMetadata(context.Background(), "http://93.184.216.34/page")
		require.NoError(t, err)
		assert.Equal(t, "from chromium", md.Title)
	}

	require.Len(t, configs, 2)
	assert.NotEqual(t, configs[0].Server, configs[1].Server, "each extract needs its own proxy")
	assert.Equal(t, []proto.BrowserBrowserContextID{"extract-1", "extract-2"}, disposed)
}

func TestExtractWithBrowser_DisposesContextAfterCancellation(t *testing.T) {
	pool := NewPool()
	ctx, cancel := context.WithCancel(context.Background())
	pool.createBrowserContext = func(_ context.Context, _ *rod.Browser, _ browserContextProxy) (proto.BrowserBrowserContextID, error) {
		return "cancelled-extract", nil
	}
	pool.extractBrowserContext = func(callCtx context.Context, _ *rod.Browser, _ proto.BrowserBrowserContextID, _ string) (PageMetadata, error) {
		cancel()
		<-callCtx.Done()
		return PageMetadata{}, callCtx.Err()
	}
	disposed := false
	pool.disposeBrowserContext = func(cleanupCtx context.Context, _ *rod.Browser, id proto.BrowserBrowserContextID) error {
		disposed = true
		assert.Equal(t, proto.BrowserBrowserContextID("cancelled-extract"), id)
		assert.NoError(t, cleanupCtx.Err(), "cancelled caller context must not cancel disposal")
		deadline, ok := cleanupCtx.Deadline()
		assert.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), pool.contextCleanupTimeout)
		return nil
	}

	_, err := pool.extractWithBrowser(ctx, &rod.Browser{}, nil, "http://93.184.216.34/page")
	require.ErrorIs(t, err, context.Canceled)
	assert.True(t, disposed)
}

func TestAbsolutize(t *testing.T) {
	base := "https://news.example.com/a/b"
	assert.Equal(t, "https://cdn.example.com/og.png", absolutize(base, "https://cdn.example.com/og.png"))
	assert.Equal(t, "https://news.example.com/img.png", absolutize(base, "/img.png"))
	assert.Equal(t, "https://news.example.com/a/rel.png", absolutize(base, "rel.png"))
	assert.Equal(t, "", absolutize(base, "  "))
	assert.Equal(t, "", absolutize(base, "javascript:alert(1)"))
	assert.Equal(t, "", absolutize(base, "data:text/html,x"))
	assert.Equal(t, "", absolutize(base, "file:///etc/passwd"))
}
