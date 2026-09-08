package outboundhttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"foldex/internal/pkg/netpolicy"
)

// ErrSSRF is the dialer sentinel for refused destinations. Wrappers must keep
// it via %w so callers classify with errors.Is instead of Error() text.
var ErrSSRF = errors.New("ssrf")

// NewSafeTransport returns an HTTP transport that checks resolved addresses
// before dialing and the connected peer afterward to prevent DNS rebinding.
func NewSafeTransport(timeout time.Duration) *http.Transport {
	return newSafeTransport(timeout, strictFromEnv())
}

// NewPublicTransport always rejects private and special-use destinations.
// Use it for user-controlled capabilities that must leave the instance.
func NewPublicTransport(timeout time.Duration) *http.Transport {
	return newSafeTransport(timeout, true)
}

func newSafeTransport(timeout time.Duration, strict bool) *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           (&safeDialer{base: &net.Dialer{Timeout: timeout}, strict: strict}).DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
	}
}

type safeDialer struct {
	base   *net.Dialer
	strict bool // snapshot of PREVIEW_STRICT_SSRF at transport construction
}

func (d *safeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	// This lookup fails fast for denied targets. The peer check after DialContext
	// is still required because the dialer's own lookup may see a rebound answer.
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if netpolicy.IsMetadataIP(ip) {
			return nil, fmt.Errorf("%w: refusing IMDS endpoint %s", ErrSSRF, ip)
		}
		if netpolicy.IsAlwaysDeniedIP(ip) {
			return nil, fmt.Errorf("%w: refusing special-use endpoint %s", ErrSSRF, ip)
		}
		if d.strict && netpolicy.IsPrivateIP(ip) {
			return nil, fmt.Errorf("%w: refusing to dial %s (%s)", ErrSSRF, host, ip)
		}
	}
	conn, err := d.base.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if err := checkRemoteAddr(d.strict, conn.RemoteAddr(), host); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// HTTP transports dial TCP, so an unexpected address type must fail closed or
// the post-dial DNS-rebinding defense would be bypassed.
func checkRemoteAddr(strict bool, addr net.Addr, host string) error {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("%w: non-TCP remote addr %T - refusing", ErrSSRF, addr)
	}
	if netpolicy.IsMetadataIP(tcp.IP) {
		return fmt.Errorf("%w: refusing IMDS endpoint %s (post-dial)", ErrSSRF, tcp.IP)
	}
	if netpolicy.IsAlwaysDeniedIP(tcp.IP) {
		return fmt.Errorf("%w: refusing special-use endpoint %s (post-dial)", ErrSSRF, tcp.IP)
	}
	if strict && netpolicy.IsPrivateIP(tcp.IP) {
		return fmt.Errorf("%w: refusing peer %s for host %s (post-dial)", ErrSSRF, tcp.IP, host)
	}
	return nil
}

// Strict mode additionally blocks private and special-purpose ranges. The
// default permits ordinary intranet targets while always denying metadata and
// shared address space.
func strictFromEnv() bool {
	switch os.Getenv("PREVIEW_STRICT_SSRF") {
	case "1", "true", "TRUE", "yes":
		return true
	default:
		return false
	}
}
