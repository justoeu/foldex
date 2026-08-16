package outboundhttp

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSafeTransportSetsBoundedTimeouts(t *testing.T) {
	tr := NewSafeTransport(3 * time.Second)

	require.NotNil(t, tr.DialContext)
	assert.Nil(t, tr.Proxy, "environment proxies must not bypass target SSRF checks")
	assert.Equal(t, 3*time.Second, tr.TLSHandshakeTimeout)
	assert.Equal(t, 3*time.Second, tr.ResponseHeaderTimeout)
	assert.Equal(t, 30*time.Second, tr.IdleConnTimeout)
	assert.GreaterOrEqual(t, tr.MaxIdleConnsPerHost, 4)
}

func TestNewPublicTransportAlwaysBlocksPrivateAddresses(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	client := &http.Client{Transport: NewPublicTransport(time.Second)}

	_, err := client.Get("http://127.0.0.1:1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing")
}

func TestCheckRemoteAddrBlocksRebinding(t *testing.T) {
	tests := []struct {
		name   string
		strict bool
		peer   net.Addr
		want   string
	}{
		{name: "public permissive", peer: &net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 443}},
		{name: "public strict", strict: true, peer: &net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 443}},
		{name: "IMDS permissive", peer: &net.TCPAddr{IP: net.ParseIP("169.254.169.254"), Port: 80}, want: "IMDS"},
		{name: "IMDS strict", strict: true, peer: &net.TCPAddr{IP: net.ParseIP("169.254.169.254"), Port: 80}, want: "IMDS"},
		{name: "Tencent metadata permissive", peer: &net.TCPAddr{IP: net.ParseIP("169.254.0.23"), Port: 80}, want: "IMDS"},
		{name: "mapped ECS credentials permissive", peer: &net.TCPAddr{IP: net.ParseIP("::ffff:169.254.170.2"), Port: 80}, want: "IMDS"},
		{name: "RFC6598 permissive", peer: &net.TCPAddr{IP: net.ParseIP("100.64.0.1"), Port: 80}, want: "refusing"},
		{name: "RFC6598 strict", strict: true, peer: &net.TCPAddr{IP: net.ParseIP("100.127.255.254"), Port: 80}, want: "refusing"},
		{name: "private strict", strict: true, peer: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80}, want: "refusing peer"},
		{name: "private permissive", peer: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80}},
		{name: "loopback strict", strict: true, peer: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 80}, want: "refusing peer"},
		{name: "IPv6 ULA strict", strict: true, peer: &net.TCPAddr{IP: net.ParseIP("fc00::1"), Port: 80}, want: "refusing peer"},
		{name: "non TCP fails closed", peer: &net.UDPAddr{IP: net.ParseIP("8.8.8.8"), Port: 53}, want: "non-TCP"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkRemoteAddr(tt.strict, tt.peer, "example.com")
			if tt.want == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestSafeDialerBlocksResolvedTargetsBeforeDial(t *testing.T) {
	tests := []struct {
		name   string
		strict bool
		addr   string
		want   string
	}{
		{name: "IMDS permissive", addr: "169.254.169.254:80", want: "IMDS"},
		{name: "private strict", strict: true, addr: "127.0.0.1:80", want: "refusing to dial"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialer := &safeDialer{base: &net.Dialer{Timeout: time.Second}, strict: tt.strict}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err := dialer.DialContext(ctx, "tcp", tt.addr)
			if conn != nil {
				_ = conn.Close()
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestStrictFromEnv(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PREVIEW_STRICT_SSRF", value)
			assert.True(t, strictFromEnv())
		})
	}
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	assert.False(t, strictFromEnv())
	t.Setenv("PREVIEW_STRICT_SSRF", "0")
	assert.False(t, strictFromEnv())
}
