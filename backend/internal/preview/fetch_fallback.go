package preview

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// Renderer extracts title/description from a fully rendered page (Chromium).
// Used when a plain HTTP GET is blocked by a bot wall.
type Renderer interface {
	ExtractMetadata(ctx context.Context, pageURL string) (Result, error)
}

// httpBeforeRenderBudget caps the cheap HTTP/oEmbed pass so a hanging origin
// cannot eat the caller's deadline before Chromium gets a turn. Matches the
// default PREVIEW_FETCH_TIMEOUT_SEC.
const httpBeforeRenderBudget = 5 * time.Second

// renderBudget is the Chromium slice of the url-metadata request. Combined
// with the HTTP cap this stays under the handler's 15s envelope.
const renderBudget = 10 * time.Second

// FetchThenRender tries the HTTP/oEmbed path first. Chromium runs only when
// the origin is a bot wall (401/403/429), the HTTP pass timed out, or the
// title is empty/interstitial. DNS/TLS/5xx and SSRF never launch a browser.
// A renderer miss still returns the HTTP result (possibly empty) so the
// caller can treat "no metadata" as success-with-gaps rather than an API fault.
func (f *Fetcher) FetchThenRender(ctx context.Context, pageURL string, r Renderer) (Result, error) {
	httpCtx := ctx
	if r != nil {
		var cancel context.CancelFunc
		httpCtx, cancel = context.WithTimeout(ctx, httpBeforeRenderBudget)
		defer cancel()
	}
	got, err := f.Fetch(httpCtx, pageURL)
	if err == nil && usableTitle(got.Title) {
		return got, nil
	}
	if r == nil || !shouldRender(ctx, err, got.Title) {
		return got, err
	}
	renderCtx, renderCancel := context.WithTimeout(ctx, renderBudget)
	rendered, rerr := r.ExtractMetadata(renderCtx, pageURL)
	renderCancel()
	if rerr != nil {
		return got, err
	}
	return mergeRendered(got, rendered), nil
}

func mergeRendered(httpRes, rendered Result) Result {
	out := httpRes
	if !usableTitle(out.Title) {
		if usableTitle(rendered.Title) {
			out.Title = rendered.Title
		} else {
			out.Title = ""
		}
	}
	if strings.TrimSpace(out.Description) == "" {
		out.Description = rendered.Description
	}
	if strings.TrimSpace(out.OGImageURL) == "" {
		out.OGImageURL = rendered.OGImageURL
	}
	if strings.TrimSpace(out.FaviconURL) == "" {
		out.FaviconURL = rendered.FaviconURL
	}
	return out
}

func shouldRender(ctx context.Context, err error, title string) bool {
	if ctx.Err() != nil {
		return false
	}
	if err == nil {
		return !usableTitle(title)
	}
	if isSSRFError(err) || errors.Is(err, context.Canceled) {
		return false
	}
	if isTimeout(err) {
		return true
	}
	msg := err.Error()
	for _, code := range []string{"status 401", "status 403", "status 429"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}

func isSSRFError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ssrf:")
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// usableTitle rejects empty strings and the interstitial titles a bot-wall
// serves as 200 OK. Treating those as success would skip Chromium — the
// only path that can actually render the page.
func usableTitle(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	if lower == "just a moment..." || lower == "just a moment" {
		return false
	}
	if strings.Contains(lower, "attention required") && strings.Contains(lower, "cloudflare") {
		return false
	}
	return true
}
