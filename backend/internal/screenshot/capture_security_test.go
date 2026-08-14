package screenshot

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCapture_IsolatesAndDisposesEveryContext(t *testing.T) {
	pool := NewPool()
	var configs []browserContextProxy
	var disposed []proto.BrowserBrowserContextID
	pool.createBrowserContext = func(_ context.Context, _ *rod.Browser, proxy browserContextProxy) (proto.BrowserBrowserContextID, error) {
		configs = append(configs, proxy)
		return proto.BrowserBrowserContextID(fmt.Sprintf("context-%d", len(configs))), nil
	}
	pool.disposeBrowserContext = func(ctx context.Context, _ *rod.Browser, id proto.BrowserBrowserContextID) error {
		require.NoError(t, ctx.Err(), "cleanup must not inherit a cancelled capture context")
		_, hasDeadline := ctx.Deadline()
		assert.True(t, hasDeadline, "context disposal must be bounded")
		disposed = append(disposed, id)
		return nil
	}
	pool.captureBrowserContext = func(_ context.Context, _ *rod.Browser, _ proto.BrowserBrowserContextID, _ string) ([]byte, error) {
		return []byte("png"), nil
	}

	browser := &rod.Browser{}
	pool.current = &pooledBrowser{browser: browser, stopped: make(chan struct{})}
	pool.generations[pool.current] = struct{}{}
	for range 2 {
		png, err := pool.Capture(context.Background(), "http://93.184.216.34/page")
		require.NoError(t, err)
		assert.Equal(t, []byte("png"), png)
	}

	require.Len(t, configs, 2)
	assert.NotEqual(t, configs[0].Server, configs[1].Server, "each capture needs its own proxy")
	for _, config := range configs {
		assert.Equal(t, "<-loopback>", config.BypassList, "Chromium must not bypass the proxy for loopback")
		proxyURL, err := url.Parse("http://" + config.Server)
		require.NoError(t, err)
		assert.Equal(t, "http", proxyURL.Scheme)
		assert.Equal(t, "127.0.0.1", proxyURL.Hostname())
	}
	assert.Equal(t, []proto.BrowserBrowserContextID{"context-1", "context-2"}, disposed)
}

func TestCaptureWithBrowser_DisposesContextAfterCancellation(t *testing.T) {
	pool := NewPool()
	ctx, cancel := context.WithCancel(context.Background())
	pool.createBrowserContext = func(_ context.Context, _ *rod.Browser, _ browserContextProxy) (proto.BrowserBrowserContextID, error) {
		return "cancelled-context", nil
	}
	pool.captureBrowserContext = func(callCtx context.Context, _ *rod.Browser, _ proto.BrowserBrowserContextID, _ string) ([]byte, error) {
		cancel()
		<-callCtx.Done()
		return nil, callCtx.Err()
	}
	disposed := false
	pool.disposeBrowserContext = func(cleanupCtx context.Context, _ *rod.Browser, id proto.BrowserBrowserContextID) error {
		disposed = true
		assert.Equal(t, proto.BrowserBrowserContextID("cancelled-context"), id)
		assert.NoError(t, cleanupCtx.Err(), "cancelled caller context must not cancel disposal")
		deadline, ok := cleanupCtx.Deadline()
		assert.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), pool.contextCleanupTimeout)
		return nil
	}

	_, err := pool.captureWithBrowser(ctx, &rod.Browser{}, nil, "http://93.184.216.34/page")
	require.ErrorIs(t, err, context.Canceled)
	assert.True(t, disposed)
}

