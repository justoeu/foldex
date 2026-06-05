package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// oembedSubDeadline caps each oEmbed leg independently of the caller's ctx
// deadline. Without it, an HTML fetch that ate most of the parent ctx budget
// would leave the enrichment call almost no time and silently fail; the
// shortcut path would inherit the same starvation. 5s is generous for
// providers like YouTube (typical p50 ~150 ms) yet bounded enough that a
// slow provider can't dominate the user-perceived latency of LinkDialog
// auto-fill.
const oembedSubDeadline = 5 * time.Second

// oembedResponse is the subset of the oEmbed spec (https://oembed.com) we care
// about. The spec lists many optional fields per provider — we extract the
// minimum that maps onto our Result struct (Title, Description, OGImageURL).
// FaviconURL is never in oEmbed; the caller synthesizes one from the host.
type oembedResponse struct {
	Title        string `json:"title"`
	ThumbnailURL string `json:"thumbnail_url"`
	// Description is non-standard but several providers (Vimeo, Flickr) ship it.
	Description string `json:"description"`
}

// knownOEmbedProviders maps a page-host to the oEmbed JSON endpoint template
// (one %s placeholder for the URL-encoded page URL). Hardcoded list, not
// runtime-extensible: we add a provider here only when we've confirmed the
// regular HTML fetch is unreliable for that host (bot detection, geo wall,
// SPA-only render). Discovery via <link rel="alternate"
// type="application/json+oembed"> handles the long tail of providers whose
// HTML works but who ALSO advertise oEmbed.
//
// YouTube serves a heavily-degraded HTML head to anything its bot heuristics
// don't like (containers fall in this bucket: changing UA, headers, cookies
// doesn't help — it's IP/TLS fingerprint). oEmbed is the documented
// preview API, has no CAPTCHA, no rate ceiling for casual use, and works
// from any IP.
//
// Order doesn't matter (Map lookup); patterns don't overlap.
var knownOEmbedProviders = map[string]string{
	"youtube.com":       "https://www.youtube.com/oembed?url=%s&format=json",
	"www.youtube.com":   "https://www.youtube.com/oembed?url=%s&format=json",
	"m.youtube.com":     "https://www.youtube.com/oembed?url=%s&format=json",
	"music.youtube.com": "https://www.youtube.com/oembed?url=%s&format=json",
	"youtu.be":          "https://www.youtube.com/oembed?url=%s&format=json",
	"vimeo.com":         "https://vimeo.com/api/oembed.json?url=%s",
	"www.vimeo.com":     "https://vimeo.com/api/oembed.json?url=%s",
}

// hardcodedOEmbedURL returns the oEmbed endpoint URL for pageURL if its host
// is one of the known providers. Empty string means "no shortcut — fall
// through to HTML fetch (+ discovery if the page advertises oEmbed)".
func hardcodedOEmbedURL(pageURL string) string {
	u, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	tpl, ok := knownOEmbedProviders[strings.ToLower(u.Host)]
	if !ok {
		return ""
	}
	return fmt.Sprintf(tpl, url.QueryEscape(pageURL))
}

// fetchOEmbed retrieves and parses an oEmbed JSON document from oembedURL via
// the same SSRF-guarded http.Client as the HTML fetcher. Body cap is 64 KiB
// (oEmbed responses are typically a few hundred bytes; the cap is paranoia,
// not a real budget).
//
// The scheme check here is critical for the discovery path: an `OEmbedURL`
// captured from arbitrary remote HTML can advertise any URL shape. Go's
// default transport happily reads `file:///etc/passwd` (no SSRF dial guard
// fires because that path never hits the IP-level dialer), and `gopher://`,
// `unix://`, `ftp://` etc. all bypass the dialer too. Refuse anything that
// isn't http(s) at the edge — same posture as Fetch and the metadata
// handler.
func (f *Fetcher) fetchOEmbed(ctx context.Context, oembedURL string) (Result, error) {
	u, err := url.Parse(oembedURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Result{}, fmt.Errorf("oembed: invalid url scheme")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, oembedURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "FoldexPreviewBot/1.0 (+local)")
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("oembed status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Result{}, err
	}
	var r oembedResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return Result{}, err
	}
	return Result{
		Title:       strings.TrimSpace(r.Title),
		Description: strings.TrimSpace(r.Description),
		OGImageURL:  strings.TrimSpace(r.ThumbnailURL),
		// FaviconURL stays empty — caller synthesizes from the page host.
	}, nil
}

// mergeOEmbed enriches an existing HTML-parsed Result with values from an
// oEmbed Result. The HTML result wins on every field that was already
// populated; oEmbed only fills the gaps. This is the "discovery" enrichment
// flow — when an HTML page works AND advertises oEmbed, we run the oEmbed
// fetch on the side and use it to backfill missing fields.
func mergeOEmbed(html, oe Result) Result {
	if html.Title == "" {
		html.Title = oe.Title
	}
	if html.Description == "" {
		html.Description = oe.Description
	}
	if html.OGImageURL == "" {
		html.OGImageURL = oe.OGImageURL
	}
	return html
}
