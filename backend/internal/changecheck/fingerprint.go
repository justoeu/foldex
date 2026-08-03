// Package changecheck periodically re-fetches opted-in links and detects
// content drift, emitting Web Push notifications when a fingerprint changes.
//
// Fingerprint strategy is hybrid:
//
//  1. If the page exposes a feed (<link rel="alternate" type="application/rss+xml">
//     or atom+xml), the worker fetches that feed and hashes the sorted set of
//     GUIDs / entry IDs. Returns "feed:<hex>".
//  2. Otherwise, hash the textual content inside <main> / <article> (or <body>
//     as a last resort) with <script>/<style>/<nav>/<header>/<footer> stripped
//     and whitespace normalized. Returns "content:<hex>".
//
// The "feed:" / "content:" prefix is recorded with the fingerprint so a
// strategy switch (page gains a feed mid-life) doesn't fire a spurious
// change — the worker can tell the kinds apart and treat the transition as
// "establish new baseline", not "page changed".
package changecheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// FingerprintKind is the prefix that precedes the hex digest. Stable strings
// — older rows compare equal to newer rows of the same kind.
const (
	KindFeed    = "feed"
	KindContent = "content"
)

// Fingerprinter wraps an HTTP-capable fetcher so the change-check worker can
// reuse the preview Fetcher's SSRF guards instead of opening its own dialer.
type Fingerprinter struct {
	httpClient HTTPGetter
}

// HTTPGetter is the minimal contract the fingerprinter needs from a fetcher:
// fetch the raw body of an absolute URL and return ([]byte, contentType, err).
// preview.Fetcher.GetRaw satisfies this — wired via fetcher_adapter.go.
type HTTPGetter interface {
	GetRaw(ctx context.Context, pageURL string) (body []byte, contentType string, err error)
}

func NewFingerprinter(g HTTPGetter) *Fingerprinter { return &Fingerprinter{httpClient: g} }

// Compute returns the (kind, value) tuple for a page. `pageHTML` is the HTML
// already fetched by the worker — passing it in avoids a double GET on every
// scan. Accepts []byte so the worker need not allocate a full string copy of
// a body that can be up to 4 MiB. `pageURL` is needed to resolve relative feed hrefs.
func (f *Fingerprinter) Compute(ctx context.Context, pageURL string, pageHTML []byte) (kind string, value string, err error) {
	if feedURL := extractFeedURL(pageHTML, pageURL); feedURL != "" {
		if hash, ferr := f.fingerprintFeed(ctx, feedURL); ferr == nil {
			return KindFeed, hash, nil
		}
		// Feed declared but unreachable / malformed — fall through to content.
		// We still record kind=content so the next scan retries the same path.
	}
	hash, cerr := fingerprintContent(pageHTML)
	if cerr != nil {
		return "", "", cerr
	}
	return KindContent, hash, nil
}

// FormatFingerprint returns "kind:hex". Stored verbatim in
// link.last_fingerprint, parsed back via SplitFingerprint.
func FormatFingerprint(kind, hash string) string { return kind + ":" + hash }

// SplitFingerprint parses a stored value into kind + hash. Empty string
// returns ("","") — the worker treats that as "no previous fingerprint".
func SplitFingerprint(stored string) (kind, hash string) {
	idx := strings.IndexByte(stored, ':')
	if idx <= 0 {
		return "", ""
	}
	return stored[:idx], stored[idx+1:]
}

// extractFeedURL walks the HTML head looking for the first
// <link rel="alternate" type="application/(rss|atom)+xml" href="...">.
// Resolves a relative href against pageURL. Returns "" when no feed is
// declared. Tokenizer stops at </head> to avoid scanning the body.
func extractFeedURL(pageHTML []byte, pageURL string) string {
	z := html.NewTokenizer(bytes.NewReader(pageHTML))
	base, _ := url.Parse(pageURL)
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return ""
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			n := string(name)
			if n == "head" {
				continue
			}
			if n == "body" {
				return ""
			}
			if n != "link" || !hasAttr {
				continue
			}
			if href := feedLinkHref(z); href != "" {
				return resolveHref(base, href)
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if string(name) == "head" {
				return ""
			}
		}
	}
}

func feedLinkHref(z *html.Tokenizer) string {
	var rel, typ, href string
	for {
		k, v, more := z.TagAttr()
		switch strings.ToLower(string(k)) {
		case "rel":
			rel = strings.ToLower(string(v))
		case "type":
			typ = strings.ToLower(string(v))
		case "href":
			href = string(v)
		}
		if !more {
			break
		}
	}
	if !strings.Contains(rel, "alternate") {
		return ""
	}
	if typ != "application/rss+xml" && typ != "application/atom+xml" {
		return ""
	}
	return href
}

func resolveHref(base *url.URL, href string) string {
	if href == "" {
		return ""
	}
	if base != nil {
		if ref, err := url.Parse(href); err == nil {
			return base.ResolveReference(ref).String()
		}
	}
	return href
}

