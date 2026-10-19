package screenshot

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAbsolutize_UnparseableAndRelativeShapes(t *testing.T) {
	base := "https://news.example.com/a/b/c?q=1"

	// Unparseable on either side yields nothing, never a half-built URL.
	assert.Empty(t, absolutize("http://%zz", "/img.png"))
	assert.Empty(t, absolutize(base, "%zz"))
	assert.Empty(t, absolutize(base, "http://%zz/img.png"))
	assert.Empty(t, absolutize("", "/img.png"))

	// Resolution against the page URL.
	assert.Equal(t, "https://cdn.example.com/og.png", absolutize(base, "//cdn.example.com/og.png"))
	assert.Equal(t, "https://news.example.com/a/up.png", absolutize(base, "../up.png"))
	assert.Equal(t, "https://news.example.com/a/b/dot.png", absolutize(base, "./dot.png"))
	assert.Equal(t, "https://news.example.com/a/b/spaces.png", absolutize(base, "  spaces.png  "))
	assert.Equal(t, "https://news.example.com/a/b/c?x=2", absolutize(base, "?x=2"))
	assert.Equal(t, "http://news.example.com/plain.png", absolutize(base, "http://news.example.com/plain.png"))

	// Non-http(s) outcomes stay refused, however they are spelled.
	assert.Empty(t, absolutize(base, "ftp://files.example.com/og.png"))
	assert.Empty(t, absolutize(base, "mailto:editor@example.com"))
	assert.Empty(t, absolutize("file:///tmp/page.html", "img.png"))
}

func TestExtractMetadata_RejectsUnparseableAndEmptyTargets(t *testing.T) {
	pool := NewPool()
	for _, target := range []string{"", "http://%zz", "://no-scheme", "https://", "example.com/page"} {
		_, err := pool.ExtractMetadata(context.Background(), target)
		require.Error(t, err, "target %q", target)
		assert.Contains(t, err.Error(), "invalid http(s) target", "target %q", target)
	}
}

func TestExtractMetadata_BrowserLaunchFailureSurfaces(t *testing.T) {
	pool := NewPool()
	t.Setenv("CHROME_PATH", "/nonexistent/chrome-binary-xyz")

	_, err := pool.ExtractMetadata(context.Background(), "https://example.com/page")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "launch browser")
}

// extractGenerationPool wires a pool whose browser, context lifecycle and
// extraction are all fakes, so ExtractMetadata's retirement decisions can be
// observed through the generation's launcher.
func extractGenerationPool(fake *fakeBrowserLauncher, generation *pooledBrowser, extract func() (PageMetadata, error)) *Pool {
	pool := NewPool()
	pool.closeBrowser = func(context.Context, *rod.Browser) error { return nil }
	pool.createBrowserContext = func(context.Context, *rod.Browser, browserContextProxy) (proto.BrowserBrowserContextID, error) {
		return "extract-more-context", nil
	}
	pool.disposeBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID) error { return nil }
	pool.extractBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID, string) (PageMetadata, error) {
		return extract()
	}
	pool.current = generation
	pool.generations[generation] = struct{}{}
	return pool
}

func TestExtractMetadata_BlockedProcessProxyAtEntryRetiresGeneration(t *testing.T) {
	fake := &fakeBrowserLauncher{pid: 4311}
	processProxy, err := newCaptureProxy()
	require.NoError(t, err)
	t.Cleanup(processProxy.Close)
	processProxy.markBlocked()

	generation := &pooledBrowser{browser: &rod.Browser{}, launcher: fake, proxy: processProxy, pid: fake.pid, stopped: make(chan struct{})}
	extracted := false
	pool := extractGenerationPool(fake, generation, func() (PageMetadata, error) {
		extracted = true
		return PageMetadata{Title: "must not happen"}, nil
	})

	_, err = pool.ExtractMetadata(context.Background(), "http://93.184.216.34/page")
	require.ErrorIs(t, err, ErrEgressBlocked)
	assert.False(t, extracted, "a blocked process proxy must refuse before extracting")
	awaitSignal(t, generation.stopped, "blocked-at-entry generation was not retired")
	assert.Equal(t, int64(1), fake.kills.Load(), "forced retirement kills the process")
	assert.Equal(t, int64(1), fake.cleanups.Load())
}

func TestExtractMetadata_RetirementDecisionsByErrorKind(t *testing.T) {
	sentinel := errors.New("underlying failure")
	for _, tc := range []struct {
		name      string
		extract   func(*captureProxy) (PageMetadata, error)
		wantKills int64
	}{
		{
			name: "browser context state forces retirement",
			extract: func(*captureProxy) (PageMetadata, error) {
				return PageMetadata{}, fmt.Errorf("%w: %w", errBrowserContextState, sentinel)
			},
			wantKills: 1,
		},
		{
			name: "egress blocked with blocked process proxy forces retirement",
			extract: func(processProxy *captureProxy) (PageMetadata, error) {
				processProxy.markBlocked()
				return PageMetadata{}, ErrEgressBlocked
			},
			wantKills: 1,
		},
		{
			name: "page open retires gracefully without a kill",
			extract: func(*captureProxy) (PageMetadata, error) {
				return PageMetadata{}, fmt.Errorf("%w: %w", errPageOpen, sentinel)
			},
			wantKills: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeBrowserLauncher{pid: 4312}
			processProxy, err := newCaptureProxy()
			require.NoError(t, err)
			t.Cleanup(processProxy.Close)
			generation := &pooledBrowser{browser: &rod.Browser{}, launcher: fake, proxy: processProxy, pid: fake.pid, stopped: make(chan struct{})}
			pool := extractGenerationPool(fake, generation, func() (PageMetadata, error) {
				return tc.extract(processProxy)
			})

			_, err = pool.ExtractMetadata(context.Background(), "http://93.184.216.34/page")
			require.Error(t, err)
			awaitSignal(t, generation.stopped, "generation was not retired")
			assert.Equal(t, tc.wantKills, fake.kills.Load())
			assert.Equal(t, int64(1), fake.cleanups.Load())
		})
	}
}

