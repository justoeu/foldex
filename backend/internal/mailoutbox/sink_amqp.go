package mailoutbox

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"sync"

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
	if err := s.cfg.Topology.Declare(ch.Raw()); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, err
	}
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

// dialAMQP opens a connection, applying TLS when the URL asks for it.
func dialAMQP(rawURL string, tlsCfg *tls.Config) (*amqp.Connection, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		// The URL carries the broker password, so the parse error never does.
		return nil, errors.New("mailoutbox: AMQP_URL is not a valid URL")
	}
	var conn *amqp.Connection
	if u.Scheme == "amqps" {
		conn, err = amqp.DialTLS(rawURL, tlsCfg)
	} else {
		conn, err = amqp.Dial(rawURL)
	}
	if err != nil {
		return nil, fmt.Errorf("mailoutbox: dial broker: %w", err)
	}
	return conn, nil
}
