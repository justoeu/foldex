package ipblock

import (
	"net"
	"net/netip"
	"strings"
)

// Normalize returns the address in Postgres's own spelling, or "" for
// anything inet would reject.
//
// Parsing here rather than letting the cast fail: RemoteAddr is
// "host:port" on a direct bind and a bare host once trustedProxyRealIP has
// rewritten it, and an IPv4-mapped IPv6 address ("::ffff:1.2.3.4") is a second
// spelling of a row that already exists. Both would make the blocklist's
// equality test and the origins aggregate disagree with themselves.
func Normalize(raw string) string {
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	addr, err := netip.ParseAddr(strings.Trim(raw, "[]"))
	if err != nil {
		return ""
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	return addr.String()
}
