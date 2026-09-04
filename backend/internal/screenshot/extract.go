package screenshot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// PageMetadata is the subset of <head> we can read after Chromium has run
// the page's JavaScript. Used when a plain HTTP GET is blocked (bot wall).
type PageMetadata struct {
	Title       string
	Description string
	FaviconURL  string
	OGImageURL  string
}

// ExtractMetadata navigates with the same pooled Chromium + SSRF proxy as
// Capture, then reads document.title / og tags from the rendered DOM.
func (p *Pool) ExtractMetadata(ctx context.Context, pageURL string) (PageMetadata, error) {
	parsed, err := url.Parse(pageURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return PageMetadata{}, fmt.Errorf("screenshot: invalid http(s) target")
	}

	waitCtx, waitCancel := context.WithTimeout(ctx, p.queueTimeout)
	select {
	case p.captureSem <- struct{}{}:
		waitCancel()
		defer func() { <-p.captureSem }()
	case <-p.shutdown:
		waitCancel()
		return PageMetadata{}, errPoolClosed
	case <-waitCtx.Done():
		waitCancel()
		return PageMetadata{}, fmt.Errorf("screenshot: wait for slot: %w", waitCtx.Err())
	}

	browser, hold, err := p.acquireBrowser(ctx)
	if err != nil {
		return PageMetadata{}, err
	}
	defer p.releaseBrowser(hold)

	if hold.proxy != nil && hold.proxy.Blocked() {
		p.retireBrowserGeneration(hold, true)
		return PageMetadata{}, ErrEgressBlocked
	}
	if hold.proxy != nil {
		hold.proxy.resetBudgets()
	}
	runCtx, runCancel := context.WithTimeout(ctx, p.executionTimeout)
	md, err := p.extractWithBrowser(runCtx, browser, hold.proxy, pageURL)
	runCancel()
	if errors.Is(err, errBrowserContextState) {
		p.retireBrowserGeneration(hold, true)
	} else if errors.Is(err, ErrEgressBlocked) && hold.proxy != nil && hold.proxy.Blocked() {
		p.retireBrowserGeneration(hold, true)
	} else if errors.Is(err, errPageOpen) {
		p.retireBrowserGeneration(hold, false)
	}
	return md, err
}

func (p *Pool) extractWithBrowser(ctx context.Context, browser *rod.Browser, processProxy *captureProxy, pageURL string) (PageMetadata, error) {
	proxy, err := p.newCaptureProxy()
	if err != nil {
		return PageMetadata{}, err
	}

	contextID, err := p.createBrowserContext(ctx, browser, browserContextProxy{
		Server:     proxy.Address(),
		BypassList: proxyBypassNone,
	})
	if err != nil {
		proxy.Close()
		return PageMetadata{}, fmt.Errorf("%w: create: %w", errBrowserContextState, err)
	}

	md, extractErr := p.extractBrowserContext(ctx, browser, contextID, pageURL)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), p.contextCleanupTimeout)
	disposeErr := p.disposeBrowserContext(cleanupCtx, browser, contextID)
	cleanupCancel()
	proxy.Close()
	if disposeErr != nil {
		extractErr = fmt.Errorf("%w: dispose: %w", errBrowserContextState, disposeErr)
	}
	if proxy.Blocked() {
		if disposeErr != nil {
			return PageMetadata{}, errors.Join(ErrEgressBlocked, extractErr)
		}
		return PageMetadata{}, ErrEgressBlocked
	}
	if processProxy != nil && processProxy.Blocked() {
		return PageMetadata{}, ErrEgressBlocked
	}
	if disposeErr != nil {
		return PageMetadata{}, extractErr
	}
	return md, extractErr
}

func extractPage(ctx context.Context, browser *rod.Browser, contextID proto.BrowserBrowserContextID, pageURL string, requestLimit int64) (PageMetadata, error) {
	contextBrowser := browser.Context(ctx)
	contextBrowser.BrowserContextID = contextID
	page, err := contextBrowser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return PageMetadata{}, fmt.Errorf("%w: %w", errPageOpen, err)
	}
	var requests atomic.Int64
	var budgetExceeded atomic.Bool
	router := page.HijackRequests()
	if err := router.Add("*", "", func(hijack *rod.Hijack) {
		switch hijack.Request.Type() {
		case proto.NetworkResourceTypeImage, proto.NetworkResourceTypeMedia,
			proto.NetworkResourceTypeFont, proto.NetworkResourceTypeStylesheet,
			proto.NetworkResourceTypePing, proto.NetworkResourceTypeManifest,
			proto.NetworkResourceTypeTextTrack:
			hijack.Response.Fail(proto.NetworkErrorReasonBlockedByClient)
			return
		}
		if requests.Add(1) > requestLimit {
			budgetExceeded.Store(true)
			hijack.Response.Fail(proto.NetworkErrorReasonAborted)
			return
		}
		hijack.ContinueRequest(&proto.FetchContinueRequest{})
	}); err != nil {
		return PageMetadata{}, fmt.Errorf("screenshot: enable request budget: %w", err)
	}
	go router.Run()
	defer func() { _ = router.Stop() }()
	if err := page.Navigate(pageURL); err != nil {
		if budgetExceeded.Load() {
			return PageMetadata{}, errors.Join(ErrEgressBlocked, errProxyBudgetExceeded)
		}
		return PageMetadata{}, fmt.Errorf("%w: %w", errPageOpen, err)
	}
	if err := page.WaitLoad(); err != nil {
		slog.Debug("screenshot: WaitLoad interrupted, proceeding when possible", "reason", captureErrorReason(err))
	}
	if budgetExceeded.Load() {
		return PageMetadata{}, errors.Join(ErrEgressBlocked, errProxyBudgetExceeded)
	}

	obj, err := page.Eval(`() => {
		const attr = (sel, name) => {
			const el = document.querySelector(sel);
			return el ? (el.getAttribute(name) || '') : '';
		};
		return {
			title: document.title || '',
			description: attr('meta[name="description"]', 'content') || attr('meta[property="og:description"]', 'content'),
			ogImage: attr('meta[property="og:image"]', 'content'),
			favicon: attr('link[rel="icon"]', 'href') || attr('link[rel="shortcut icon"]', 'href')
		};
	}`)
	if err != nil {
		return PageMetadata{}, fmt.Errorf("screenshot: read metadata: %w", err)
	}
	md := PageMetadata{}
	if obj != nil {
		md.Title = strings.TrimSpace(obj.Value.Get("title").Str())
		md.Description = strings.TrimSpace(obj.Value.Get("description").Str())
		md.OGImageURL = absolutize(pageURL, obj.Value.Get("ogImage").Str())
		md.FaviconURL = absolutize(pageURL, obj.Value.Get("favicon").Str())
	}
	if md.Title == "" && md.Description == "" && md.OGImageURL == "" {
		return PageMetadata{}, fmt.Errorf("screenshot: rendered page had no metadata")
	}
	return md, nil
}

func absolutize(pageURL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	abs := base.ResolveReference(u)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	return abs.String()
}
