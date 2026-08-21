// Package privatenet answers one question: is this address on a network an
// operator can plausibly own?
//
// It exists as its own package because two callers need the same answer at two
// different moments — config at boot, when it decides whether a plaintext
// broker URL may be accepted, and mailoutbox at dial, when it checks where the
// connection actually landed — and a copy in each would drift.
//
// It is deliberately NOT netpolicy.IsPrivateIP, which answers a different
// question. That one exists to stop an SSRF fetcher from visiting anything
// sensitive, so it treats the whole IANA special-purpose registry as off-limits,
// documentation ranges included: under it 203.0.113.4 (TEST-NET-3) reads as
// private. Correct for a fetcher refusing to go there, wrong for deciding
// whether a credential may cross that link in clear.
package privatenet

import "net"

// IsOperatorNetwork reports whether ip is RFC1918, CGNAT, loopback or
// link-local — the ranges an operator can reasonably claim as "my network".
func IsOperatorNetwork(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || isCGNAT(ip)
}

// isCGNAT covers RFC6598 (100.64.0.0/10), which ip.IsPrivate does not: it is
// carrier-grade NAT space, and Tailscale and similar overlays hand out addresses
// from it for exactly the kind of private link this package is about.
//
// The upper bound is 127, not 128: 100.128.0.0/9 is allocated public space, and
// widening the comparison by one would quietly admit it.
func isCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}