// fingerprintFeed fetches the declared feed URL and hashes a sorted slice of
// item GUIDs / entry IDs. We don't parse a strict RSS/Atom schema — we just
// collect the textual contents of every <guid>, <id>, or <link> tag inside
// <item>/<entry>. That's robust enough for "did the latest set of items
// change?" without depending on namespaces being well-formed.
//
// Sorting before hashing means feed reordering (e.g. a CDN flipping the
// item list) doesn't count as a change. New IDs appearing or old IDs
// disappearing do.
func (f *Fingerprinter) fingerprintFeed(ctx context.Context, feedURL string) (string, error) {
	body, _, err := f.httpClient.GetRaw(ctx, feedURL)
	if err != nil {
		return "", fmt.Errorf("fetch feed: %w", err)
	}
	ids := extractFeedItemIDs(body)
	if len(ids) == 0 {
		return "", fmt.Errorf("feed had no item ids (%s)", feedURL)
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

// extractFeedItemIDs reads the feed via the same HTML tokenizer
// (golang.org/x/net/html is forgiving with XML-shaped content for this kind
// of "grab text inside known tags" workload — we don't need a strict XML
// parser here, and avoiding encoding/xml keeps the surface area small).
// Returns text contents of <guid>, <id>, and the href attribute of <link>
// when it's inside <item> or <entry>.
type feedIDCollector struct {
	inItem bool
	ids    []string
	buf    strings.Builder
	grab   bool
}

func (c *feedIDCollector) flush() {
	if c.grab {
		v := strings.TrimSpace(c.buf.String())
		if v != "" {
			c.ids = append(c.ids, v)
		}
	}
	c.buf.Reset()
	c.grab = false
}

func (c *feedIDCollector) onStart(z *html.Tokenizer, name string, hasAttr bool) {
	switch name {
	case "item", "entry":
		c.inItem = true
	case "guid", "id":
		if c.inItem {
			c.flush()
			c.grab = true
		}
	case "link":
		if c.inItem && hasAttr {
			if href := attrValue(z, "href"); href != "" {
				c.ids = append(c.ids, strings.TrimSpace(href))
			}
		}
	}
}

func (c *feedIDCollector) onEnd(name string) {
	switch name {
	case "item", "entry":
		c.flush()
		c.inItem = false
	case "guid", "id":
		c.flush()
	}
}

func attrValue(z *html.Tokenizer, key string) string {
	var out string
	for {
		k, v, more := z.TagAttr()
		if strings.ToLower(string(k)) == key {
			out = string(v)
		}
		if !more {
			break
		}
	}
	return out
}

func extractFeedItemIDs(feedXML []byte) []string {
	z := html.NewTokenizer(bytes.NewReader(feedXML))
	var c feedIDCollector
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return c.ids
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			c.onStart(z, strings.ToLower(string(name)), hasAttr)
		case html.TextToken:
			if c.grab {
				c.buf.Write(z.Text())
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			c.onEnd(strings.ToLower(string(name)))
		}
	}
}

// fingerprintContent hashes the visible text inside <main> or <article>.
// Falls back to the full <body> when neither is present (small sites,
// landing pages). Noise tags (script/style/nav/header/footer) are
// completely skipped — both their text and any of their nested children.
//
// Whitespace is collapsed to single spaces and the result trimmed so
// minor reformatting (extra blank lines, indentation changes) doesn't
// flip the hash.
func fingerprintContent(pageHTML []byte) (string, error) {
	text := extractMainContent(pageHTML)
	text = normalizeWhitespace(text)
	if text == "" {
		return "", fmt.Errorf("no extractable content")
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:]), nil
}

// extractMainContent returns the concatenated text inside the first <main>
// or <article> element, falling back to <body> when neither exists. Skips
// any text inside <script>, <style>, <nav>, <header>, <footer>, including
// nested children.
func extractMainContent(pageHTML []byte) string {
	doc, err := html.Parse(bytes.NewReader(pageHTML))
	if err != nil {
		return ""
	}
	var main, article, body *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "main":
				if main == nil {
					main = n
				}
			case "article":
				if article == nil {
					article = n
				}
			case "body":
				if body == nil {
					body = n
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	root := main
	if root == nil {
		root = article
	}
	if root == nil {
		root = body
	}
	if root == nil {
		return ""
	}
	var b strings.Builder
	collectText(root, &b)
	return b.String()
}

// noiseTags is the set of element names we skip entirely (including their
// subtree) when collecting visible text. Comments are skipped at the
// node-type level below.
var noiseTags = map[string]struct{}{
	"script":   {},
	"style":    {},
	"nav":      {},
	"header":   {},
	"footer":   {},
	"noscript": {},
	"template": {},
}

func collectText(n *html.Node, b *strings.Builder) {
	if n.Type == html.ElementNode {
		if _, skip := noiseTags[n.Data]; skip {
			return
		}
	}
	if n.Type == html.CommentNode || n.Type == html.DoctypeNode {
		return
	}
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
		b.WriteByte(' ')
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectText(c, b)
	}
}

// normalizeWhitespace collapses runs of any whitespace to a single space and
// trims leading/trailing whitespace.
func normalizeWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := true // suppress leading whitespace
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		b.WriteRune(r)
		inSpace = false
	}
	return strings.TrimRight(b.String(), " ")
}
