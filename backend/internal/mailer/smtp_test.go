package mailer

import (
	"bufio"
	"context"
	"crypto/tls"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSMTP is a minimal SMTP server: enough of the protocol for net/smtp's
// client to complete a session, and nothing more.
//
// A real listener rather than an interface seam, because what needs testing IS
// the protocol conversation — MAIL FROM, RCPT TO, the DATA terminator, dot
// stuffing. Mocking an smtp.Client would assert that the code calls the methods
// it calls, which proves nothing about whether a server would accept the result.
type fakeSMTP struct {
	addr     string
	mu       sync.Mutex
	sessions []string
	ln       net.Listener
	// failAt makes the server reject a given verb, so the error paths are
	// exercised rather than assumed.
	failAt string
}

func newFakeSMTP(t *testing.T, failAt string) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &fakeSMTP{addr: ln.Addr().String(), ln: ln, failAt: failAt}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	return s
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	write := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}

	write("220 fake ESMTP")
	var transcript strings.Builder
	inData := false

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			break
		}
		trimmed := strings.TrimRight(line, "\r\n")

		if inData {
			if trimmed == "." {
				inData = false
				write("250 OK")
				continue
			}
			transcript.WriteString(trimmed + "\n")
			continue
		}

		verb := strings.ToUpper(strings.Fields(trimmed + " ")[0])
		transcript.WriteString(trimmed + "\n")

		if s.failAt != "" && verb == s.failAt {
			write("550 refused")
			continue
		}

		switch verb {
		case "EHLO":
			write("250-fake")
			write("250 AUTH PLAIN")
		case "HELO":
			write("250 fake")
		case "MAIL", "RCPT", "AUTH", "NOOP":
			write("250 OK")
		case "DATA":
			inData = true
			write("354 send it")
		case "QUIT":
			write("221 bye")
			s.mu.Lock()
			s.sessions = append(s.sessions, transcript.String())
			s.mu.Unlock()
			return
		default:
			write("250 OK")
		}
	}
	s.mu.Lock()
	s.sessions = append(s.sessions, transcript.String())
	s.mu.Unlock()
}

func (s *fakeSMTP) lastSession(t *testing.T) string {
	t.Helper()
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.sessions) > 0
	}, 3*time.Second, 10*time.Millisecond, "the server never completed a session")
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[len(s.sessions)-1]
}

func hostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, port
}

func newTestMailer(t *testing.T, addr string) Mailer {
	t.Helper()
	host, port := hostPort(t, addr)
	m, err := New(Config{
		Driver: "smtp", Host: host, Port: port,
		From: "foldex@localhost", FromName: "Foldex",
		STARTTLS: false, Timeout: 3 * time.Second,
	}, discardLogger())
	require.NoError(t, err)
	return m
}

func TestSMTPSendCompletesAConversation(t *testing.T) {
	srv := newFakeSMTP(t, "")
	m := newTestMailer(t, srv.addr)

	require.NoError(t, m.Send(context.Background(), Message{
		To: "someone@example.com", Subject: "Invitation", Text: "https://foldex.test/#invite=TOK",
	}))

	session := srv.lastSession(t)
	assert.Contains(t, session, "MAIL FROM:<foldex@localhost>")
	assert.Contains(t, session, "RCPT TO:<someone@example.com>")
	assert.Contains(t, session, "DATA")
	assert.Contains(t, session, "Subject: Invitation")
	assert.Contains(t, session, "https://foldex.test/#invite=TOK")
	assert.Equal(t, "smtp", m.Driver())
}

// Dot stuffing, verified against a server that actually parses the DATA
// terminator: a body line consisting of a single "." ends the message, so
// without stuffing everything after it is silently dropped.
func TestSMTPSendDotStuffsSoTheBodySurvives(t *testing.T) {
	srv := newFakeSMTP(t, "")
	m := newTestMailer(t, srv.addr)

	require.NoError(t, m.Send(context.Background(), Message{
		To: "a@b.com", Subject: "s", Text: "before\n.\nafter the dot",
	}))

	session := srv.lastSession(t)
	assert.Contains(t, session, "after the dot",
		"content following a lone dot line must survive — without stuffing the server ends DATA there")
}

func TestSMTPSendSurfacesServerRejections(t *testing.T) {
	for _, tc := range []struct {
		verb string
		want string
	}{
		{"MAIL", "MAIL FROM"},
		{"RCPT", "RCPT TO"},
		{"DATA", "DATA"},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			srv := newFakeSMTP(t, tc.verb)
			m := newTestMailer(t, srv.addr)

			err := m.Send(context.Background(), Message{To: "a@b.com", Subject: "s", Text: "t"})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			// The failure must name the stage, not just "550": a caller reading
			// the log needs to know whether the sender, the recipient or the
			// body was refused.
			assert.Contains(t, err.Error(), "mailer:")
		})
	}
}