func TestCapture_DisposeFailureKillsAndCleansLauncherExactlyOnce(t *testing.T) {
	pool := NewPool()
	fake := &fakeBrowserLauncher{pid: 4244}

	pool.createBrowserContext = func(context.Context, *rod.Browser, browserContextProxy) (proto.BrowserBrowserContextID, error) {
		return "leaked-context", nil
	}
	pool.captureBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID, string) ([]byte, error) {
		return []byte("discarded"), nil
	}
	pool.disposeBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID) error {
		return context.DeadlineExceeded
	}

	generation := &pooledBrowser{browser: &rod.Browser{}, launcher: fake, pid: fake.pid, stopped: make(chan struct{})}
	pool.current = generation
	pool.generations[generation] = struct{}{}
	_, err := pool.Capture(context.Background(), "http://93.184.216.34/page")
	require.Error(t, err)
	assert.ErrorIs(t, err, errBrowserContextState)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	awaitSignal(t, generation.stopped, "dispose failure teardown did not finish")
	assert.Equal(t, int64(1), fake.kills.Load())
	assert.Equal(t, int64(1), fake.cleanups.Load())
}

func TestCapture_CreateContextFailureKillsAndCleansLauncherExactlyOnce(t *testing.T) {
	pool := NewPool()
	fake := &fakeBrowserLauncher{pid: 4245}

	pool.createBrowserContext = func(context.Context, *rod.Browser, browserContextProxy) (proto.BrowserBrowserContextID, error) {
		return "", errors.New("create failed")
	}
	generation := &pooledBrowser{browser: &rod.Browser{}, launcher: fake, pid: fake.pid, stopped: make(chan struct{})}
	pool.current = generation
	pool.generations[generation] = struct{}{}

	_, err := pool.Capture(context.Background(), "http://93.184.216.34/page")
	require.ErrorIs(t, err, errBrowserContextState)
	awaitSignal(t, generation.stopped, "create failure teardown did not finish")
	assert.Equal(t, int64(1), fake.kills.Load())
	assert.Equal(t, int64(1), fake.cleanups.Load())
}

func TestCapture_ProcessProxyBlockInvalidatesCaptureAndGeneration(t *testing.T) {
	pool := NewPool()
	fake := &fakeBrowserLauncher{pid: 4246}
	processProxy, err := newCaptureProxy()
	require.NoError(t, err)
	t.Cleanup(processProxy.Close)

	pool.createBrowserContext = func(context.Context, *rod.Browser, browserContextProxy) (proto.BrowserBrowserContextID, error) {
		return "process-proxy-context", nil
	}
	pool.disposeBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID) error { return nil }
	pool.captureBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID, string) ([]byte, error) {
		processProxy.markBlocked()
		return []byte("discarded"), nil
	}
	generation := &pooledBrowser{browser: &rod.Browser{}, launcher: fake, proxy: processProxy, pid: fake.pid, stopped: make(chan struct{})}
	pool.current = generation
	pool.generations[generation] = struct{}{}

	png, err := pool.Capture(context.Background(), "http://93.184.216.34/page")
	require.ErrorIs(t, err, ErrEgressBlocked)
	assert.Nil(t, png)
	awaitSignal(t, generation.stopped, "blocked generation teardown did not finish")
	assert.Equal(t, int64(1), fake.kills.Load())
	assert.Equal(t, int64(1), fake.cleanups.Load())
}

func TestCaptureWithBrowser_BlockedSubresourceInvalidatesCapture(t *testing.T) {
	pool := NewPool()
	var proxyConfig browserContextProxy
	pool.createBrowserContext = func(_ context.Context, _ *rod.Browser, proxy browserContextProxy) (proto.BrowserBrowserContextID, error) {
		proxyConfig = proxy
		return "blocked-context", nil
	}
	pool.disposeBrowserContext = func(_ context.Context, _ *rod.Browser, _ proto.BrowserBrowserContextID) error { return nil }
	pool.captureBrowserContext = func(_ context.Context, _ *rod.Browser, _ proto.BrowserBrowserContextID, _ string) ([]byte, error) {
		proxyURL, err := url.Parse("http://" + proxyConfig.Server)
		require.NoError(t, err)
		client := &http.Client{
			Timeout: time.Second,
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}
		resp, err := client.Get("http://10.0.0.8/private-pixel.png")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		return []byte("must-be-discarded"), nil
	}

	png, err := pool.captureWithBrowser(context.Background(), &rod.Browser{}, nil, "http://93.184.216.34/page")
	require.ErrorIs(t, err, ErrEgressBlocked)
	assert.Nil(t, png, "a screenshot with any blocked request must be discarded")
}

