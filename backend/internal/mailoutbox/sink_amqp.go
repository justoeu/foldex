package mailoutbox

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"time"

	"foldex/internal/pkg/privatenet"
	amqp "github.com/rabbitmq/amqp091-go"
)

// ErrNoBrokerURL is the boot-time refusal for MAIL_TRANSPORT=amqp with no URL.
//
// Refusing to start rather than falling back to inproc, for the same reason
// mailer.New refuses MAIL_DRIVER=smtp with no host: a silent downgrade leaves
// an operator believing mail runs on the broker they configured, and the
// symptom only appears when they scale the workers and half the mail vanishes
// from the queue they are watching.
var ErrNoBrokerURL = errors.New("mailoutbox: MAIL_TRANSPORT=amqp requires AMQP_URL")

// AMQPConfig is everything the sink needs to reach a broker.
type AMQPConfig struct {
	URL       string
	Topology  Topology
	TLSConfig *tls.Config
	// Logger carries the no-consumer warning below. Optional: a nil logger
	// falls back to the default, so a test constructing a bare config is not
	// forced to care.
	Logger *slog.Logger
}

// AMQPSink publishes claimed rows to a broker, still sealed.
//
// It does NOT render and it does NOT send: that is cmd/mailer's job. The split
// is what lets the sending process run without a Postgres credential while the
// process that holds one never holds an SMTP session.
type AMQPSink struct {
	cfg AMQPConfig

	// One channel behind one mutex, which means the relay's delivery workers do
	// NOT publish concurrently here: each waits for the one in front to finish
	// its confirm round-trip. That is a deliberate trade and worth stating
	// honestly — it is not that a pool would buy nothing, it is that the mutex
	// is what makes the unroutable-return check above sound, and auth mail
	// (invites, resets, sign-in codes) is nowhere near a volume where one
	// same-network round-trip per message matters. If it ever is, the fix is to
	// pipeline several deferred confirms, not to add workers.
	mu   sync.Mutex
	conn *amqp.Connection
	ch   *ConfirmingChannel
}

func NewAMQPSink(cfg AMQPConfig) (*AMQPSink, error) {
	if cfg.URL == "" {
		return nil, ErrNoBrokerURL
	}
	cfg.Topology = cfg.Topology.WithDefaults()
	return &AMQPSink{cfg: cfg}, nil
}

func (s *AMQPSink) Name() string { return "amqp" }

// warnIfNobodyIsListening reports a send queue with no consumer attached.
//
// Publishing to a bound queue nobody reads succeeds, gets its publisher
// confirm, and settles the outbox row as `published` — an outcome identical to
// a delivered message in every record the system keeps. The failure it hides is
// total: with MAIL_TRANSPORT=amqp and the worker not running, every reset link
// and sign-in code lands in a queue and stays there, and the first sign of it is
// a user saying the e-mail never arrived.
//
// What this does NOT cover: the count is read from the declare, so it is a
// snapshot taken when the connection is opened. A worker that dies while the
// sink holds a healthy connection is not reported until the next reconnect.
// That is a deliberate stop — a continuous probe means a round-trip per publish
// to watch for something the operator also sees in the queue depth — and the
// case it does cover is the one that actually happens: a stack brought up
// without the worker at all.
func (s *AMQPSink) warnIfNobodyIsListening(state SendQueueState) {
	if state.Consumers > 0 {
		return
	}
	logger := s.cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("mail queue has no consumer: messages will be accepted and never sent",
		"queue", s.cfg.Topology.QueueName(),
		"waiting", state.Messages,
		"hint", "start the mailer worker (COMPOSE_PROFILES=amqp) or set MAIL_TRANSPORT=inproc")
}

// Close releases the connection. Safe to call more than once.
func (s *AMQPSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discardLocked()
	return nil
}

