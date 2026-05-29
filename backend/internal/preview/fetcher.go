package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Result holds the metadata extracted from a page (any field may be empty).
type Result struct {
	Title       string
	Description string
	FaviconURL  string
	OGImageURL  string
}

type Fetcher struct {
	client *http.Client
}

func NewFetcher(timeout time.Duration) *Fetcher {
	tr := &http.Transport{
		DialContext: (&safeDialer{base: &net.Dialer{Timeout: timeout}, strict: strictSSRF()}).DialContext,
		// Keep TLS handshake bounded inside the overall client timeout.
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       30 * time.Second,
	}
	return &Fetcher{
		client: &http.Client{
			Timeout: timeout,
			Transport: tr,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
	}
}

// Fetch returns the metadata for pageURL. SSRF guard is enforced in the dialer.
func (f *Fetcher) Fetch(ctx context.Context, pageURL string) (Result, error) {
	u, err := url.Parse(pageURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Result{}, fmt.Errorf("invalid url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "FoldexPreviewBot/1.0 (+local)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := f.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	// Cap to 2 MB of HTML — the head is always at the top.
	body := io.LimitReader(resp.Body, 2<<20)
	r := parseHead(body)
	finalURL := resp.Request.URL // resolved after redirects
	resolved := resolveRelatives(r, finalURL)
	if resolved.FaviconURL == "" {
		resolved.FaviconURL = finalURL.Scheme + "://" + finalURL.Host + "/favicon.ico"
	}
	return resolved, nil
}

func parseHead(r io.Reader) Result {
	z := html.NewTokenizer(r)
	out := Result{}
	depth := 0
	inHead := false
loop:
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			break loop
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			name := tok.Data
			switch name {
			case "head":
				inHead = true
			case "body":
				break loop
			case "title":
				if tt == html.StartTagToken {
					if z.Next() == html.TextToken {
						out.Title = strings.TrimSpace(z.Token().Data)
					}
				}
			case "meta":
				if !inHead && depth > 1 {
					continue
				}
				property := attr(tok, "property")
				nameAttr := attr(tok, "name")
				content := attr(tok, "content")
				switch {
				case property == "og:image" && out.OGImageURL == "":
					out.OGImageURL = content
				case (property == "og:description" || nameAttr == "description") && out.Description == "":
					out.Description = content
				case property == "og:title" && out.Title == "":
					out.Title = content
				}
			case "link":
				rel := strings.ToLower(attr(tok, "rel"))
				href := attr(tok, "href")
				if (rel == "icon" || rel == "shortcut icon" || strings.Contains(rel, "icon")) && out.FaviconURL == "" {
					out.FaviconURL = href
				}
			}
			if tt == html.StartTagToken && !isVoid(name) {
				depth++
			}
		case html.EndTagToken:
			tok := z.Token()
			if tok.Data == "head" {
				break loop
			}
			depth--
		}
	}
	return out
}

func attr(t html.Token, key string) string {
	for _, a := range t.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func isVoid(name string) bool {
	switch name {
	case "area", "base", "br", "col", "embed", "hr", "img", "input",
		"link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}

func resolveRelatives(r Result, base *url.URL) Result {
	r.FaviconURL = resolveOne(r.FaviconURL, base)
	r.OGImageURL = resolveOne(r.OGImageURL, base)
	return r
}

func resolveOne(href string, base *url.URL) string {
	if href == "" {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		return u.String()
	}
	return base.ResolveReference(u).String()
}

// ----- SSRF guard at the dial layer -----

type safeDialer struct {
	base   *net.Dialer
	strict bool // snapshot of PREVIEW_STRICT_SSRF at fetcher construction
}

func (d *safeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	// Pre-dial guard. The pre-dial check fails fast for cleanly-resolved bad
	// hosts (cheap LookupIP, no TCP RTT, no socket leak). But by itself it is
	// vulnerable to DNS rebinding — the resolver can return a public IP for
	// this lookup and a private IP for the resolver call that net.Dialer
	// performs internally. The post-dial RemoteAddr check below closes that
	// gap, since RemoteAddr reflects the IP we actually connected to.
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		// IMDS (AWS/GCP/Azure metadata) is ALWAYS refused — no env opt-out.
		// It's never a legitimate preview target and exposing it is the only
		// well-known SSRF footgun on a workstation.
		if isIMDS(ip) {
			return nil, fmt.Errorf("ssrf: refusing IMDS endpoint %s", ip)
		}
		if d.strict && isPrivateIP(ip) {
			return nil, fmt.Errorf("ssrf: refusing to dial %s (%s)", host, ip)
		}
	}
	conn, err := d.base.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if err := checkRemoteAddrSSRF(d.strict, conn.RemoteAddr(), host); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// checkRemoteAddrSSRF validates the post-dial peer address against the SSRF
// policy. Extracted so the rebinding defense can be unit-tested without a
// real Dial — feed it a faked net.Addr.
//
// HTTP transport always dials TCP (tcp4/tcp6), so the type assertion is
// expected to succeed. Fail closed if it ever doesn't: a non-TCP conn path
// would silently re-open the rebinding hole.
func checkRemoteAddrSSRF(strict bool, addr net.Addr, host string) error {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("ssrf: non-TCP remote addr %T — refusing", addr)
	}
	if isIMDS(tcp.IP) {
		return fmt.Errorf("ssrf: refusing IMDS endpoint %s (post-dial)", tcp.IP)
	}
	if strict && isPrivateIP(tcp.IP) {
		return fmt.Errorf("ssrf: refusing peer %s for host %s (post-dial)", tcp.IP, host)
	}
	return nil
}

// strictSSRF reports whether PREVIEW_STRICT_SSRF is enabled. When true the
// dialer additionally refuses loopback, RFC1918, link-local and IPv6 ULA.
// Default (single-user / local dev) is permissive — you usually want to save
// links from your own intranet (Jira, Grid, Confluence, internal dashboards).
func strictSSRF() bool {
	v := os.Getenv("PREVIEW_STRICT_SSRF")
	return v == "1" || v == "true" || v == "TRUE" || v == "yes"
}

func isIMDS(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] == 169 && ip4[1] == 254 && ip4[2] == 169 && ip4[3] == 254
	}
	return false
}

func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 10 {
			return true
		}
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
	} else if len(ip) == 16 && ip[0]&0xfe == 0xfc {
		// IPv6 unique local fc00::/7
		return true
	}
	return false
}
