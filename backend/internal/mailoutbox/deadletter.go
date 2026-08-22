package mailoutbox

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// deadLetterReconnectDelay keeps a broker outage from becoming a dial storm.
const deadLetterReconnectDelay = 5 * time.Second

// DeadLetterWatcher settles outbox rows the broker gave up on.
//
// The worker cannot do this itself, and that is deliberate rather than
// awkward: cmd/mailer is the process that DECRYPTS credentials, and keeping it
// without a Postgres credential is what bounds the damage if it is ever
// compromised. So the reporting runs here, in the backend, which already holds
// the database and — importantly — never needs to open the payload to do it.
// It reads an id and a reason, both of which travel outside the sealed blob.
//
// Without this, `mail_outbox.status` would stop meaning anything under AMQP:
// every row would read 'published' whether the message reached a mailbox or
// died three retries later, and the only surface left would be a queue an
// operator has to remember to look at.
type DeadLetterWatcher struct {
	repo   *Repository
	cfg    AMQPConfig
	logger *slog.Logger

	ctx     context.Context
	cancel  context.CancelFunc
	stopped atomic.Bool
	start   sync.Once
	stop    sync.Once
	wg      sync.WaitGroup
}

func NewDeadLetterWatcher(repo *Repository, cfg AMQPConfig, logger *slog.Logger) *DeadLetterWatcher {
	cfg.Topology = cfg.Topology.WithDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	return &DeadLetterWatcher{repo: repo, cfg: cfg, logger: logger}
}

// Start launches the consume loop. Same guard as Relay.Start: a second call
// must not replace the context out from under the running loop.
func (w *DeadLetterWatcher) Start(parent context.Context) {
	w.start.Do(func() {
		if w.stopped.Load() {
			return
		}
		w.ctx, w.cancel = context.WithCancel(parent)
		w.wg.Add(1)
		go w.loop()
	})
}

func (w *DeadLetterWatcher) Stop() {
	w.stop.Do(func() {
		w.stopped.Store(true)
		if w.cancel != nil {
			w.cancel()
		}
		w.wg.Wait()
	})
}

func (w *DeadLetterWatcher) loop() {
	defer w.wg.Done()
	for {
		if w.stopped.Load() || w.ctx.Err() != nil {
			return
		}
		if err := w.consume(); err != nil && w.ctx.Err() == nil {
			w.logger.Warn("mail dead-letter watcher", "err", err)
		}
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(ReconnectWait(deadLetterReconnectDelay)):
		}
	}
}

// consume runs one connection's worth of deliveries, returning when it drops.
func (w *DeadLetterWatcher) consume() error {
	conn, err := dialAMQP(w.cfg.URL, w.cfg.TLSConfig)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer func() { _ = ch.Close() }()
	if _, err := w.cfg.Topology.Declare(ch); err != nil {
		return err
	}
	// One at a time. This queue is a trickle by construction — it only receives
	// what exhausted the whole ladder — and a low prefetch keeps a restart from
	// stranding a batch of unsettled rows.
	if err := ch.Qos(1, 0, false); err != nil {
		return err
	}

	deliveries, err := ch.Consume(w.cfg.Topology.deadQueue(), "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	for {
		select {
		case <-w.ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return nil
			}
			w.settle(d)
		}
	}
}

func (w *DeadLetterWatcher) settle(d amqp.Delivery) {
	msg, err := DecodeWire(d.Body)
	if err != nil {
		// Nothing here improves with redelivery, and requeueing would spin the
		// loop on a message no version of this code can read.
		w.logger.Error("mail dead-letter payload unreadable", "err", err)
		_ = d.Ack(false)
		return
	}

	reason := "delivery_exhausted"
	if raw, ok := d.Headers[ReasonHeader].(string); ok && raw != "" {
		reason = raw
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(w.ctx), 5*time.Second)
	defer cancel()
	settled, err := w.repo.MarkDead(ctx, msg.OutboxID, reason)
	if err != nil {
		// Leave it on the queue. The row is the operator's record that this
		// message died, and dropping the report to keep the queue tidy would
		// trade the evidence for nothing.
		w.logger.Error("mail dead-letter settle", "id", msg.OutboxID, "err", err)
		_ = d.Nack(false, true)
		return
	}
	if !settled {
		// The report outlived the row it describes — purged after PublishedTTL,
		// or requeued out of 'published' by a concurrent sweep. Requeueing would
		// spin forever on something no UPDATE can ever match, so this is acked
		// and shouted about instead: the log line is now the ONLY record that
		// this message was never delivered.
		w.logger.Error("mail message abandoned by the broker, and no outbox row remained to record it",
			"id", msg.OutboxID, "template", msg.Template, "reason", reason)
		_ = d.Ack(false)
		return
	}
	w.logger.Warn("mail message abandoned by the broker",
		"id", msg.OutboxID, "template", msg.Template, "reason", reason)
	_ = d.Ack(false)
}
