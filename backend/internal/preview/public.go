package preview

import (
	"context"
	"net"
	"net/url"
)

// IsPublicURL reports whether the URL's hostname resolves to ONLY public IP
// addresses. Used as a gate before triggering an expensive screenshot fallback:
// we never screenshot intranet hosts (the page would usually be a login wall
// and the bytes would leak to MinIO unnecessarily).
//
// Unlike the SSRF guard in the dialer, this is strict by design — there is no
// env opt-out. The default preview HTML fetch keeps the permissive behavior
// because intranet links are foldex's primary use case.
func IsPublicURL(ctx context.Context, pageURL string) bool {
	u, err := url.Parse(pageURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if isIMDS(ip) || isPrivateIP(ip) {
			return false
		}
	}
	return true
}
