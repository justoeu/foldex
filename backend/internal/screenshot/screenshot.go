package screenshot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Capture navigates to pageURL with a headless Chromium browser and returns a
// PNG screenshot. The CHROME_PATH env var overrides the browser binary path,
// which is required inside the Docker image (apk chromium sets it to
// /usr/bin/chromium-browser).
//
// Pooling: the first call lazily launches a single headless Chromium and caches
// the connection. Subsequent calls reuse it and pay only the cost of opening a
// new tab (~10 ms). The previous per-call implementation cold-started a fresh
// browser every time — 500 ms–2 s + 150–400 MB RAM each, multiplied by N when
// the preview worker runs screenshots concurrently for many links. One shared
// browser turns that into a fixed startup cost amortized across the process
// lifetime.
//
// Self-healing: if a page-open fails (the cached Chromium may have crashed),
// the pool is reset so the next call re-launches instead of failing forever.
// Use Close() at process shutdown to tear the browser down cleanly.
func Capture(ctx context.Context, pageURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	browser, err := getBrowser()
	if err != nil {
		return nil, err
	}

	// browser.Context(ctx) returns a *rod.Browser that threads the per-call
	// deadline through every subsequent CDP call on this page — without it,
	// a wedged site would hang the pooled browser past the caller's timeout.
	page, err := browser.Context(ctx).Page(proto.TargetCreateTarget{URL: pageURL})
	if err != nil {
		// Stale-pool recovery: the cached browser may have died (Chromium
		// crash, OOM kill, container pressure). Drop it so the next Capture
		// re-launches instead of pinned to a dead connection. The current
		// call still surfaces the error; callers already treat screenshot
		// failure as best-effort (preview worker logs + moves on).
		resetBrowser()
		return nil, fmt.Errorf("screenshot: open page: %w", err)
	}
	// page.Close (not browser.Close!) frees the tab. The browser itself
	// stays pooled. A close failure is logged, not fatal — the tab will be
	// reaped when the browser exits.
	defer func() {
		if err := page.Close(); err != nil {
			slog.Warn("screenshot: page close failed", "err", err)
		}
	}()

	// Wait until the network is idle (or 10 s, whichever comes first).
	// Non-fatal: a slow third-party script doesn't justify failing the
	// screenshot. Log at debug so it's findable when investigating but
	// doesn't flood normal logs.
	if err := page.WaitLoad(); err != nil {
		slog.Debug("screenshot: WaitLoad timeout, proceeding anyway",
			"url", pageURL, "err", err)
	}

	// Set viewport to a standard 1280×800 desktop size.
	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:             1280,
		Height:            800,
		DeviceScaleFactor: 1,
	}); err != nil {
		return nil, fmt.Errorf("screenshot: set viewport: %w", err)
	}

	png, err := page.Screenshot(false, &proto.PageCaptureScreenshot{
		Format:  proto.PageCaptureScreenshotFormatPng,
		Quality: &[]int{90}[0],
	})
	if err != nil {
		return nil, fmt.Errorf("screenshot: capture: %w", err)
	}
	return png, nil
}

// pool guards poolBrowser. The browser itself is goroutine-safe (CDP supports
// concurrent targets), so the mutex only serializes the launch + connect path
// — concurrent Capture callers can open pages in parallel once connected.
var (
	poolMu      sync.Mutex
	poolBrowser *rod.Browser
)

// getBrowser returns the cached headless Chromium, launching it on first call.
// ctx is intentionally NOT propagated to launcher.Launch / rod.Connect: the
// cached browser must outlive any single caller, and binding it to a caller's
// ctx would tear down the pool when that ctx expires. Cold-start latency is
// bounded in practice (sub-second once the Chromium binary is warm) and the
// per-Capture 30s deadline still applies to every subsequent page-open via
// browser.Context(ctx).Page(...) in Capture above.
func getBrowser() (*rod.Browser, error) {
	poolMu.Lock()
	defer poolMu.Unlock()
	if poolBrowser != nil {
		return poolBrowser, nil
	}

	l := launcher.New().
		Set("no-sandbox", "").
		Set("disable-gpu", "").
		Set("disable-dev-shm-usage", "").
		Headless(true)
	if path := os.Getenv("CHROME_PATH"); path != "" {
		l = l.Bin(path)
	}

	url, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("screenshot: launch browser: %w", err)
	}
	b := rod.New().ControlURL(url)
	if err := b.Connect(); err != nil {
		return nil, fmt.Errorf("screenshot: connect browser: %w", err)
	}
	poolBrowser = b
	return b, nil
}

// resetBrowser closes (best-effort) and forgets the cached Chromium. Called
// from two paths: (a) self-healing when a page-open failed, and (b) graceful
// shutdown via Close(). The log message intentionally says "stale" only on
// the failure path; Close()'s shutdown close is silent on success.
func resetBrowser() {
	poolMu.Lock()
	defer poolMu.Unlock()
	if poolBrowser == nil {
		return
	}
	if err := poolBrowser.Close(); err != nil {
		slog.Warn("screenshot: pooled browser close failed", "err", err)
	}
	poolBrowser = nil
}

// Close tears down the pooled Chromium. Intended for graceful shutdown from
// main (signal handler). Safe to call multiple times; no-op when nothing is
// cached (e.g. screenshot endpoints were never wired or never hit).
func Close() {
	resetBrowser()
}
