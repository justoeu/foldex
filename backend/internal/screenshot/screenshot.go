package screenshot

import (
	"context"
	"fmt"
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
	defer browser.MustClose()

	page, err := browser.Page(proto.TargetCreateTarget{URL: pageURL})
	if err != nil {
		return nil, fmt.Errorf("screenshot: open page: %w", err)
	}

	// Wait until the network is idle (or 10 s, whichever comes first).
	if err := page.WaitLoad(); err != nil {
		// Non-fatal: we still take the screenshot even if some resources fail.
		_ = err
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
