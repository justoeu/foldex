package screenshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"foldex/internal/pkg/netpolicy"
)

// ErrEgressBlocked means Chromium attempted to reach an address outside the
// screenshot egress policy. Any occurrence invalidates the whole capture.
var ErrEgressBlocked = errors.New("screenshot: egress blocked")

const (
	proxyDialTimeout          = 10 * time.Second
	proxyCleanupTimeout       = 2 * time.Second
	maxProxyConnections       = 32
	maxProxyTunnels           = 32
	maxProxyRequests          = 256
	maxProxyBytes       int64 = 32 << 20
)

var errProxyBudgetExceeded = errors.New("screenshot: proxy resource budget exceeded")

type dialContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type captureProxy struct {
	listener     net.Listener
	server       *http.Server
	transport    *http.Transport
	baseDial     dialContextFunc
	blocked      atomic.Bool
	tunnelSem    chan struct{}
	connSem      chan struct{}
	requests     atomic.Int64
	bytes        atomic.Int64
	requestLimit int64
	byteLimit    int64

	tunnelMu sync.Mutex
	tunnels  map[*proxyTunnel]struct{}
	closeMu  sync.Mutex
	closed   bool
}

type proxyTunnel struct {
	client   net.Conn
	upstream net.Conn
}

func newCaptureProxy() (*captureProxy, error) {
	dialer := &net.Dialer{Timeout: proxyDialTimeout, KeepAlive: 30 * time.Second}
	return newCaptureProxyWithDial(dialer.DialContext)
}

func newCaptureProxyWithDial(baseDial dialContextFunc) (*captureProxy, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("screenshot: start egress proxy: %w", err)
	}
	p := &captureProxy{
		listener:     listener,
		baseDial:     baseDial,
		tunnelSem:    make(chan struct{}, maxProxyTunnels),
		connSem:      make(chan struct{}, maxProxyConnections),
		requestLimit: maxProxyRequests,
		byteLimit:    maxProxyBytes,
		tunnels:      make(map[*proxyTunnel]struct{}),
	}
	p.transport = &http.Transport{
		Proxy:                 nil,
		DialContext:           p.dialContext,
		ForceAttemptHTTP2:     false,
		TLSHandshakeTimeout:   proxyDialTimeout,
		ResponseHeaderTimeout: proxyDialTimeout,
	}
	p.server = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       15 * time.Second,
	}
	go func() {
		if err := p.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.markBlocked()
		}
	}()
	return p, nil
}

func (p *captureProxy) URL() string {
	return "http://" + p.listener.Addr().String()
}

func (p *captureProxy) Address() string {
	return p.listener.Addr().String()
}

func (p *captureProxy) Blocked() bool {
	return p.blocked.Load()
}

func (p *captureProxy) resetBudgets() {
	p.transport.CloseIdleConnections()
	p.requests.Store(0)
	p.bytes.Store(0)
}

func (p *captureProxy) Close() {
	if p == nil {
		return
	}
	p.closeMu.Lock()
	if p.closed {
		p.closeMu.Unlock()
		return
	}
	p.closed = true
	p.closeMu.Unlock()

	p.closeTunnels()
	p.transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), proxyCleanupTimeout)
	defer cancel()
	if err := p.server.Shutdown(ctx); err != nil {
		_ = p.server.Close()
	}
}

func (p *captureProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.requests.Add(1) > p.requestLimit {
		p.exceedBudget(w)
		return
	}
	if r.Method == http.MethodConnect {
		p.serveConnect(w, r)
		return
	}
	if r.URL == nil || (r.URL.Scheme != "http" && r.URL.Scheme != "https") || r.URL.Host == "" {
		p.refuse(w, fmt.Errorf("%w: invalid proxy target", ErrEgressBlocked))
		return
	}

	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header = r.Header.Clone()
	removeHopHeaders(out.Header)
	out.Header.Del("Proxy-Authorization")
	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		p.writeDialError(w, err)
		return
	}
	defer resp.Body.Close()
	removeHopHeaders(resp.Header)
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *captureProxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	if _, _, err := net.SplitHostPort(r.Host); err != nil {
		p.refuse(w, fmt.Errorf("%w: invalid CONNECT target", ErrEgressBlocked))
		return
	}
	select {
	case p.tunnelSem <- struct{}{}:
		defer func() { <-p.tunnelSem }()
	default:
		p.exceedBudget(w)
		return
	}
	upstream, err := p.dialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		p.writeDialError(w, err)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "proxy unavailable", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	if err := buffered.Flush(); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}

	tunnel := &proxyTunnel{client: client, upstream: upstream}
	if !p.trackTunnel(tunnel) {
		return
	}
	defer p.untrackTunnel(tunnel)
	done := make(chan struct{}, 2)
	go proxyCopy(upstream, buffered, done)
	go proxyCopy(client, upstream, done)
	<-done
	_ = client.Close()
	_ = upstream.Close()
	<-done
}

func proxyCopy(dst io.Writer, src io.Reader, done chan<- struct{}) {
	_, _ = io.Copy(dst, src)
	done <- struct{}{}
}