func (s *AMQPSink) discardLocked() {
	if s.ch != nil {
		_ = s.ch.Close()
		s.ch = nil
	}
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

// channelLocked returns a live confirming channel, dialling if needed.
func (s *AMQPSink) channelLocked() (*ConfirmingChannel, error) {
	if s.conn != nil && !s.conn.IsClosed() && !s.ch.closed() {
		return s.ch, nil
	}
	s.discardLocked()

	conn, err := dialAMQP(s.cfg.URL, s.cfg.TLSConfig)
	if err != nil {
		return nil, err
	}
	ch, err := NewConfirmingChannel(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	state, err := s.cfg.Topology.Declare(ch.Raw())
	if err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, err
	}
	s.warnIfNobodyIsListening(state)
	s.conn, s.ch = conn, ch
	return ch, nil
}

// Deliver publishes one message and waits for the broker to take it.
//
// "Delivered" here means a queue holds the message, not that it reached a
// mailbox — which is the whole semantic of handing delivery to a queue, and is
// why the dead-letter watcher exists to settle what the ladder gives up on.
func (s *AMQPSink) Deliver(ctx context.Context, msg Outgoing) error {
	body, err := encodeWire(msg)
	if err != nil {
		// A message that cannot be encoded will not encode better later.
		return permanent(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ch, err := s.channelLocked()
	if err != nil {
		return err
	}
	err = ch.Publish(ctx, s.cfg.Topology.Exchange, s.cfg.Topology.RoutingKey,
		amqp.Table{AttemptHeader: int32(0)}, body)
	if err != nil && !errors.Is(err, ErrUnroutable) {
		// The channel is probably dead; drop it so the next attempt redials
		// instead of publishing into a socket nobody is reading. An unroutable
		// message is the exception — the channel is fine, the topology is not,
		// and redialling would only re-declare and re-fail.
		s.discardLocked()
	}
	return err
}

// Dial opens a broker connection from a config. The consumer lives in another
// package and must reach the broker the same way the sink does — including the
// refusal below, which is the one place a missing URL is caught for it.
func Dial(cfg AMQPConfig) (*amqp.Connection, error) {
	if cfg.URL == "" {
		return nil, ErrNoBrokerURL
	}
	return dialAMQP(cfg.URL, cfg.TLSConfig)
}

// Ping reports whether the broker accepts a connection. The deadline on ctx
// is the whole budget — without it the library's 30s dial would pin a
// status refresh (and the footer poll behind it) to a missing host. The URL
// never appears in the error: it carries the broker password.
func Ping(ctx context.Context, cfg AMQPConfig) error {
	if cfg.URL == "" {
		return ErrNoBrokerURL
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return errors.New("mailoutbox: AMQP_URL is not a valid URL")
	}
	timeout := 2 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ctx.Err()
		}
		timeout = remaining
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	dialer := func(network, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: timeout}
		conn, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		// DialConfig's handshake does not take ctx. Without a deadline a
		// broker that accepts TCP and never speaks AMQP pins Ping (and
		// whoever is waiting on Snapshot) past the 2s budget.
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return nil, err
		}
		if u.Scheme != "amqps" {
			if err := requirePrivatePeer(conn, addr); err != nil {
				return nil, err
			}
		}
		return conn, nil
	}
	conn, err := amqp.DialConfig(cfg.URL, amqp.Config{
		Dial:            dialer,
		TLSClientConfig: cfg.TLSConfig,
		Heartbeat:       0,
		Locale:          "en_US",
	})
	if err != nil {
		return errors.New("mailoutbox: broker unreachable")
	}
	_ = conn.Close()
	return nil
}

// ErrBrokerNotPrivate is returned when a plaintext dial lands on an address
// outside the operator's own network. It is a refusal, never a downgrade: the
// connection is closed before a byte of the AMQP handshake — and therefore
// before the SASL PLAIN credential — is written.
var ErrBrokerNotPrivate = errors.New("mailoutbox: plaintext amqp:// reached a non-private address")

// dialPrivateOnly connects and then verifies where it landed, refusing anything
// outside RFC1918/CGNAT/loopback/link-local.
//
// The type assertion is fail-closed on purpose. A connection whose address is
// not a *net.TCPAddr is one we cannot judge, and "cannot judge" must not read as
// "fine" on the path that carries a credential in clear.
func dialPrivateOnly(network, addr string) (net.Conn, error) {
	conn, err := net.DialTimeout(network, addr, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if err := requirePrivatePeer(conn, addr); err != nil {
		return nil, err
	}
	return conn, nil
}

// requirePrivatePeer closes conn and returns ErrBrokerNotPrivate unless the peer
// sits on the operator's own network. Split out from the dial so the decision
// can be tested against a constructed peer instead of a routable address: the
// obvious test — dial 203.0.113.4 and expect refusal — spends the whole timeout
// discovering the address is unreachable and then proves nothing.
func requirePrivatePeer(conn net.Conn, addr string) error {
	tcp, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok || !privatenet.IsOperatorNetwork(tcp.IP) {
		_ = conn.Close()
		// The address is named; the URL that produced it is not, because it
		// carries the broker password.
		return fmt.Errorf("%w: %s", ErrBrokerNotPrivate, addr)
	}
	return nil
}

// dialAMQP opens a connection, applying TLS when the URL asks for it.
func dialAMQP(rawURL string, tlsCfg *tls.Config) (*amqp.Connection, error) {
	return dialAMQPWith(rawURL, tlsCfg, dialPrivateOnly)
}

// dialAMQPWith takes the plaintext dialer as a parameter so a test can observe
// WHICH path a URL takes. Without the seam, replacing the checking dialer with a
// bare amqp.Dial turns requirePrivatePeer into dead code and every test still
// passes — the guarantee disappears with nothing to report it.
func dialAMQPWith(rawURL string, tlsCfg *tls.Config, plaintextDial func(network, addr string) (net.Conn, error)) (*amqp.Connection, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		// The URL carries the broker password, so the parse error never does.
		return nil, errors.New("mailoutbox: AMQP_URL is not a valid URL")
	}
	var conn *amqp.Connection
	if u.Scheme == "amqps" {
		conn, err = amqp.DialTLS(rawURL, tlsCfg)
	} else {
		// Plaintext only ever reaches here because AMQP_ALLOW_PLAINTEXT is on
		// (config.validateMailTransport refuses it otherwise), and that flag is a
		// claim about the NETWORK. Boot can only check the claim when the URL
		// carries an IP literal; a hostname — the majority form, including a
		// compose service name — is resolved later, by infrastructure the
		// operator may not control, and the answer can change between boot and
		// any reconnect.
		//
		// So the claim is re-checked HERE, against the peer we actually reached.
		// Same two-legged shape preview.safeDialer uses for SSRF, and for the
		// same reason: a name is not an address. This also closes the literal
		// that ParseIP rejects but the resolver accepts — 3221225985 dials
		// 192.0.2.1 all the same, and the peer check sees it.
		conn, err = amqp.DialConfig(rawURL, amqp.Config{Dial: plaintextDial})
	}
	if err != nil {
		return nil, fmt.Errorf("mailoutbox: dial broker: %w", err)
	}
	return conn, nil
}
