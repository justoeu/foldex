package mailer

import (
	"bufio"
	"context"
	"errors"
	htmltemplate "html/template"
	"io/fs"
	"net"
	"strings"
	"testing"
	"testing/fstest"
	texttemplate "text/template"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A plaintext listener behind a TLS=false config: dialSMTP must hand back
// the raw connection with a deadline, in both deadline shapes.
func TestDialSMTP_PlainConnectionCarriesADeadline(t *testing.T) {
	srv := newFakeSMTP(t, "")
	host, port := hostPort(t, srv.addr)
	m := &smtpMailer{cfg: Config{Host: host, Port: port, Timeout: 3 * time.Second}}

	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{"no ctx deadline uses the configured timeout", func() (context.Context, context.CancelFunc) {
			return context.Background(), func() {}
		}},
		{"ctx deadline wins", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), time.Minute)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.ctx()
			defer cancel()
			conn, cancelClose, err := m.dialSMTP(ctx, srv.addr)
			require.NoError(t, err)
			require.NotNil(t, conn)
			assert.True(t, cancelClose())
			_ = conn.Close()
		})
	}
}

func TestDialSMTP_RefusedAndTLSAgainstPlaintext(t *testing.T) {
	m := &smtpMailer{cfg: Config{Host: "127.0.0.1", Port: 1, Timeout: time.Second}}
	_, _, err := m.dialSMTP(context.Background(), "127.0.0.1:1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mailer: dial 127.0.0.1:1")

	// Implicit TLS against a server that speaks plaintext SMTP: the
	// handshake, not the dial, is what fails — in both deadline shapes.
	srv := newFakeSMTP(t, "")
	host, port := hostPort(t, srv.addr)
	tlsMailer := &smtpMailer{cfg: Config{
		Host: host, Port: port, Timeout: 2 * time.Second,
		TLS: true, InsecureSkipVerify: true,
	}}
	for _, ctx := range []context.Context{
		context.Background(),
		func() context.Context {
			c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			t.Cleanup(cancel)
			return c
		}(),
	} {
		_, _, err = tlsMailer.dialSMTP(ctx, srv.addr)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tls handshake")
	}
}

// authSMTP answers AUTH with proper SMTP auth codes (235/535); net/smtp
// rejects the shared fake's blanket "250 OK" for that verb.
func authSMTPServer(t *testing.T, acceptAuth bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		_, _ = conn.Write([]byte("220 fake ESMTP\r\n"))
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					_, _ = conn.Write([]byte("250 OK\r\n"))
				}
				continue
			}
			switch {
			case len(line) >= 4 && line[:4] == "EHLO":
				_, _ = conn.Write([]byte("250-fake\r\n250 AUTH PLAIN\r\n"))
			case len(line) >= 4 && line[:4] == "AUTH":
				if acceptAuth {
					_, _ = conn.Write([]byte("235 authenticated\r\n"))
				} else {
					_, _ = conn.Write([]byte("535 bad credentials\r\n"))
				}
			case len(line) >= 4 && line[:4] == "DATA":
				inData = true
				_, _ = conn.Write([]byte("354 send it\r\n"))
			case len(line) >= 4 && line[:4] == "QUIT":
				_, _ = conn.Write([]byte("221 bye\r\n"))
				return
			default:
				_, _ = conn.Write([]byte("250 OK\r\n"))
			}
		}
	}()
	return ln.Addr().String()
}

func TestSMTPAuth_SuccessAndRejection(t *testing.T) {
	send := func(t *testing.T, addr string) error {
		t.Helper()
		host, port := hostPort(t, addr)
		m, err := New(Config{
			Driver: "smtp", Host: host, Port: port, From: "f@l",
			Username: "user", Password: "secret", Timeout: 3 * time.Second,
		}, discardLogger())
		require.NoError(t, err)
		return m.Send(context.Background(), Message{To: "a@b.com", Subject: "s", Text: "t"})
	}

	require.NoError(t, send(t, authSMTPServer(t, true)))

	err := send(t, authSMTPServer(t, false))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mailer: auth")
}

