package mailoutbox

import (
	"errors"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// HandlerKind selects which broker session BuildAMQPHandler assembles. The
// relay sink, the mail worker and the dead-letter watcher each used to
// hand-roll the same dial → channel → declare sequence; they differ only in
// which channels they need, which is what the kind captures.
type HandlerKind int

const (
	// HandlerKindRelay is the publishing session: one confirming channel.
	HandlerKindRelay HandlerKind = iota
	// HandlerKindWorker is the consuming session: a plain channel to
	// consume from plus a confirming channel to republish retries on.
	HandlerKindWorker
	// HandlerKindDeadLetter is the settle-only session: one plain channel.
	HandlerKindDeadLetter
)

var errUnknownHandlerKind = errors.New("mailoutbox: unknown AMQP handler kind")

// AMQPHandler is one built broker session. Channel is the plain consume
// channel (worker and dead-letter kinds), Publish the confirming publish
// channel (relay and worker kinds); the field a kind does not use is nil.
// SendQueue carries the declare-time queue state the relay's no-consumer
// warning reads.
type AMQPHandler struct {
	Conn      *amqp.Connection
	Channel   *amqp.Channel
	Publish   *ConfirmingChannel
	SendQueue SendQueueState
}

// Close releases the channels and the connection. Safe on a nil handler and
// safe to call more than once: fields are cleared as they close.
func (h *AMQPHandler) Close() {
	if h == nil {
		return
	}
	if h.Publish != nil {
		_ = h.Publish.Close()
		h.Publish = nil
	}
	if h.Channel != nil {
		_ = h.Channel.Close()
		h.Channel = nil
	}
	if h.Conn != nil {
		_ = h.Conn.Close()
		h.Conn = nil
	}
}

// BuildAMQPHandler dials the broker, opens the channels the kind needs and
// declares the topology on them. Any failure closes whatever was opened, so
// a caller that gets an error owns nothing.
func BuildAMQPHandler(kind HandlerKind, cfg AMQPConfig) (*AMQPHandler, error) {
	cfg.Topology = cfg.Topology.WithDefaults()
	conn, err := Dial(cfg)
	if err != nil {
		return nil, err
	}
	h := &AMQPHandler{Conn: conn}
	if err := h.openChannels(kind, cfg); err != nil {
		h.Close()
		return nil, err
	}
	return h, nil
}

func (h *AMQPHandler) openChannels(kind HandlerKind, cfg AMQPConfig) error {
	switch kind {
	case HandlerKindRelay:
		pub, err := NewConfirmingChannel(h.Conn)
		if err != nil {
			return err
		}
		h.Publish = pub
		state, err := cfg.Topology.Declare(pub.Raw())
		if err != nil {
			return err
		}
		h.SendQueue = state
		return nil
	case HandlerKindWorker:
		ch, err := h.Conn.Channel()
		if err != nil {
			return err
		}
		h.Channel = ch
		if _, err := cfg.Topology.Declare(ch); err != nil {
			return err
		}
		pub, err := NewConfirmingChannel(h.Conn)
		if err != nil {
			return err
		}
		h.Publish = pub
		return nil
	case HandlerKindDeadLetter:
		ch, err := h.Conn.Channel()
		if err != nil {
			return err
		}
		h.Channel = ch
		if _, err := cfg.Topology.Declare(ch); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("%w: %d", errUnknownHandlerKind, int(kind))
	}
}
