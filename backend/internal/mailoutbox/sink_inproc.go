package mailoutbox

import (
	"context"
	"errors"

	"foldex/internal/mailer"
)

// InprocSink renders and sends in the relay's own process.
//
// This is the default transport, and it is what keeps foldex a self-hosted
// binary plus a Postgres: durability, retry and backoff all come from the
// outbox table, so an instance with no broker loses nothing but horizontal
// scale. The AMQP sink (ADR-36, PR2) replaces this without the relay or the
// handlers changing.
type InprocSink struct {
	outbox *Outbox
	mailer mailer.Mailer
}

func NewInprocSink(o *Outbox, m mailer.Mailer) *InprocSink {
	return &InprocSink{outbox: o, mailer: m}
}

func (s *InprocSink) Name() string { return "inproc" }

func (s *InprocSink) Deliver(ctx context.Context, msg Outgoing) error {
	env, err := s.outbox.Open(msg)
	if err != nil {
		// The key changed, or the bytes did. Neither improves with a retry, and
		// both are conditions an operator has to see rather than watch decay
		// quietly into a queue that never drains.
		return permanent(err)
	}
	m, err := mailer.Render(env, msg.Locale)
	if err != nil {
		if errors.Is(err, mailer.ErrUnknownTemplate) {
			return permanent(err)
		}
		return err
	}
	return s.mailer.Send(ctx, m)
}