// starttlsGarbageServer advertises STARTTLS, accepts the upgrade verb, then
// hangs up — so the client's TLS handshake fails inside StartTLS.
func TestSMTPStartTLSHandshakeFailureIsWrapped(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		_, _ = conn.Write([]byte("220 fake ESMTP\r\n"))
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case len(line) >= 4 && line[:4] == "EHLO":
				_, _ = conn.Write([]byte("250-fake\r\n250 STARTTLS\r\n"))
			case len(line) >= 8 && line[:8] == "STARTTLS":
				_, _ = conn.Write([]byte("220 go ahead\r\n"))
				return // hang up mid-handshake
			default:
				_, _ = conn.Write([]byte("250 OK\r\n"))
			}
		}
	}()

	host, port := hostPort(t, ln.Addr().String())
	m, err := New(Config{
		Driver: "smtp", Host: host, Port: port, From: "f@l",
		STARTTLS: true, InsecureSkipVerify: true, Timeout: 3 * time.Second,
	}, discardLogger())
	require.NoError(t, err)

	err = m.Send(context.Background(), Message{To: "a@b.com", Subject: "s", Text: "t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mailer: starttls")
}

func TestSMTPQuitRejectionSurfaces(t *testing.T) {
	srv := newFakeSMTP(t, "QUIT")
	m := newTestMailer(t, srv.addr)
	err := m.Send(context.Background(), Message{To: "a@b.com", Subject: "s", Text: "t"})
	require.Error(t, err, "a server that refuses QUIT with 550 must not read as a clean send")
	assert.Contains(t, err.Error(), "550")
}

// handshakeGarbage accepts TCP and answers the SMTP greeting with nonsense,
// so smtp.NewClient fails before any verb is exchanged.
func TestSMTPSendGarbageGreetingIsAHandshakeError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte("hello, definitely not smtp\r\n"))
		time.Sleep(100 * time.Millisecond)
	}()

	host, port := hostPort(t, ln.Addr().String())
	m, err := New(Config{
		Driver: "smtp", Host: host, Port: port, From: "f@l", Timeout: 3 * time.Second,
	}, discardLogger())
	require.NoError(t, err)

	err = m.Send(context.Background(), Message{To: "a@b.com", Subject: "s", Text: "t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "smtp handshake")
}

// dataDropServer accepts DATA, then hangs up without the final 250 — the
// body write/close is where the failure must surface.
func TestSMTPSendServerDropsMidData(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		_, _ = conn.Write([]byte("220 fake ESMTP\r\n"))
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if len(line) >= 4 && line[:4] == "DATA" {
				_, _ = conn.Write([]byte("354 send it\r\n"))
				return // drop the connection with the body in flight
			}
			if len(line) >= 4 && line[:4] == "EHLO" {
				_, _ = conn.Write([]byte("250 fake\r\n"))
				continue
			}
			_, _ = conn.Write([]byte("250 OK\r\n"))
		}
	}()

	host, port := hostPort(t, ln.Addr().String())
	m, err := New(Config{
		Driver: "smtp", Host: host, Port: port, From: "f@l", Timeout: 3 * time.Second,
	}, discardLogger())
	require.NoError(t, err)

	err = m.Send(context.Background(), Message{To: "a@b.com", Subject: "s", Text: "body that never lands"})
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "write body") || strings.Contains(err.Error(), "close body"),
		"a mid-DATA drop is a body-stage failure, got: %v", err)
}

func TestCompileMessage_EachFieldCanRefuseBrokenCopy(t *testing.T) {
	broken := "hi {{ .Name"
	cases := []messageStrings{
		{Subject: broken},
		{Eyebrow: broken},
		{Heading: broken},
		{Action: broken},
		{Footnote: broken},
		{Body: []string{"fine", broken}},
	}
	for i, ms := range cases {
		_, err := compileMessage(ms)
		assert.Error(t, err, "case %d", i)
	}
	out, err := compileMessage(messageStrings{Subject: "plain", Body: []string{"one {{.Code}}"}})
	require.NoError(t, err)
	assert.Equal(t, "plain", out.subject.literal)
	require.Len(t, out.body, 1)
}

