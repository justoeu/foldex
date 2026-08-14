package mailer

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultDispatcherWorkers   = 2
	DefaultDispatcherQueueSize = 32
	defaultDispatchTimeout     = 15 * time.Second
)

var (
	ErrQueueFull = errors.New("mailer: queue full")
	ErrStopped   = errors.New("mailer: dispatcher stopped")
	errUsed      = errors.New("mailer: admission already used")
)

type DispatcherOptions struct {
	Workers     int
	QueueSize   int
	SendTimeout time.Duration
}

type dispatchJob struct {
	message Message
	what    string
}

// Dispatcher bounds asynchronous mail delivery for the whole process.
type Dispatcher struct {
	mailer  Mailer
	logger  *slog.Logger
	timeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan dispatchJob
	slots  chan struct{}

	mu      sync.Mutex
	stopped bool
	stop    sync.Once
	workers sync.WaitGroup
}

func NewDispatcher(parent context.Context, m Mailer, opts DispatcherOptions, logger *slog.Logger) *Dispatcher {
	if opts.Workers <= 0 {
		opts.Workers = DefaultDispatcherWorkers
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = DefaultDispatcherQueueSize
	}
	if opts.SendTimeout <= 0 {
		opts.SendTimeout = defaultDispatchTimeout
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(parent)
	d := &Dispatcher{
		mailer:  m,
		logger:  logger,
		timeout: opts.SendTimeout,
		ctx:     ctx,
		cancel:  cancel,
		jobs:    make(chan dispatchJob, opts.QueueSize),
		slots:   make(chan struct{}, opts.QueueSize),
	}
	d.workers.Add(opts.Workers)
	for range opts.Workers {
		go d.run()
	}
	return d
}

func (d *Dispatcher) run() {
	defer d.workers.Done()
	for {
		select {
		case <-d.ctx.Done():
			return
		default:
		}
		select {
		case <-d.ctx.Done():
			return
		case job := <-d.jobs:
			<-d.slots
			ctx, cancel := context.WithTimeout(d.ctx, d.timeout)
			err := d.mailer.Send(ctx, job.message)
			cancel()
			if err != nil {
				d.logger.Error("send mail", "what", job.what, "err", err)
			}
		}
	}
}

// Reserve claims queue capacity before a caller publishes durable credentials.
func (d *Dispatcher) Reserve() (*Admission, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || d.ctx.Err() != nil {
		return nil, ErrStopped
	}
	select {
	case d.slots <- struct{}{}:
		return &Admission{dispatcher: d}, nil
	default:
		return nil, ErrQueueFull
	}
}

func (d *Dispatcher) Enqueue(msg Message, what string) error {
	a, err := d.Reserve()
	if err != nil {
		return err
	}
	return a.Publish(msg, what)
}

// Stop rejects new work, cancels active sends, and joins every worker.
func (d *Dispatcher) Stop() {
	d.stop.Do(func() {
		d.mu.Lock()
		d.stopped = true
		d.cancel()
		d.mu.Unlock()
		d.workers.Wait()
	})
}

// Admission is one reserved queue slot. It must be published or released.
type Admission struct {
	dispatcher *Dispatcher
	used       atomic.Bool
}

func (a *Admission) Publish(msg Message, what string) error {
	if a == nil || a.dispatcher == nil || !a.used.CompareAndSwap(false, true) {
		return errUsed
	}
	d := a.dispatcher
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || d.ctx.Err() != nil {
		<-d.slots
		return ErrStopped
	}
	// The reservation occupies one slot, so at most cap(jobs)-1 other jobs can
	// be queued here. Publishing therefore cannot block.
	select {
	case d.jobs <- dispatchJob{message: msg, what: what}:
		return nil
	default:
		<-d.slots
		return ErrQueueFull
	}
}

func (a *Admission) Release() {
	if a == nil || a.dispatcher == nil || !a.used.CompareAndSwap(false, true) {
		return
	}
	a.dispatcher.mu.Lock()
	<-a.dispatcher.slots
	a.dispatcher.mu.Unlock()
}
