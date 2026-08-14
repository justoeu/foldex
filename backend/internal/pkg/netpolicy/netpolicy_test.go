package netpolicy

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsAlwaysDeniedIP(t *testing.T) {
	for _, raw := range []string{
		"100.64.0.0",
		"100.127.255.255",
		"100.100.100.200",
		"169.254.0.23",
		"169.254.169.254",
		"169.254.170.2",
		"169.254.170.23",
		"fd00:ec2::254",
		"fd00:ec2::23",
		"::ffff:169.254.0.23",
		"::ffff:169.254.170.2",
	} {
		assert.True(t, IsAlwaysDeniedIP(net.ParseIP(raw)), raw)
	}
	for _, raw := range []string{
		"100.63.255.255",
		"100.128.0.0",
		"10.0.0.1",
		"169.254.0.22",
		"169.254.0.24",
		"169.254.168.255",
		"169.254.171.0",
		"8.8.8.8",
	} {
		assert.False(t, IsAlwaysDeniedIP(net.ParseIP(raw)), raw)
	}
	assert.True(t, IsAlwaysDeniedIP(nil))
}

func TestStrictSpecialUseRegistryAndBoundaries(t *testing.T) {
	expected := []string{
		"0.0.0.0/8",
		"10.0.0.0/8",
		"100.64.0.0/10",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"172.16.0.0/12",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"192.31.196.0/24",
		"192.52.193.0/24",
		"192.88.99.0/24",
		"192.168.0.0/16",
		"192.175.48.0/24",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"240.0.0.0/4",
		"::/128",
		"::1/128",
		"::ffff:0.0.0.0/96",
		"64:ff9b::/96",
		"64:ff9b:1::/48",
		"100::/64",
		"100:0:0:1::/64",
		"2001::/23",
		"2001:db8::/32",
		"2002::/16",
		"2620:4f:8000::/48",
		"3fff::/20",
		"5f00::/16",
		"fc00::/7",
		"fe80::/10",
	}
	require.Len(t, strictSpecialUsePrefixes, len(expected))
	for i, raw := range expected {
		prefix := strictSpecialUsePrefixes[i]
		assert.Equal(t, raw, prefix.String())
		assert.True(t, IsPrivateIP(net.IP(prefix.Addr().AsSlice())), "first address of %s", raw)
		assert.True(t, IsPrivateIP(net.IP(lastAddress(prefix).AsSlice())), "last address of %s", raw)
	}

	for _, raw := range []string{"1.0.0.0", "8.8.8.8", "93.184.216.34", "2001:4860:4860::8888"} {
		assert.False(t, IsPrivateIP(net.ParseIP(raw)), raw)
	}
	assert.False(t, IsPrivateIP(net.ParseIP("::ffff:8.8.8.8")), "public IPv4-mapped addresses remain public")
	assert.True(t, IsPrivateIP(net.ParseIP("::ffff:127.0.0.1")), "IPv4-mapped loopback must be normalized")
}

func lastAddress(prefix netip.Prefix) netip.Addr {
	addr := prefix.Addr()
	bits := addr.BitLen()
	bytes := addr.AsSlice()
	for bit := prefix.Bits(); bit < bits; bit++ {
		bytes[bit/8] |= 1 << (7 - bit%8)
	}
	last, ok := netip.AddrFromSlice(bytes)
	if !ok {
		panic("invalid address bytes")
	}
	return last
}

func TestIsPublicURLRejectsRFC6598(t *testing.T) {
	assert.False(t, IsPublicURL(context.Background(), "http://100.64.0.1/"))
	assert.False(t, IsPublicURL(context.Background(), "http://100.127.255.254/"))
}