func TestSMTPSendFailsOnUnreachableHost(t *testing.T) {
	m, err := New(Config{
		Driver: "smtp", Host: "127.0.0.1", Port: 1, From: "f@l", Timeout: time.Second,
	}, discardLogger())
	require.NoError(t, err)

	err = m.Send(context.Background(), Message{To: "a@b.com", Subject: "s", Text: "t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dial")
}

// STARTTLS was requested but the server does not advertise it. Continuing in
// plaintext would silently downgrade a connection the operator asked to be
// encrypted — the credential on it is the SMTP password and the payload is
// invite links.
func TestSMTPRefusesToDowngradeWhenSTARTTLSIsUnavailable(t *testing.T) {
	srv := newFakeSMTP(t, "")
	host, port := hostPort(t, srv.addr)
	m, err := New(Config{
		Driver: "smtp", Host: host, Port: port, From: "f@l",
		STARTTLS: true, Timeout: 3 * time.Second,
	}, discardLogger())
	require.NoError(t, err)

	err = m.Send(context.Background(), Message{To: "a@b.com", Subject: "s", Text: "t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not advertise STARTTLS")
}

// The deadline covers the whole conversation, not just the dial: a server that
// accepts the connection and then stops responding would otherwise pin the
// goroutine — and the request behind it — until the process died.
func TestSMTPSendHonoursAContextDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Accept and then say nothing at all.
		defer func() { _ = conn.Close() }()
		time.Sleep(10 * time.Second)
	}()

	host, port := hostPort(t, ln.Addr().String())
	m, err := New(Config{
		Driver: "smtp", Host: host, Port: port, From: "f@l", Timeout: 500 * time.Millisecond,
	}, discardLogger())
	require.NoError(t, err)

	start := time.Now()
	err = m.Send(context.Background(), Message{To: "a@b.com", Subject: "s", Text: "t"})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "the send must not hang past its timeout")
}

func TestSMTPSendCancellationInterruptsAStalledConversation(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		close(accepted)
		_, _ = bufio.NewReader(conn).ReadByte()
	}()

	host, port := hostPort(t, ln.Addr().String())
	m, err := New(Config{
		Driver: "smtp", Host: host, Port: port, From: "f@l", Timeout: time.Minute,
	}, discardLogger())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- m.Send(ctx, Message{To: "a@b.com", Subject: "s", Text: "t"})
	}()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		require.FailNow(t, "SMTP client did not connect")
	}
	cancel()
	select {
	case err := <-done:
		assert.Error(t, err)
	case <-time.After(time.Second):
		require.FailNow(t, "context cancellation did not interrupt SMTP")
	}
}

// tlsConfig is what STARTTLS and implicit TLS both hand to the handshake, so a
// mistake here silently downgrades every message. TLS 1.2 is the floor because
// 1.0/1.1 are deprecated and still accepted by plenty of SMTP servers.
func TestTLSConfigPinsAModernFloorAndVerifiesByDefault(t *testing.T) {
	t.Parallel()
	m := &smtpMailer{cfg: Config{Host: "smtp.example.com"}}
	c := m.tlsConfig()

	assert.Equal(t, "smtp.example.com", c.ServerName,
		"ServerName must be set or certificate verification cannot check the hostname")
	assert.Equal(t, uint16(tls.VersionTLS12), c.MinVersion)
	assert.False(t, c.InsecureSkipVerify, "verification must be on unless explicitly disabled")
}

// The opt-out exists only for a self-signed local test server; config.
// validateSecureDefaults refuses it alongside a non-loopback host.
func TestTLSConfigHonoursTheExplicitOptOut(t *testing.T) {
	t.Parallel()
	m := &smtpMailer{cfg: Config{Host: "localhost", InsecureSkipVerify: true}}
	assert.True(t, m.tlsConfig().InsecureSkipVerify)
}

func TestNewSMTPAppliesADefaultTimeout(t *testing.T) {
	t.Parallel()
	m, err := New(Config{Driver: "smtp", Host: "h", From: "f@l"}, discardLogger())
	require.NoError(t, err)
	// Without a deadline a server that accepts and then stalls pins the
	// goroutine — and the request behind it — forever.
	assert.Positive(t, m.(*smtpMailer).cfg.Timeout)
}