func TestCaptureProxy_BlocksPrivateRedirect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/private", http.StatusFound)
	}))
	t.Cleanup(upstream.Close)

	var mu sync.Mutex
	var dials []string
	baseDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dials = append(dials, addr)
		mu.Unlock()
		conn, err := (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
		if err != nil {
			return nil, err
		}
		return &reportedRemoteConn{
			Conn: conn,
			addr: &net.TCPAddr{IP: net.ParseIP("93.184.216.34"), Port: 80},
		}, nil
	}

	proxy, err := newCaptureProxyWithDial(baseDial)
	require.NoError(t, err)
	t.Cleanup(proxy.Close)
	client := proxyHTTPClient(t, proxy.URL())

	resp, err := client.Get("http://93.184.216.34/start")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.True(t, proxy.Blocked())
	mu.Lock()
	assert.Equal(t, []string{"93.184.216.34:80"}, dials, "private redirect must be rejected before dial")
	mu.Unlock()
}

func TestStrictDialContextDialsValidatedIP(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	t.Cleanup(func() { _ = serverSide.Close() })
	var dialed string

	conn, err := strictDialContext(context.Background(), func(_ context.Context, _ string, addr string) (net.Conn, error) {
		dialed = addr
		return &reportedRemoteConn{
			Conn: clientSide,
			addr: &net.TCPAddr{IP: net.ParseIP("93.184.216.34"), Port: 443},
		}, nil
	}, "tcp", "93.184.216.34:443")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	assert.Equal(t, "93.184.216.34:443", dialed)
}

func TestCaptureProxy_BlocksPrivateSubresources(t *testing.T) {
	var dialed bool
	proxy, err := newCaptureProxyWithDial(func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("unexpected dial")
	})
	require.NoError(t, err)
	t.Cleanup(proxy.Close)
	client := proxyHTTPClient(t, proxy.URL())

	for _, target := range []string{
		"http://localhost/private-pixel.png",
		"http://10.0.0.8/pixel.png",
		"http://172.16.0.8/script.js",
		"http://192.168.0.8/style.css",
		"http://100.64.0.8/carrier-internal.js",
		"http://100.127.255.254/carrier-internal.css",
		"http://169.254.2.3/font.woff2",
		"http://[fd00::8]/frame",
		"http://169.254.169.254/latest/meta-data/",
	} {
		resp, requestErr := client.Get(target)
		require.NoError(t, requestErr, target)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, target)
		resp.Body.Close()
	}
	assert.False(t, dialed, "blocked subresources must fail before dialing")
	assert.True(t, proxy.Blocked())
}

func TestCaptureProxy_TunnelsPublicCONNECTAndClosesIt(t *testing.T) {
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = upstream.Close() })
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		request := make([]byte, 4)
		if _, readErr := io.ReadFull(conn, request); readErr != nil {
			return
		}
		if string(request) == "ping" {
			_, _ = conn.Write([]byte("pong"))
		}
		_, _ = io.Copy(io.Discard, conn)
	}()

	proxy, err := newCaptureProxyWithDial(func(ctx context.Context, network, _ string) (net.Conn, error) {
		conn, dialErr := (&net.Dialer{}).DialContext(ctx, network, upstream.Addr().String())
		if dialErr != nil {
			return nil, dialErr
		}
		return &reportedRemoteConn{
			Conn: conn,
			addr: &net.TCPAddr{IP: net.ParseIP("93.184.216.34"), Port: 443},
		}, nil
	})
	require.NoError(t, err)

	proxyURL, err := url.Parse(proxy.URL())
	require.NoError(t, err)
	conn, err := net.DialTimeout("tcp", proxyURL.Host, time.Second)
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	_, err = fmt.Fprintf(conn, "CONNECT 93.184.216.34:443 HTTP/1.1\r\nHost: 93.184.216.34:443\r\n\r\n")
	require.NoError(t, err)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	_, err = conn.Write([]byte("ping"))
	require.NoError(t, err)
	reply := make([]byte, 4)
	_, err = io.ReadFull(reader, reply)
	require.NoError(t, err)
	assert.Equal(t, "pong", string(reply))
	assert.False(t, proxy.Blocked())

	proxy.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, err = conn.Read(make([]byte, 1))
	assert.Error(t, err, "capture cleanup must close active CONNECT tunnels")
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
		t.Fatal("upstream tunnel did not stop after proxy cleanup")
	}
}

