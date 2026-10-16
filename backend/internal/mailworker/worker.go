// Package mailworker is the consuming half of the AMQP transport: it takes a
// sealed message off the queue, opens it, renders it in the recipient's locale
// and hands it to SMTP.
//
// It exists as a package rather than living in cmd/mailer so the decision that
// matters — what happens when a send fails — is testable without a broker.
package mailworker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"foldex/internal/mailer"
	"foldex/internal/mailoutbox"
	"foldex/internal/pkg/secrets"
)

const (
	DefaultPrefetch    = 4
	DefaultMaxAttempts = 4
	DefaultSendTimeout = 15 * time.Second
	reconnectDelay     = 5 * time.Second
)

// Options tunes the consumer.
type Options struct {
	// Prefetch is clamped to 1..64. SMTP is serial I/O, so a high prefetch buys
	// no throughput and only widens how many messages are stranded in an
	// unacked state when a worker dies.
	Prefetch int
	// MaxAttempts counts total delivery attempts, ladder steps included. Past
	// it the message goes to the dead queue and the backend settles the row.
	MaxAttempts int
	SendTimeout time.Duration
}

// Worker consumes the send queue.
type Worker struct {
	outbox *mailoutbox.Outbox
	mail   mailer.Mailer
	cfg    mailoutbox.AMQPConfig
	opts   Options
	logger *slog.Logger

	life mailoutbox.Lifecycle
}

func New(outbox *mailoutbox.Outbox, m mailer.Mailer, cfg mailoutbox.AMQPConfig,
	opts Options, logger *slog.Logger) *Worker {

	if opts.Prefetch <= 0 {
		opts.Prefetch = DefaultPrefetch
	}
	if opts.Prefetch > 64 {
		opts.Prefetch = 64
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = DefaultMaxAttempts
	}
	if opts.SendTimeout <= 0 {
		opts.SendTimeout = DefaultSendTimeout
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg.Topology = cfg.Topology.WithDefaults()
	return &Worker{outbox: outbox, mail: m, cfg: cfg, opts: opts, logger: logger}
}

// Start launches the consume loop and returns immediately.
func (w *Worker) Start(parent context.Context) {
	w.life.Start(parent, func(ctx context.Context) {
		mailoutbox.ReconnectLoop(ctx, w.life.Stopped, w.logger, "mail worker connection", true, reconnectDelay, func() error {
			// Republishing happens on its own confirming channel. Mixing it into the
			// consuming channel would put confirm mode on something that never
			// publishes, and a retry that is acked without being confirmed AND routed
			// is a message deleted from the only place it still existed.
			return mailoutbox.ConsumeOnce(ctx, mailoutbox.ConsumeConfig{
				Kind: mailoutbox.HandlerKindWorker, Cfg: w.cfg,
				Queue: w.cfg.Topology.QueueName(), Prefetch: w.opts.Prefetch,
				OnDelivery: func(h *mailoutbox.AMQPHandler, d amqp.Delivery) { w.handle(h.Publish, d) },
			})
		})
	})
}

// Stop rejects new work, cancels in-flight sends and joins the loop.
func (w *Worker) Stop() { w.life.Stop() }

// handle delivers one message and decides its fate.
//
// The message is ACKed in every outcome, because every outcome has already
// routed it somewhere durable: success needs nothing further, a retry has been
// republished onto a ladder step, and a give-up has been republished to the
// dead queue. Only a failure to republish leaves it on the original queue.
func (w *Worker) handle(pub *mailoutbox.ConfirmingChannel, d amqp.Delivery) {
	attempt := mailoutbox.Attempt(d.Headers) + 1

	msg, err := mailoutbox.DecodeWire(d.Body)
	if err != nil {
		// Unreadable, and no version of this code reads it better later. It
		// carries no outbox id we could report either, so the queue is the only
		// record — which is exactly what the dead queue is for.
		w.logger.Error("mail worker: undecodable message", "err", err)
		w.route(pub, d, attempt, "undecodable_message", true)
		return
	}

	reason, fatal, err := w.send(msg)
	if err == nil {
		// INFO, and deliberately not silent. A queue worker whose only output
		// is its own startup line cannot answer the one question an operator
		// has when a user reports a missing e-mail — did this process send
		// anything at all? — and "mailer ready" reads identically after
		// draining a hundred messages and after sitting idle for a day.
		//
		// The recipient is NOT an attribute. logsafe redacts the key `email`,
		// not `recipient`, so naming the address here either leaks it into
		// every log line or renders it blank; the outbox id identifies the
		// message for anyone holding the database anyway.
		w.logger.Info("mail sent",
			"outbox_id", msg.OutboxID, "template", msg.Template, "attempt", attempt)
		_ = d.Ack(false)
		return
	}

	give := fatal || attempt >= w.opts.MaxAttempts
	w.logger.Warn("mail send failed",
		"outbox_id", msg.OutboxID, "template", msg.Template,
		"attempt", attempt, "reason", reason, "permanent", fatal, "dead", give)
	w.route(pub, d, attempt, reason, give)
}

// send opens, renders and delivers one message, classifying the failure.
//
// The bool reports whether retrying is pointless. A template the binary no
// longer ships and a payload that will not decrypt are both conditions an
// operator has to see, and spending the whole ladder on them only delays that
// by half an hour.
func (w *Worker) send(msg mailoutbox.WireMessage) (reason string, fatal bool, err error) {
	env, err := w.outbox.OpenWire(msg)
	if err != nil {
		return "undecryptable_payload", true, err
	}
	m, err := mailer.Render(env, msg.Locale)
	if err != nil {
		if errors.Is(err, mailer.ErrUnknownTemplate) {
			return "unknown_template", true, err
		}
		return "render_failed", true, err
	}
	ctx, cancel := context.WithTimeout(w.life.Ctx(), w.opts.SendTimeout)
	defer cancel()
	if err := w.mail.Send(ctx, m); err != nil {
		return sendReason(err), false, err
	}
	return "", false, nil
}

// route republishes a failed delivery and acks the original.
func (w *Worker) route(pub *mailoutbox.ConfirmingChannel, d amqp.Delivery, attempt int, reason string, dead bool) {
	// Detached from the worker context: during shutdown the cancellation lands
	// exactly here, and abandoning the republish would leave the message unacked
	// on the send queue to be retried immediately by the next worker, with no
	// backoff at all.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(w.life.Ctx()), 10*time.Second)
	defer cancel()

	if err := w.cfg.Topology.Republish(ctx, pub, d.Body, attempt, reason, dead); err != nil {
		// Leave it unacked. The broker redelivers it when this consumer drops,
		// which is slower than the ladder but never loses the message.
		w.logger.Error("mail worker: republish", "err", err)
		return
	}
	_ = d.Ack(false)
}

// sendReason normalizes a transport failure into a stable token.
//
// The transport's own text never reaches a log or a database column: an SMTP
// rejection routinely quotes the envelope back, and the envelope here is a
// recipient address.
func sendReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, secrets.ErrDecrypt):
		return "undecryptable_payload"
	}
	return "send_failed"
}
