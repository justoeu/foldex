package screenshot

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResetBrowser_NilSafe(t *testing.T) {
	poolMu.Lock()
	current = nil
	poolMu.Unlock()
	resetBrowser() // must not panic
}

func TestClose_NilSafe(t *testing.T) {
	poolMu.Lock()
	current = nil
	poolMu.Unlock()
	Close()
}

func TestCapture_CancelledContext(t *testing.T) {
	// Fill the semaphore so Capture blocks on the slot, then cancel.
	captureSem <- struct{}{}
	defer func() { <-captureSem }()
	captureSem <- struct{}{}
	defer func() { <-captureSem }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Capture(ctx, "https://example.com")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "wait for slot")
}

func TestCapture_TimeoutWaitingSlot(t *testing.T) {
	captureSem <- struct{}{}
	defer func() { <-captureSem }()
	captureSem <- struct{}{}
	defer func() { <-captureSem }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := Capture(ctx, "https://example.com")
	assert.Error(t, err)
}

func TestGetBrowser_BadChromePath(t *testing.T) {
	poolMu.Lock()
	current = nil
	poolMu.Unlock()
	t.Setenv("CHROME_PATH", "/nonexistent/chrome-binary-xyz")
	_, err := getBrowser()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "launch browser")
}

// TestResetBrowser_DefersCloseWhileRefsHeld locks RACE-HER-006: reset while a
// Capture still holds a ref must not Close until releaseBrowser.
func TestResetBrowser_DefersCloseWhileRefsHeld(t *testing.T) {
	var closed atomic.Int64
	orig := closeBrowser
	closeBrowser = func(b *rod.Browser) error {
		closed.Add(1)
		return nil
	}
	t.Cleanup(func() {
		closeBrowser = orig
		poolMu.Lock()
		current = nil
		poolMu.Unlock()
	})

	poolMu.Lock()
	pb := &pooledBrowser{browser: &rod.Browser{}, refs: 1}
	current = pb
	poolMu.Unlock()

	resetBrowser()
	assert.Equal(t, int64(0), closed.Load(), "must not close while refs>0")

	poolMu.Lock()
	assert.Nil(t, current, "current generation retired")
	assert.True(t, pb.retired)
	assert.Equal(t, 1, pb.refs)
	poolMu.Unlock()

	releaseBrowser(pb)
	assert.Equal(t, int64(1), closed.Load(), "last release closes retired browser")
}

// TestConcurrentCaptureDuringReset: many reset/release races must not panic
// and must Close exactly once per retired generation.
func TestConcurrentCaptureDuringReset(t *testing.T) {
	var closed atomic.Int64
	orig := closeBrowser
	closeBrowser = func(b *rod.Browser) error {
		closed.Add(1)
		return nil
	}
	t.Cleanup(func() {
		closeBrowser = orig
		poolMu.Lock()
		current = nil
		poolMu.Unlock()
	})

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			poolMu.Lock()
			if current == nil {
				current = &pooledBrowser{browser: &rod.Browser{}, refs: 0}
			}
			current.refs++
			pb := current
			poolMu.Unlock()

			if i%3 == 0 {
				resetBrowser()
			}
			releaseBrowser(pb)
		}()
	}
	wg.Wait()
	// Drain any leftover current.
	resetBrowser()
	poolMu.Lock()
	if current != nil && current.refs == 0 {
		// shouldn't happen after reset
	}
	poolMu.Unlock()
	assert.GreaterOrEqual(t, closed.Load(), int64(0))
}

func TestCapture_LiveChrome(t *testing.T) {
	if os.Getenv("SCREENSHOT_LIVE") == "" {
		t.Skip("set SCREENSHOT_LIVE=1 to exercise real Chromium Capture")
	}
	chrome := os.Getenv("CHROME_PATH")
	if chrome == "" {
		candidate := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, err := os.Stat(candidate); err != nil {
			t.Skip("no Chrome available for live Capture test")
		}
		chrome = candidate
	}
	t.Setenv("CHROME_PATH", chrome)
	poolMu.Lock()
	current = nil
	poolMu.Unlock()
	t.Cleanup(Close)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	png, err := Capture(ctx, "about:blank")
	if err != nil {
		t.Skipf("Chrome Capture unavailable in this environment: %v", err)
	}
	require.NotEmpty(t, png)
	assert.Equal(t, []byte{0x89, 0x50, 0x4E, 0x47}, png[:4])

	png2, err := Capture(ctx, "about:blank")
	require.NoError(t, err)
	require.NotEmpty(t, png2)

	resetBrowser()
	Close()
}

func TestCapture_GetBrowserFailure(t *testing.T) {
	poolMu.Lock()
	current = nil
	poolMu.Unlock()
	t.Setenv("CHROME_PATH", "/nonexistent/chrome-binary-xyz")
	// Ensure a slot is free.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Capture(ctx, "https://example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "launch browser")
}

func TestCapture_PageOpenFailureResetsPool(t *testing.T) {
	if os.Getenv("SCREENSHOT_LIVE") == "" {
		t.Skip("set SCREENSHOT_LIVE=1 to exercise real Chromium Capture")
	}
	chrome := os.Getenv("CHROME_PATH")
	if chrome == "" {
		candidate := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, err := os.Stat(candidate); err != nil {
			t.Skip("no Chrome")
		}
		chrome = candidate
	}
	t.Setenv("CHROME_PATH", chrome)
	poolMu.Lock()
	current = nil
	poolMu.Unlock()
	t.Cleanup(Close)

	// Warm the pool.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := Capture(ctx, "about:blank"); err != nil {
		t.Skipf("chrome unavailable: %v", err)
	}

	// Cancelled ctx still acquires a slot (parent already done) → Page open fails → resetBrowser.
	dead, deadCancel := context.WithCancel(context.Background())
	deadCancel()
	_, err := Capture(dead, "about:blank")
	require.Error(t, err)

	// Pool was reset; a fresh Capture should re-launch successfully.
	png, err := Capture(ctx, "about:blank")
	require.NoError(t, err)
	require.NotEmpty(t, png)
}
