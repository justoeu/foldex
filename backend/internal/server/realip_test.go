package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func nets(t *testing.T, csv string) []*net.IPNet {
	t.Helper()
	n, bad := parseTrustedProxies(csv)
	if len(bad) > 0 {
		t.Fatalf("unexpected bad entries: %v", bad)
	}
	return n
}

// seen runs the middleware and reports the RemoteAddr the handler observed.
func seen(t *testing.T, trusted []*net.IPNet, remoteAddr string, headers map[string]string) string {
	t.Helper()
	var got string
	h := trustedProxyRealIP(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.RemoteAddr
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

// The default. With nothing configured the header is ignored, so a direct bind
// cannot be talked into believing an attacker-chosen address — which would let
// them both evade their own rate-limit bucket and pin the cost on someone else.
func TestNoTrustedProxiesIgnoresTheHeader(t *testing.T) {
	t.Parallel()
	got := seen(t, nil, "203.0.113.9:5555", map[string]string{
		"X-Forwarded-For": "198.51.100.1",
		"X-Real-IP":       "198.51.100.2",
	})
	if got != "203.0.113.9:5555" {
		t.Fatalf("RemoteAddr = %q, want the untouched peer", got)
	}
}

// An untrusted peer's header is ignored even when OTHER proxies are trusted.
func TestUntrustedPeerHeaderIsIgnored(t *testing.T) {
	t.Parallel()
	got := seen(t, nets(t, "10.0.0.1"), "203.0.113.9:5555", map[string]string{
		"X-Forwarded-For": "198.51.100.1",
	})
	if got != "203.0.113.9:5555" {
		t.Fatalf("RemoteAddr = %q, want the untouched peer", got)
	}
}

func TestTrustedPeerHeaderIsHonoured(t *testing.T) {
	t.Parallel()
	got := seen(t, nets(t, "10.0.0.1"), "10.0.0.1:5555", map[string]string{
		"X-Forwarded-For": "198.51.100.1",
	})
	if got != "198.51.100.1" {
		t.Fatalf("RemoteAddr = %q, want the forwarded client", got)
	}
}

// The subtlety worth a test of its own.
//
// X-Forwarded-For is appended left to right, so the leftmost entry is whatever
// the CLIENT sent — attacker-controlled when there is more than one hop.
// Walking from the right and stopping at the first non-proxy is what yields
// the address the outermost trusted proxy actually observed.
func TestChainedProxiesTakeTheRightmostUntrustedHop(t *testing.T) {
	t.Parallel()
	trusted := nets(t, "10.0.0.0/24")

	got := seen(t, trusted, "10.0.0.1:5555", map[string]string{
		// "1.2.3.4" is what the client claimed; 10.0.0.7 and 10.0.0.1 are our
		// own proxies. The real client, as seen by the innermost proxy we
		// trust, is 198.51.100.1.
		"X-Forwarded-For": "1.2.3.4, 198.51.100.1, 10.0.0.7",
	})
	if got != "198.51.100.1" {
		t.Fatalf("RemoteAddr = %q, want 198.51.100.1 — a spoofed leftmost entry was believed", got)
	}
}

// If every hop is one of our own proxies the real client never appeared, so
// there is nothing to believe.
func TestAllHopsTrustedFallsBackToThePeer(t *testing.T) {
	t.Parallel()
	got := seen(t, nets(t, "10.0.0.0/24"), "10.0.0.1:5555", map[string]string{
		"X-Forwarded-For": "10.0.0.7, 10.0.0.8",
	})
	if got != "10.0.0.1:5555" {
		t.Fatalf("RemoteAddr = %q, want the peer", got)
	}
}

// Garbage in a position we EXAMINE means the chain is not the shape we expect,
// so falling back beats guessing — the guess is what an attacker would steer.
func TestMalformedHopInAnExaminedPositionFallsBackToThePeer(t *testing.T) {
	t.Parallel()
	got := seen(t, nets(t, "10.0.0.1"), "10.0.0.1:5555", map[string]string{
		// The RIGHTMOST entry is what our own proxy appended. Garbage there
		// means we cannot tell which hop is which.
		"X-Forwarded-For": "198.51.100.1, not-an-ip",
	})
	if got != "10.0.0.1:5555" {
		t.Fatalf("RemoteAddr = %q, want the peer", got)
	}
}

// Garbage to the LEFT of the hop we pick is ignored, and that is correct: the
// rightmost untrusted entry is the address our proxy actually observed, and
// everything left of it is whatever the client chose to send. Believing the
// peer instead would discard a good answer because of noise the attacker
// controls — handing them a way to opt out of being identified.
func TestMalformedHopLeftOfTheChosenOneIsIgnored(t *testing.T) {
	t.Parallel()
	got := seen(t, nets(t, "10.0.0.1"), "10.0.0.1:5555", map[string]string{
		"X-Forwarded-For": "not-an-ip, 198.51.100.1",
	})
	if got != "198.51.100.1" {
		t.Fatalf("RemoteAddr = %q, want the observed client", got)
	}
}

// X-Real-IP is a single value and only consulted when XFF is absent — XFF
// carries the chain, and preferring the header with less information would
// discard it.
func TestXRealIPIsOnlyUsedWithoutXFF(t *testing.T) {
	t.Parallel()
	trusted := nets(t, "10.0.0.1")

	if got := seen(t, trusted, "10.0.0.1:5555", map[string]string{
		"X-Real-IP": "198.51.100.5",
	}); got != "198.51.100.5" {
		t.Fatalf("X-Real-IP alone = %q", got)
	}
	if got := seen(t, trusted, "10.0.0.1:5555", map[string]string{
		"X-Forwarded-For": "198.51.100.1",
		"X-Real-IP":       "198.51.100.5",
	}); got != "198.51.100.1" {
		t.Fatalf("with both present = %q, want the XFF client", got)
	}
}

func TestIPv6PeerAndClient(t *testing.T) {
	t.Parallel()
	got := seen(t, nets(t, "fd00::/8"), "[fd00::1]:5555", map[string]string{
		"X-Forwarded-For": "2001:db8::9",
	})
	if got != "2001:db8::9" {
		t.Fatalf("RemoteAddr = %q, want the IPv6 client", got)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	t.Parallel()

	// A bare address means that host — an operator should not have to know to
	// append /32.
	n, bad := parseTrustedProxies("10.0.0.1, 172.18.0.0/16 , fd00::1,")
	if len(bad) != 0 {
		t.Fatalf("unexpected bad entries: %v", bad)
	}
	if len(n) != 3 {
		t.Fatalf("parsed %d networks, want 3", len(n))
	}
	if !containsIP(n, net.ParseIP("10.0.0.1")) || containsIP(n, net.ParseIP("10.0.0.2")) {
		t.Error("a bare IPv4 address should match exactly itself")
	}
	if !containsIP(n, net.ParseIP("172.18.4.9")) {
		t.Error("the CIDR should match a member")
	}

	// Unparseable entries are REPORTED, not skipped: silently dropping one
	// leaves the operator believing a proxy is trusted when it is not.
	n, bad = parseTrustedProxies("10.0.0.1, nonsense, 999.999.999.999")
	if len(n) != 1 {
		t.Fatalf("parsed %d networks, want 1", len(n))
	}
	if len(bad) != 2 {
		t.Fatalf("reported %v, want both unparseable entries", bad)
	}
}

func TestParseTrustedProxiesEmpty(t *testing.T) {
	t.Parallel()
	n, bad := parseTrustedProxies("")
	if len(n) != 0 || len(bad) != 0 {
		t.Fatalf("empty config should yield nothing, got %v / %v", n, bad)
	}
}

// A proxy that emits a SECOND X-Forwarded-For line instead of appending to the
// first: Header.Get would return only the client-supplied one and the walk
// would operate on attacker-controlled data.
func TestMultipleForwardedForHeaderLines(t *testing.T) {
	t.Parallel()
	var got string
	h := trustedProxyRealIP(nets(t, "10.0.0.0/24"))(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) { got = r.RemoteAddr }))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:5555"
	req.Header.Add("X-Forwarded-For", "1.2.3.4")      // what the client sent
	req.Header.Add("X-Forwarded-For", "198.51.100.1") // what our proxy added
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got != "198.51.100.1" {
		t.Fatalf("RemoteAddr = %q, want the hop our proxy appended", got)
	}
}

// Azure Front Door and friends append "ip:port"; IPv6 arrives bracketed.
// Rejecting those would discard the header and silently fall back to the
// proxy's own address — the exact failure this middleware exists to prevent.
func TestHopsWithPortsAndBrackets(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"203.0.113.5:1234":  "203.0.113.5",
		"[2001:db8::1]:443": "2001:db8::1",
		"[2001:db8::2]":     "2001:db8::2",
	}
	for hop, want := range cases {
		got := seen(t, nets(t, "10.0.0.1"), "10.0.0.1:5555",
			map[string]string{"X-Forwarded-For": hop})
		if got != want {
			t.Errorf("hop %q → %q, want %q", hop, got, want)
		}
	}
}