func TestCaptureProxy_BlocksPrivateCONNECT(t *testing.T) {
	var dialed bool
	proxy, err := newCaptureProxyWithDial(func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("unexpected dial")
	})
	require.NoError(t, err)
	t.Cleanup(proxy.Close)

	proxyURL, err := url.Parse(proxy.URL())
	require.NoError(t, err)
	conn, err := net.DialTimeout("tcp", proxyURL.Host, time.Second)
	require.NoError(t, err)
	defer conn.Close()
	_, err = fmt.Fprintf(conn, "CONNECT 127.0.0.1:443 HTTP/1.1\r\nHost: 127.0.0.1:443\r\n\r\n")
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.False(t, dialed, "private CONNECT must fail before dialing")
	assert.True(t, proxy.Blocked())
}

func TestCaptureProxy_RejectsCONNECTAboveTunnelLimit(t *testing.T) {
	var dialed atomic.Int64
	var upstreamMu sync.Mutex
	var upstreams []net.Conn
	proxy, err := newCaptureProxyWithDial(func(context.Context, string, string) (net.Conn, error) {
		client, upstream := net.Pipe()
		upstreamMu.Lock()
		upstreams = append(upstreams, upstream)
		upstreamMu.Unlock()
		dialed.Add(1)
		return &reportedRemoteConn{
			Conn: client,
			addr: &net.TCPAddr{IP: net.ParseIP("93.184.216.34"), Port: 443},
		}, nil
	})
	require.NoError(t, err)
	var clients []net.Conn
	t.Cleanup(func() {
		for _, conn := range clients {
			_ = conn.Close()
		}
		upstreamMu.Lock()
		for _, conn := range upstreams {
			_ = conn.Close()
		}
		upstreamMu.Unlock()
		proxy.Close()
	})

	for range maxProxyTunnels {
		conn, status := openProxyTunnel(t, proxy.Address())
		require.Equal(t, http.StatusOK, status)
		clients = append(clients, conn)
	}
	require.Equal(t, maxProxyTunnels, len(proxy.tunnelSem))
	assert.Equal(t, int64(maxProxyTunnels), dialed.Load())

	overLimit, status := openProxyTunnel(t, proxy.Address())
	_ = overLimit.Close()
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, int64(maxProxyTunnels), dialed.Load(), "over-limit CONNECT must be rejected before dial")
	assert.True(t, proxy.Blocked(), "exceeding any capture budget must invalidate the screenshot")
	require.Eventually(t, func() bool { return len(proxy.tunnelSem) == 0 }, time.Second, time.Millisecond)
}

func TestCaptureProxy_RequestBudgetInvalidatesCapture(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(upstream.Close)
	proxy, err := newCaptureProxyWithDial(publicDialTo(upstream.Listener.Addr().String()))
	require.NoError(t, err)
	t.Cleanup(proxy.Close)
	proxy.requestLimit = 2
	client := proxyHTTPClient(t, proxy.URL())

	for range 2 {
		resp, requestErr := client.Get("http://93.184.216.34/resource")
		require.NoError(t, requestErr)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}
	resp, err := client.Get("http://93.184.216.34/over-limit")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.True(t, proxy.Blocked())
}

