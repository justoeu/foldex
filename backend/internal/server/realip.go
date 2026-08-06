package server

import (
	"net"
	"net/http"
	"strings"
)

// trustedProxyRealIP rewrites RemoteAddr from X-Forwarded-For, but ONLY when
// the request arrived from a configured proxy.
//
// It replaces chi's middleware.RealIP, which honours the header
// unconditionally. Behind nginx that is correct and invisible; on a direct
// bind it is forgeable by anyone, and every rate-limit bucket keyed by IP
// becomes decorative — an attacker rotates one header value per attempt and
// never trips a cap. Since the login path also keys a bucket by e-mail, and
// the second-factor budgets live in the database, the failure is a weakened
// defence rather than an open door; that is exactly why it went unnoticed.
//
// With no trusted proxies configured the header is ignored entirely. That is
// the safe default for the product's own default (BACKEND_BIND=127.0.0.1, no
// proxy in front): a spoofable identifier is worse than a coarse one, because
// it lets an attacker not merely evade their own bucket but ALSO pin the blame
// on someone else's.
func trustedProxyRealIP(trusted []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip := forwardedFor(r, trusted); ip != "" {
				r.RemoteAddr = ip
			}
			next.ServeHTTP(w, r)
		})
	}
}

// forwardedFor returns the client address to trust, or "" to keep RemoteAddr.
func forwardedFor(r *http.Request, trusted []*net.IPNet) string {
	if len(trusted) == 0 {
		return ""
	}
	peer := peerIP(r.RemoteAddr)
	if peer == nil || !containsIP(trusted, peer) {
		return ""
	}

	// X-Forwarded-For is "client, proxy1, proxy2" — appended left to right, so
	// the CLIENT is leftmost. Walk from the right instead, skipping addresses
	// that are themselves trusted proxies, and take the first that is not: with
	// two proxies chained, the leftmost entry is whatever the client sent and is
	// still attacker-controlled. X-Real-IP is a single value and only honoured
	// when XFF is absent.
	// Values, not Get: Get returns only the FIRST header line, and with two
	// X-Forwarded-For lines the first is the one the client sent. Joining them
	// keeps our own proxy's append at the right-hand end, where the walk below
	// expects it.
	if hops := forwardedHops(r.Header.Values("X-Forwarded-For")); len(hops) > 0 {
		for i := len(hops) - 1; i >= 0; i-- {
			ip := parseHop(hops[i])
			if ip == nil {
				// Garbage in a position we examine means the chain is not the
				// shape we expect, so fall back rather than guess. Note this
				// only fires for hops at or right of the one we would pick:
				// anything further left is client-supplied noise we never
				// reach, and discarding a good answer because of it would hand
				// an attacker a way to opt out of being identified.
				return ""
			}
			if !containsIP(trusted, ip) {
				return ip.String()
			}
		}
		// Every hop was a trusted proxy: the real client never appeared.
		return ""
	}

	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if ip := net.ParseIP(xri); ip != nil {
			return ip.String()
		}
	}
	return ""
}

// maxForwardedHops bounds the work a single header can cause. A megabyte of
// commas would otherwise split into half a million entries per request; only a
// trusted peer can reach this, but a compromised or misbehaving proxy is
// exactly the case worth bounding.
const maxForwardedHops = 32

func forwardedHops(headers []string) []string {
	var hops []string
	for _, h := range headers {
		for _, part := range strings.Split(h, ",") {
			if p := strings.TrimSpace(part); p != "" {
				hops = append(hops, p)
			}
		}
	}
	if len(hops) > maxForwardedHops {
		// Keep the RIGHT-hand end: that is where our own proxies appended, and
		// the walk reads from there.
		hops = hops[len(hops)-maxForwardedHops:]
	}
	return hops
}

// parseHop accepts the shapes proxies actually emit.
//
// Some (Azure Front Door among them) append "ip:port", and IPv6 arrives
// bracketed. Rejecting those would discard the whole header and silently fall
// back to the proxy's own address — the failure this middleware exists to
// prevent, arriving through the front door.
func parseHop(hop string) net.IP {
	if ip := net.ParseIP(hop); ip != nil {
		return ip
	}
	if host, _, err := net.SplitHostPort(hop); err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(strings.Trim(hop, "[]"))
}

func peerIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return net.ParseIP(host)
}

func containsIP(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// parseTrustedProxies turns the CSV config into networks.
//
// Accepts both bare addresses and CIDRs, because an operator writing
// "172.18.0.5" means that host and should not have to know to append /32.
// Unparseable entries are skipped AND reported, so boot continues with a
// narrower trust set rather than failing. That is fail-coarse, not fail-open:
// the cost is rate limits keyed by the proxy's own address, which is loud in
// its own way. The report matters because the symptom otherwise looks like an
// unrelated bug.
func parseTrustedProxies(csv string) ([]*net.IPNet, []string) {
	var (
		nets []*net.IPNet
		bad  []string
	)
	for _, raw := range strings.Split(csv, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(entry); err == nil {
			nets = append(nets, n)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		bad = append(bad, entry)
	}
	return nets, bad
}
