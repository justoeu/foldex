package mailoutbox

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Lifecycle is the Start/Stop discipline every mail worker shares: a second
// Start must not replace the context under the running loop, and Stop sets
// stopped first, then cancels, then joins — in that order, so a tick already
// in progress claims no new work.
type Lifecycle struct {
	ctx     context.Context
	cancel  context.CancelFunc
	stopped atomic.Bool
	start   sync.Once
	stop    sync.Once
	wg      sync.WaitGroup
}

// Start launches loop once. The loop receives the lifecycle context and must
// return when it is done.
func (l *Lifecycle) Start(parent context.Context, loop func(ctx context.Context)) {
	l.start.Do(func() {
		if l.stopped.Load() {
			return
		}
		l.ctx, l.cancel = context.WithCancel(parent)
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			loop(l.ctx)
		}()
	})
}

func (l *Lifecycle) Stop() {
	l.stop.Do(func() {
		l.stopped.Store(true)
		if l.cancel != nil {
			l.cancel()
		}
		l.wg.Wait()
	})
}

// SeedContext installs a cancellable context without launching the loop —
// for tests that drive loop internals directly.
func (l *Lifecycle) SeedContext(parent context.Context) context.CancelFunc {
	l.ctx, l.cancel = context.WithCancel(parent)
	return l.cancel
}

func (l *Lifecycle) Stopped() bool { return l.stopped.Load() }

func (l *Lifecycle) Ctx() context.Context { return l.ctx }

// ConsumeConfig parameterizes one AMQP consumer connection.
type ConsumeConfig struct {
	Kind     HandlerKind
	Cfg      AMQPConfig
	Queue    string
	Prefetch int
	// OnDelivery handles one delivery; h exposes the confirming publish
	// channel for kinds that republish (worker retries).
	OnDelivery func(h *AMQPHandler, d amqp.Delivery)
}

// ConsumeOnce runs one connection's worth of deliveries, returning when it
// drops or ctx ends.
func ConsumeOnce(ctx context.Context, c ConsumeConfig) error {
	h, err := BuildAMQPHandler(c.Kind, c.Cfg)
	if err != nil {
		return err
	}
	defer h.Close()
	ch := h.Channel
	if err := ch.Qos(c.Prefetch, 0, false); err != nil {
		return err
	}
	deliveries, err := ch.Consume(c.Queue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return nil
			}
			c.OnDelivery(h, d)
		}
	}
}

// ReconnectLoop re-runs consume across broker drops with the shared
// ReconnectWait backoff until ctx ends or stopped reports true.
func ReconnectLoop(ctx context.Context, stopped func() bool, logger *slog.Logger, logMsg string, logErr bool, delay time.Duration, consume func() error) {
	for {
		if stopped() || ctx.Err() != nil {
			return
		}
		if err := consume(); err != nil && ctx.Err() == nil {
			if logErr {
				logger.Error(logMsg, "err", err)
			} else {
				logger.Warn(logMsg, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(ReconnectWait(delay)):
		}
	}
}