// composeDoc must fail on the FIRST copy piece whose params are missing —
// each field is its own branch, and the send-time error is what lands a bad
// queued row in the outbox's failed state.
func TestComposeDoc_MissingParamFailsPerField(t *testing.T) {
	mkCopy := func(s string) copyText {
		c, err := compileCopy(s)
		require.NoError(t, err)
		return c
	}
	needs := mkCopy("{{.Missing}}")
	lit := mkCopy("literal")
	base := compiledMessage{subject: lit, eyebrow: lit, heading: lit, action: lit, footnote: lit}

	variants := map[string]compiledMessage{
		"eyebrow":  {subject: lit, eyebrow: needs, heading: lit, action: lit, footnote: lit},
		"subject":  {subject: needs, eyebrow: lit, heading: lit, action: lit, footnote: lit},
		"heading":  {subject: lit, eyebrow: lit, heading: needs, action: lit, footnote: lit},
		"action":   {subject: lit, eyebrow: lit, heading: lit, action: needs, footnote: lit},
		"footnote": {subject: lit, eyebrow: lit, heading: lit, action: lit, footnote: needs},
		"body":     {subject: lit, eyebrow: lit, heading: lit, action: lit, footnote: lit, body: []copyText{needs}},
	}
	a := &assets{catalogs: map[string]map[string]compiledMessage{DefaultLocale: {}}}
	for name, ms := range variants {
		t.Run(name, func(t *testing.T) {
			_, _, err := a.composeDoc(ms, Envelope{Params: map[string]string{}}, "en")
			assert.Error(t, err)
		})
	}

	// Footer copy resolves first and fails the same way when ITS param is gone.
	a.catalogs[DefaultLocale][footerKey] = compiledMessage{body: []copyText{needs}}
	_, _, err := a.composeDoc(base, Envelope{Params: map[string]string{}}, "en")
	assert.Error(t, err, "a broken footer must not silently drop the sign-off check")

	// Empty body paragraphs are dropped, not rendered as blank gaps.
	a.catalogs[DefaultLocale][footerKey] = compiledMessage{}
	withGap := base
	withGap.body = []copyText{lit, mkCopy(""), lit}
	doc, subject, err := a.composeDoc(withGap, Envelope{Params: map[string]string{}}, "klingon")
	require.NoError(t, err)
	assert.Equal(t, "literal", subject)
	assert.Equal(t, []string{"literal", "literal"}, doc.Body)
	assert.Equal(t, DefaultLocale, doc.Locale)
}

func TestExecuteLayouts_EachArmCanFail(t *testing.T) {
	htmlOK := htmltemplate.Must(htmltemplate.New("mail").Parse(`{{define "x"}}H{{end}}`))
	textOK := texttemplate.Must(texttemplate.New("mail").Parse(`{{define "text.x"}}T{{end}}`))
	htmlBad := htmltemplate.Must(htmltemplate.New("mail").Parse(`{{define "x"}}{{.Nope}}{{end}}`))
	textBad := texttemplate.Must(texttemplate.New("mail").Parse(`{{define "text.x"}}{{.Nope}}{{end}}`))

	_, _, err := (&assets{html: htmlBad, text: textOK}).executeLayouts("x", mailDoc{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `render html "x"`)

	_, _, err = (&assets{html: htmlOK, text: textBad}).executeLayouts("x", mailDoc{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `render text "x"`)

	h, txt, err := (&assets{html: htmlOK, text: textOK}).executeLayouts("x", mailDoc{})
	require.NoError(t, err)
	assert.Equal(t, "H", h)
	assert.Equal(t, "T", txt)
}

type brokenFS struct{ err error }

func (b brokenFS) Open(string) (fs.File, error) { return nil, b.err }

func TestMustLoadAssets_PanicsOnBrokenAssetsAndReadErrorsSurface(t *testing.T) {
	assert.Panics(t, func() { _ = mustLoadAssets(fstest.MapFS{}) },
		"embedded assets that do not load must refuse the binary at init")

	_, _, err := loadOneCatalogue(brokenFS{err: errors.New("disk gone")}, "templates/strings.en.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read templates/strings.en.json")

	_, err = loadCatalogs(fstest.MapFS{
		"templates/strings.en.json": {Data: []byte(`{"x":{"heading":"{{ .B"}}`)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "strings.en.json/x", "the failing message is named")
}