// A megabyte of commas must not turn into half a million parsed entries per
// request. Only a trusted peer can reach this, but a misbehaving proxy is
// exactly the case worth bounding.
func TestAbsurdlyLongChainIsBounded(t *testing.T) {
	t.Parallel()
	hops := make([]string, 0, 5000)
	for range 5000 {
		hops = append(hops, "10.0.0.9")
	}
	// The real client sits at the far right, inside the retained window.
	hops = append(hops, "198.51.100.1")

	got := seen(t, nets(t, "10.0.0.0/24"), "10.0.0.1:5555",
		map[string]string{"X-Forwarded-For": strings.Join(hops, ",")})
	if got != "198.51.100.1" {
		t.Fatalf("RemoteAddr = %q, want the rightmost untrusted hop", got)
	}
}

func TestEmptyAndWhitespaceHopsAreIgnored(t *testing.T) {
	t.Parallel()
	got := seen(t, nets(t, "10.0.0.1"), "10.0.0.1:5555",
		map[string]string{"X-Forwarded-For": " , 198.51.100.1 , "})
	if got != "198.51.100.1" {
		t.Fatalf("RemoteAddr = %q", got)
	}
}

// An X-Real-IP that does not parse must leave RemoteAddr alone rather than
// blanking it — the peer address is still the best thing we know.
func TestUnparseableXRealIPKeepsThePeer(t *testing.T) {
	t.Parallel()
	got := seen(t, nets(t, "10.0.0.1"), "10.0.0.1:5555",
		map[string]string{"X-Real-IP": "not-an-ip"})
	if got != "10.0.0.1:5555" {
		t.Fatalf("RemoteAddr = %q, want the peer", got)
	}
}