func (p *captureProxy) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	select {
	case p.connSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		p.markBlocked()
		return nil, errProxyBudgetExceeded
	}
	conn, err := strictDialContext(ctx, p.baseDial, network, addr)
	if err != nil {
		<-p.connSem
	}
	if errors.Is(err, ErrEgressBlocked) {
		p.markBlocked()
	}
	if err != nil {
		return nil, err
	}
	return &proxyBudgetConn{Conn: conn, proxy: p}, nil
}

func strictDialContext(ctx context.Context, baseDial dialContextFunc, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid dial target", ErrEgressBlocked)
	}
	ips, err := resolveTarget(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if isBlockedEgressIP(ip) {
			return nil, fmt.Errorf("%w: refusing target %s", ErrEgressBlocked, ip)
		}
	}

	var dialErr error
	for _, ip := range ips {
		conn, err := baseDial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err != nil {
			dialErr = err
			continue
		}
		peer, ok := conn.RemoteAddr().(*net.TCPAddr)
		if !ok || isBlockedEgressIP(peer.IP) {
			_ = conn.Close()
			return nil, fmt.Errorf("%w: refusing connected peer", ErrEgressBlocked)
		}
		return conn, nil
	}
	return nil, dialErr
}

func resolveTarget(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("screenshot: target resolved without addresses")
	}
	return ips, nil
}

func isBlockedEgressIP(ip net.IP) bool {
	return netpolicy.IsPrivateIP(ip)
}

func (p *captureProxy) refuse(w http.ResponseWriter, err error) {
	p.markBlocked()
	p.writeDialError(w, err)
}

func (p *captureProxy) markBlocked() {
	p.blocked.Store(true)
}

func (p *captureProxy) exceedBudget(w http.ResponseWriter) {
	p.markBlocked()
	p.closeTunnels()
	p.transport.CloseIdleConnections()
	http.Error(w, "screenshot proxy resource budget exceeded", http.StatusServiceUnavailable)
}

func (p *captureProxy) reserveBytes(n int) error {
	if n <= 0 {
		return nil
	}
	if p.bytes.Add(int64(n)) <= p.byteLimit {
		return nil
	}
	p.markBlocked()
	p.closeTunnels()
	p.transport.CloseIdleConnections()
	return errProxyBudgetExceeded
}

type proxyBudgetConn struct {
	net.Conn
	proxy *captureProxy
	once  sync.Once
}

func (c *proxyBudgetConn) Read(data []byte) (int, error) {
	n, err := c.Conn.Read(data)
	if budgetErr := c.proxy.reserveBytes(n); budgetErr != nil {
		return 0, budgetErr
	}
	return n, err
}

func (c *proxyBudgetConn) Write(data []byte) (int, error) {
	if err := c.proxy.reserveBytes(len(data)); err != nil {
		return 0, err
	}
	return c.Conn.Write(data)
}

func (c *proxyBudgetConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { <-c.proxy.connSem })
	return err
}

func (p *captureProxy) writeDialError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrEgressBlocked) {
		http.Error(w, "blocked by screenshot egress policy", http.StatusForbidden)
		return
	}
	http.Error(w, "upstream unavailable", http.StatusBadGateway)
}

func (p *captureProxy) trackTunnel(tunnel *proxyTunnel) bool {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.closed {
		_ = tunnel.client.Close()
		_ = tunnel.upstream.Close()
		return false
	}
	p.tunnelMu.Lock()
	// Refused once any budget has been enforced, for the same reason the
	// closed check above exists — and this half was missing.
	//
	// A tunnel holds its semaphore slot from the moment CONNECT is admitted,
	// but only enters this map AFTER the 200 has been flushed. The client can
	// see that 200 and open the next tunnel while this handler is still between
	// the two, so `closeTunnels` walks a map the tunnel is not in yet: it
	// survives the teardown, its `io.Copy` blocks on a peer nobody closed, and
	// the slot is never returned. In production that is a tunnel still relaying
	// bytes after the capture was invalidated, which is precisely what the
	// budget exists to stop.
	//
	// markBlocked runs BEFORE closeTunnels, and closeTunnels holds this mutex
	// while it walks, so the two orderings are both covered: register first and
	// closeTunnels closes it, register after and it closes itself here.
	if p.blocked.Load() {
		p.tunnelMu.Unlock()
		_ = tunnel.client.Close()
		_ = tunnel.upstream.Close()
		return false
	}
	p.tunnels[tunnel] = struct{}{}
	p.tunnelMu.Unlock()
	return true
}

func (p *captureProxy) untrackTunnel(tunnel *proxyTunnel) {
	p.tunnelMu.Lock()
	delete(p.tunnels, tunnel)
	p.tunnelMu.Unlock()
}

func (p *captureProxy) closeTunnels() {
	p.tunnelMu.Lock()
	for tunnel := range p.tunnels {
		_ = tunnel.client.Close()
		_ = tunnel.upstream.Close()
	}
	p.tunnelMu.Unlock()
}

func removeHopHeaders(header http.Header) {
	for _, token := range strings.Split(header.Get("Connection"), ",") {
		if key := strings.TrimSpace(token); key != "" {
			header.Del(key)
		}
	}
	for _, key := range []string{
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		header.Del(key)
	}
}
