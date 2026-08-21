package privatenet

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// Both callers — the boot check and the dial check — decide whether a credential
// may cross a link in clear based on this answer, so the boundaries are pinned
// on BOTH sides. An interior-only table would leave a widened bound reading as
// correct: 100.128.0.0/9 is allocated public space one comparison away.
func TestIsOperatorNetwork(t *testing.T) {
	for _, tc := range []struct {
		ip    string
		allow bool
		why   string
	}{
		{"10.0.0.1", true, "RFC1918 /8"},
		{"172.16.0.1", true, "RFC1918 /12 lower edge"},
		{"172.31.255.255", true, "RFC1918 /12 upper edge"},
		{"172.32.0.1", false, "just past RFC1918 /12"},
		{"192.168.1.1", true, "RFC1918 /16"},
		{"100.64.0.0", true, "CGNAT lower edge"},
		{"100.127.255.255", true, "CGNAT upper edge"},
		{"100.63.255.255", false, "one below CGNAT"},
		{"100.128.0.0", false, "one above CGNAT — allocated PUBLIC space"},
		{"127.0.0.1", true, "loopback"},
		{"127.0.0.2", true, "loopback beyond the literal isLocalBind knows"},
		{"169.254.1.1", true, "link-local: cannot leave the local link"},
		{"fd00::1", true, "IPv6 ULA"},
		{"fc00::1", true, "IPv6 ULA lower edge of fc00::/7"},
		{"fbff::1", false, "just below fc00::/7"},
		{"fe80::1", true, "IPv6 link-local"},
		{"::1", true, "IPv6 loopback"},
		{"::ffff:192.168.1.1", true, "IPv4-mapped private"},
		{"::ffff:8.8.8.8", false, "IPv4-mapped public"},
		{"8.8.8.8", false, "public"},
		{"203.0.113.4", false, "TEST-NET-3 — netpolicy.IsPrivateIP would call this private"},
		{"2001:db8::1", false, "IPv6 documentation range"},
		{"0.0.0.0", false, "unspecified"},
		{"255.255.255.255", false, "broadcast"},
	} {
		ip := net.ParseIP(tc.ip)
		require.NotNil(t, ip, "test data %q must parse", tc.ip)
		require.Equal(t, tc.allow, IsOperatorNetwork(ip), "%s (%s)", tc.ip, tc.why)
	}
}
