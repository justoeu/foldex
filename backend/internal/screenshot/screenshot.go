package screenshot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync/atomic"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// Capture navigates to pageURL with a pooled headless Chromium process and
// returns a PNG screenshot. Every call uses a disposable incognito context and
// dedicated strict egress proxy so browser state cannot cross captures.
func (p *Pool) Capture(ctx context.Context, pageURL string) ([]byte, error) {
	parsed, err := url.Parse(pageURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, fmt.Errorf("screenshot: invalid http(s) target")
	}

	waitCtx, waitCancel := context.WithTimeout(ctx, p.queueTimeout)
	select {
	case p.captureSem <- struct{}{}:
		waitCancel()
		defer func() { <-p.captureSem }()
	case <-p.shutdown:
		waitCancel()
		return nil, errPoolClosed
	case <-waitCtx.Done():
		waitCancel()
		return nil, fmt.Errorf("screenshot: wait for slot: %w", waitCtx.Err())
	}

	browser, hold, err := p.acquireBrowser(ctx)
	if err != nil {
		return nil, err
	}
	defer p.releaseBrowser(hold)

	if hold.proxy != nil && hold.proxy.Blocked() {
		p.retireBrowserGeneration(hold, true)
		return nil, ErrEgressBlocked
	}
	if hold.proxy != nil {
		hold.proxy.resetBudgets()
	}
	captureCtx, captureCancel := context.WithTimeout(ctx, p.executionTimeout)
	png, err := p.captureWithBrowser(captureCtx, browser, hold.proxy, pageURL)
	captureCancel()
	if errors.Is(err, errBrowserContextState) {
		p.retireBrowserGeneration(hold, true)
	} else if errors.Is(err, ErrEgressBlocked) && hold.proxy != nil && hold.proxy.Blocked() {
		p.retireBrowserGeneration(hold, true)
	} else if errors.Is(err, errPageOpen) {
		p.retireBrowserGeneration(hold, false)
	}
	return png, err
}

const proxyBypassNone = "<-loopback>"

type browserContextProxy struct {
	Server     string
	BypassList string
}

var (
	errPageOpen            = errors.New("screenshot: page open failed")
	errBrowserContextState = errors.New("screenshot: browser context state unknown")
)

func (p *Pool) captureWithBrowser(ctx context.Context, browser *rod.Browser, processProxy *captureProxy, pageURL string) ([]byte, error) {
	proxy, err := p.newCaptureProxy()
	if err != nil {
		return nil, err
	}

	contextID, err := p.createBrowserContext(ctx, browser, browserContextProxy{
		Server:     proxy.Address(),
		BypassList: proxyBypassNone,
	})
	if err != nil {
		proxy.Close()
		// Context creation may have reached Chromium before the CDP response
		// was cancelled. Without an id, process retirement is the only cleanup.
		return nil, fmt.Errorf("%w: create: %w", errBrowserContextState, err)
	}

	png, captureErr := p.captureBrowserContext(ctx, browser, contextID, pageURL)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), p.contextCleanupTimeout)
	disposeErr := p.disposeBrowserContext(cleanupCtx, browser, contextID)
	cleanupCancel()
	proxy.Close()
	if disposeErr != nil {
		// Process retirement guarantees disposal when the bounded CDP cleanup
		// cannot establish the BrowserContext's final state.
		captureErr = fmt.Errorf("%w: dispose: %w", errBrowserContextState, disposeErr)
	}
	if proxy.Blocked() {
		if disposeErr != nil {
			return nil, errors.Join(ErrEgressBlocked, captureErr)
		}
		return nil, ErrEgressBlocked
	}
	if processProxy != nil && processProxy.Blocked() {
		return nil, ErrEgressBlocked
	}
	if disposeErr != nil {
		return nil, captureErr
	}
	return png, captureErr
}

func capturePage(ctx context.Context, browser *rod.Browser, contextID proto.BrowserBrowserContextID, pageURL string, requestLimit int64) ([]byte, error) {
	// The capture context bounds every CDP command. BrowserContext disposal
	// uses a separate context so caller cancellation cannot preserve state.
	contextBrowser := browser.Context(ctx)
	contextBrowser.BrowserContextID = contextID
	page, err := contextBrowser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errPageOpen, err)
	}
	var requests atomic.Int64
	var budgetExceeded atomic.Bool
	router := page.HijackRequests()
	if err := router.Add("*", "", func(hijack *rod.Hijack) {
		if requests.Add(1) > requestLimit {
			budgetExceeded.Store(true)
			hijack.Response.Fail(proto.NetworkErrorReasonAborted)
			return
		}
		hijack.ContinueRequest(&proto.FetchContinueRequest{})
	}); err != nil {
		return nil, fmt.Errorf("screenshot: enable request budget: %w", err)
	}
	go router.Run()
	defer func() { _ = router.Stop() }()
	if err := page.Navigate(pageURL); err != nil {
		if budgetExceeded.Load() {
			return nil, errors.Join(ErrEgressBlocked, errProxyBudgetExceeded)
		}
		return nil, fmt.Errorf("%w: %w", errPageOpen, err)
	}

	// A slow third-party script does not justify failing the screenshot; the
	// capture context remains the hard upper bound for this wait.
	if err := page.WaitLoad(); err != nil {
		slog.Debug("screenshot: WaitLoad interrupted, proceeding when possible", "reason", captureErrorReason(err))
	}
	if budgetExceeded.Load() {
		return nil, errors.Join(ErrEgressBlocked, errProxyBudgetExceeded)
	}

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

func captureErrorReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "page_error"
	}
}