// A trusted peer that sends NEITHER header: there is nothing to believe, so the
// peer stands. This is the ordinary case for a request that reached the backend
// directly on the compose network.
func TestTrustedPeerWithNoHeadersKeepsThePeer(t *testing.T) {
	t.Parallel()
	got := seen(t, nets(t, "10.0.0.1"), "10.0.0.1:5555", nil)
	if got != "10.0.0.1:5555" {
		t.Fatalf("RemoteAddr = %q, want the peer", got)
	}
}

// RemoteAddr without a port — which is what our own middleware writes after it
// rewrites, so a second pass through must not lose the address.
func TestPeerWithoutAPortIsParsed(t *testing.T) {
	t.Parallel()
	got := seen(t, nets(t, "10.0.0.1"), "10.0.0.1",
		map[string]string{"X-Forwarded-For": "198.51.100.1"})
	if got != "198.51.100.1" {
		t.Fatalf("RemoteAddr = %q — a portless peer was not recognised as trusted", got)
	}
}

func TestIsLoopbackBind(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{"127.0.0.1", "127.0.0.1:9089", "::1", "[::1]:9089", "localhost", "localhost:9089", ""} {
		if !isLoopbackBind(addr) {
			t.Errorf("isLoopbackBind(%q) = false", addr)
		}
	}
	// 0.0.0.0 is the compose default and is exactly the case that must warn.
	for _, addr := range []string{"0.0.0.0", "0.0.0.0:9089", "192.168.1.10", "[::]:9089"} {
		if isLoopbackBind(addr) {
			t.Errorf("isLoopbackBind(%q) = true — a network-reachable bind was treated as local", addr)
		}
	}
}
