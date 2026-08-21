package mailoutbox

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The boot check can only judge a URL that carries an IP literal. A hostname —
// the majority form, a compose service name included — is resolved later, by
// infrastructure the operator may not control, and the answer can change between
// boot and any reconnect. These cases are about the second leg: what the dial
// itself refuses, whatever the URL looked like.

// fakeConn reports a chosen RemoteAddr and records whether it was closed. The
// peer check has to CLOSE a rejected connection, not merely return an error —
// leaving it open would hold a socket to a stranger for as long as the process
// keeps retrying.
type fakeConn struct {
	net.Conn
	remote net.Addr
	closed bool
}

func (c *fakeConn) RemoteAddr() net.Addr { return c.remote }
func (c *fakeConn) Close() error         { c.closed = true; return nil }

func TestRequirePrivatePeer_AcceptsTheOperatorsNetwork(t *testing.T) {
	for _, ip := range []string{"192.168.68.70", "10.1.2.3", "100.64.0.1", "127.0.0.1", "fd00::1"} {
		c := &fakeConn{remote: &net.TCPAddr{IP: net.ParseIP(ip), Port: 5672}}
		require.NoError(t, requirePrivatePeer(c, ip+":5672"), "peer %s", ip)
		require.False(t, c.closed, "an accepted peer must stay open")
	}
}

// The case the whole function exists for: the URL named something, the name
// resolved somewhere public, and the credential must not follow.
func TestRequirePrivatePeer_RefusesAPublicPeerAndClosesIt(t *testing.T) {
	for _, ip := range []string{"8.8.8.8", "203.0.113.4", "2001:db8::1", "100.128.0.0"} {
		c := &fakeConn{remote: &net.TCPAddr{IP: net.ParseIP(ip), Port: 5672}}
		err := requirePrivatePeer(c, "broker.internal:5672")
		require.ErrorIs(t, err, ErrBrokerNotPrivate, "peer %s", ip)
		require.True(t, c.closed, "a refused peer must be closed, not merely reported")
	}
}

// Fail closed. An address we cannot judge is not an address we may trust — and
// this is the path that carries a credential in clear.
func TestRequirePrivatePeer_RefusesAnAddressItCannotJudge(t *testing.T) {
	c := &fakeConn{remote: &net.UnixAddr{Name: "/tmp/broker.sock", Net: "unix"}}
	err := requirePrivatePeer(c, "/tmp/broker.sock")
	require.ErrorIs(t, err, ErrBrokerNotPrivate)
	require.True(t, c.closed)
}

// The refusal names the address, because that is what makes it actionable, and
// carries nothing from the URL, which holds the broker password.
func TestRequirePrivatePeer_NamesTheAddressWithoutTheCredential(t *testing.T) {
	c := &fakeConn{remote: &net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 5672}}
	err := requirePrivatePeer(c, "broker.internal:5672")

	require.Contains(t, err.Error(), "broker.internal:5672")
	require.NotContains(t, err.Error(), "hunter2")
	require.NotContains(t, err.Error(), "@")
}

// The happy path end to end, against a real loopback listener, so the wiring
// between DialTimeout and the check is exercised too.
func TestDialPrivateOnly_ConnectsToALoopbackBroker(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := dialPrivateOnly("tcp", ln.Addr().String())
		require.NoError(t, err)
		require.NoError(t, conn.Close())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dial to a loopback listener should be immediate")
	}
}

func TestErrBrokerNotPrivate_IsASentinelCallersCanMatch(t *testing.T) {
	require.True(t, errors.Is(ErrBrokerNotPrivate, ErrBrokerNotPrivate))
	require.Contains(t, ErrBrokerNotPrivate.Error(), "non-private")
}

// The peer check only protects anything if the plaintext path actually goes
// through it. Reverting amqp.DialConfig to a bare amqp.Dial leaves
// requirePrivatePeer compiling, tested and never called — a guarantee that
// disappears with every other test still green. This is the case that notices.
func TestDialAMQP_PlaintextGoesThroughTheCheckingDialerAndTLSDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		url        string
		wantDialed bool
	}{
		{"amqp:// must use the checking dialer", "amqp://u:p@127.0.0.1:1/", true},
		{"amqps:// must not — TLS verifies the peer itself", "amqps://u:p@127.0.0.1:1/", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			dial := func(network, addr string) (net.Conn, error) {
				called = true
				return nil, errors.New("stop here: reaching the dialer is the assertion")
			}
			// Port 1 never answers, so both paths fail; what is under test is
			// which dialer was consulted on the way.
			_, _ = dialAMQPWith(tc.url, nil, dial)
			require.Equal(t, tc.wantDialed, called)
		})
	}
}
