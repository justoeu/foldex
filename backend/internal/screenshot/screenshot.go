package screenshot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Capture navigates to pageURL with a headless Chromium browser and returns a
// PNG screenshot. The CHROME_PATH env var overrides the browser binary path,
// which is required inside the Docker image (apk chromium sets it to
// /usr/bin/chromium-browser).
func Capture(ctx context.Context, pageURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

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

	browser := rod.New().ControlURL(url).Context(ctx)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("screenshot: connect browser: %w", err)
	}
	// MustClose panics on error; the older code accepted that, but a panic
	// inside the screenshot worker takes the goroutine down. Plain Close +
	// log keeps the worker healthy and surfaces the failure for ops.
	defer func() {
		if err := browser.Close(); err != nil {
			slog.Warn("screenshot: browser close failed", "err", err)
		}
	}()

	page, err := browser.Page(proto.TargetCreateTarget{URL: pageURL})
	if err != nil {
		return nil, fmt.Errorf("screenshot: open page: %w", err)
	}

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