func TestCaptureProxy_ByteBudgetInvalidatesCapture(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "response exceeds budget")
	}))
	t.Cleanup(upstream.Close)
	proxy, err := newCaptureProxyWithDial(publicDialTo(upstream.Listener.Addr().String()))
	require.NoError(t, err)
	t.Cleanup(proxy.Close)
	proxy.byteLimit = 8
	client := proxyHTTPClient(t, proxy.URL())

	resp, err := client.Get("http://93.184.216.34/large")
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	assert.True(t, proxy.Blocked())
}

func TestCaptureProxy_ConnectionBudgetInvalidatesCapture(t *testing.T) {
	var peers []net.Conn
	var peersMu sync.Mutex
	proxy, err := newCaptureProxyWithDial(func(context.Context, string, string) (net.Conn, error) {
		client, peer := net.Pipe()
		peersMu.Lock()
		peers = append(peers, peer)
		peersMu.Unlock()
		return &reportedRemoteConn{Conn: client, addr: &net.TCPAddr{IP: net.ParseIP("93.184.216.34"), Port: 80}}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		peersMu.Lock()
		for _, peer := range peers {
			_ = peer.Close()
		}
		peersMu.Unlock()
		proxy.Close()
	})

	connections := make([]net.Conn, 0, maxProxyConnections)
	for range maxProxyConnections {
		conn, dialErr := proxy.dialContext(context.Background(), "tcp", "93.184.216.34:80")
		require.NoError(t, dialErr)
		connections = append(connections, conn)
	}
	_, err = proxy.dialContext(context.Background(), "tcp", "93.184.216.34:80")
	require.ErrorIs(t, err, errProxyBudgetExceeded)
	assert.True(t, proxy.Blocked())
	for _, conn := range connections {
		_ = conn.Close()
	}
}

func publicDialTo(address string) dialContextFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &reportedRemoteConn{Conn: conn, addr: &net.TCPAddr{IP: net.ParseIP("93.184.216.34"), Port: 80}}, nil
	}
}

func openProxyTunnel(t *testing.T, proxyAddress string) (net.Conn, int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddress, time.Second)
	require.NoError(t, err)
	_, err = fmt.Fprintf(conn, "CONNECT 93.184.216.34:443 HTTP/1.1\r\nHost: 93.184.216.34:443\r\n\r\n")
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return conn, resp.StatusCode
}

func TestCaptureProxy_BlocksPrivatePeerAfterDial(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	t.Cleanup(func() { _ = serverSide.Close() })
	proxy, err := newCaptureProxyWithDial(func(context.Context, string, string) (net.Conn, error) {
		return &reportedRemoteConn{
			Conn: clientSide,
			addr: &net.TCPAddr{IP: net.ParseIP("192.168.10.20"), Port: 80},
		}, nil
	})
	require.NoError(t, err)
	t.Cleanup(proxy.Close)
	client := proxyHTTPClient(t, proxy.URL())

	resp, err := client.Get("http://93.184.216.34/rebound")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.True(t, proxy.Blocked(), "post-dial peer rejection must invalidate the capture")
}

func TestCaptureProxy_BlocksRFC6598PeerAfterDial(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	t.Cleanup(func() { _ = serverSide.Close() })
	proxy, err := newCaptureProxyWithDial(func(context.Context, string, string) (net.Conn, error) {
		return &reportedRemoteConn{
			Conn: clientSide,
			addr: &net.TCPAddr{IP: net.ParseIP("100.64.12.34"), Port: 80},
		}, nil
	})
	require.NoError(t, err)
	t.Cleanup(proxy.Close)
	client := proxyHTTPClient(t, proxy.URL())

	resp, err := client.Get("http://93.184.216.34/rebound")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.True(t, proxy.Blocked(), "RFC6598 post-dial rejection must invalidate the capture")
}

type reportedRemoteConn struct {
	net.Conn
	addr net.Addr
}

func (c *reportedRemoteConn) RemoteAddr() net.Addr { return c.addr }

func proxyHTTPClient(t *testing.T, rawProxyURL string) *http.Client {
	t.Helper()
	proxyURL, err := url.Parse(rawProxyURL)
	require.NoError(t, err)
	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURL),
			DisableKeepAlives: true,
		},
	}
}
