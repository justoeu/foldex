package preview

import (
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsPrivateIP(t *testing.T) {
	cases := []struct {
		ip      string
		private bool
	}{
		{"127.0.0.1", true},             // loopback
		{"0.0.0.0", true},               // unspecified
		{"169.254.169.254", true},       // IMDS (also link-local)
		{"169.254.1.2", true},           // link-local
		{"10.1.2.3", true},              // RFC1918
		{"172.16.0.1", true},            // RFC1918
		{"172.31.255.255", true},        // RFC1918 upper
		{"172.32.0.1", false},           // outside RFC1918
		{"192.168.1.1", true},           // RFC1918
		{"::1", true},                   // IPv6 loopback
		{"fc00::1", true},               // IPv6 ULA
		{"fd00::1", true},               // IPv6 ULA
		{"8.8.8.8", false},              // public
		{"1.1.1.1", false},              // public
		{"2001:4860:4860::8888", false}, // public IPv6
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			assert.Equal(t, tc.private, isPrivateIP(ip), "IP %s", tc.ip)
		})
	}
}

func TestIsPrivateIP_NilIsPrivate(t *testing.T) {
	assert.True(t, isPrivateIP(nil), "nil IP must be refused")
}

func TestIsIMDS(t *testing.T) {
	assert.True(t, isIMDS(net.ParseIP("169.254.169.254")))
	assert.False(t, isIMDS(net.ParseIP("169.254.1.1")), "other link-local IPs are not IMDS")
	assert.False(t, isIMDS(net.ParseIP("10.0.0.1")))
	assert.False(t, isIMDS(nil))
}

func TestStrictSSRF_DefaultsToOff(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	assert.False(t, strictSSRF(), "default must be permissive (single-user threat model)")
}

func TestStrictSSRF_Truthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes"} {
		t.Setenv("PREVIEW_STRICT_SSRF", v)
		assert.True(t, strictSSRF(), "value %q must enable strict mode", v)
	}
}

func TestParseHead_TitleAndOG(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head>
  <title>  My Page  </title>
  <meta property="og:image" content="https://cdn.example/cover.png">
  <meta property="og:description" content="A nice page.">
  <link rel="icon" href="/favicon.png">
</head>
<body>...</body>
</html>`
	got := parseHead(strings.NewReader(html))
	assert.Equal(t, "My Page", got.Title)
	assert.Equal(t, "https://cdn.example/cover.png", got.OGImageURL)
	assert.Equal(t, "A nice page.", got.Description)
	assert.Equal(t, "/favicon.png", got.FaviconURL)
}

func TestParseHead_FallbackToOgTitleAndMetaDescription(t *testing.T) {
	html := `<head>
  <meta name="description" content="meta-desc">
  <meta property="og:title" content="OG Title">
</head>`
	got := parseHead(strings.NewReader(html))
	assert.Equal(t, "OG Title", got.Title)
	assert.Equal(t, "meta-desc", got.Description)
}

func TestParseHead_ShortcutIconRel(t *testing.T) {
	html := `<head>
  <link rel="shortcut icon" href="/sicon.ico">
</head>`
	got := parseHead(strings.NewReader(html))
	assert.Equal(t, "/sicon.ico", got.FaviconURL)
}

func TestResolveRelatives(t *testing.T) {
	base, _ := url.Parse("https://example.com/path/page.html")
	in := Result{
		FaviconURL: "/favicon.png",
		OGImageURL: "img/og.jpg",
	}
	got := resolveRelatives(in, base)
	assert.Equal(t, "https://example.com/favicon.png", got.FaviconURL)
	assert.Equal(t, "https://example.com/path/img/og.jpg", got.OGImageURL)
}

func TestResolveOne(t *testing.T) {
	base, _ := url.Parse("https://example.com/a/b")
	assert.Equal(t, "", resolveOne("", base))
	assert.Equal(t, "https://cdn.example/x.png", resolveOne("https://cdn.example/x.png", base))
	assert.Equal(t, "https://example.com/abs", resolveOne("/abs", base))
	assert.Equal(t, "https://example.com/a/rel", resolveOne("rel", base))
}

func TestAttrAndIsVoid(t *testing.T) {
	require.True(t, isVoid("br"))
	require.True(t, isVoid("img"))
	require.False(t, isVoid("div"))
}