// fakeExtractPool returns a pool that runs extractWithBrowser end to end over
// fakes, handing the test the per-context proxy extractWithBrowser creates.
func fakeExtractPool(t *testing.T) (*Pool, **captureProxy) {
	t.Helper()
	pool := NewPool()
	var created *captureProxy
	pool.newCaptureProxy = func() (*captureProxy, error) {
		proxy, err := newCaptureProxy()
		created = proxy
		return proxy, err
	}
	pool.createBrowserContext = func(context.Context, *rod.Browser, browserContextProxy) (proto.BrowserBrowserContextID, error) {
		return "extract-more-direct", nil
	}
	pool.disposeBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID) error { return nil }
	t.Cleanup(func() {
		if created != nil {
			created.Close()
		}
	})
	return pool, &created
}

func TestExtractWithBrowser_ProxyCreationFailure(t *testing.T) {
	sentinel := errors.New("no proxy for you")
	pool := NewPool()
	pool.newCaptureProxy = func() (*captureProxy, error) { return nil, sentinel }

	_, err := pool.extractWithBrowser(context.Background(), &rod.Browser{}, nil, "http://93.184.216.34/page")
	assert.ErrorIs(t, err, sentinel)
}

func TestExtractWithBrowser_CreateContextFailure(t *testing.T) {
	sentinel := errors.New("create refused")
	pool, _ := fakeExtractPool(t)
	pool.createBrowserContext = func(context.Context, *rod.Browser, browserContextProxy) (proto.BrowserBrowserContextID, error) {
		return "", sentinel
	}

	_, err := pool.extractWithBrowser(context.Background(), &rod.Browser{}, nil, "http://93.184.216.34/page")
	assert.ErrorIs(t, err, errBrowserContextState)
	assert.ErrorIs(t, err, sentinel)
}

func TestExtractWithBrowser_DisposeFailureDiscardsMetadata(t *testing.T) {
	sentinel := errors.New("dispose refused")
	pool, _ := fakeExtractPool(t)
	pool.extractBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID, string) (PageMetadata, error) {
		return PageMetadata{Title: "discarded"}, nil
	}
	pool.disposeBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID) error {
		return sentinel
	}

	md, err := pool.extractWithBrowser(context.Background(), &rod.Browser{}, nil, "http://93.184.216.34/page")
	assert.Zero(t, md)
	assert.ErrorIs(t, err, errBrowserContextState)
	assert.ErrorIs(t, err, sentinel)
}

func TestExtractWithBrowser_BlockedContextProxy(t *testing.T) {
	pool, created := fakeExtractPool(t)
	pool.extractBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID, string) (PageMetadata, error) {
		(*created).markBlocked()
		return PageMetadata{Title: "discarded"}, nil
	}

	md, err := pool.extractWithBrowser(context.Background(), &rod.Browser{}, nil, "http://93.184.216.34/page")
	assert.Zero(t, md)
	assert.ErrorIs(t, err, ErrEgressBlocked)
	assert.NotErrorIs(t, err, errBrowserContextState)
}

func TestExtractWithBrowser_BlockedContextProxyPlusDisposeFailureJoins(t *testing.T) {
	sentinel := errors.New("dispose refused")
	pool, created := fakeExtractPool(t)
	pool.extractBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID, string) (PageMetadata, error) {
		(*created).markBlocked()
		return PageMetadata{Title: "discarded"}, nil
	}
	pool.disposeBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID) error {
		return sentinel
	}

	_, err := pool.extractWithBrowser(context.Background(), &rod.Browser{}, nil, "http://93.184.216.34/page")
	assert.ErrorIs(t, err, ErrEgressBlocked)
	assert.ErrorIs(t, err, errBrowserContextState)
	assert.ErrorIs(t, err, sentinel)
}

func TestExtractWithBrowser_BlockedProcessProxyDiscardsMetadata(t *testing.T) {
	pool, _ := fakeExtractPool(t)
	processProxy, err := newCaptureProxy()
	require.NoError(t, err)
	t.Cleanup(processProxy.Close)
	pool.extractBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID, string) (PageMetadata, error) {
		processProxy.markBlocked()
		return PageMetadata{Title: "discarded"}, nil
	}

	md, err := pool.extractWithBrowser(context.Background(), &rod.Browser{}, processProxy, "http://93.184.216.34/page")
	assert.Zero(t, md)
	assert.ErrorIs(t, err, ErrEgressBlocked)
}

func TestExtractWithBrowser_ExtractErrorPassesThroughUntouched(t *testing.T) {
	sentinel := errors.New("plain extraction failure")
	pool, _ := fakeExtractPool(t)
	pool.extractBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID, string) (PageMetadata, error) {
		return PageMetadata{}, sentinel
	}

	_, err := pool.extractWithBrowser(context.Background(), &rod.Browser{}, nil, "http://93.184.216.34/page")
	assert.ErrorIs(t, err, sentinel)
	assert.NotErrorIs(t, err, ErrEgressBlocked)
	assert.NotErrorIs(t, err, errBrowserContextState)
}
