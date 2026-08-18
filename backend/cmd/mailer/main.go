// Command mailer is the sending half of the AMQP mail transport.
//
// It consumes foldex.mail.send, opens the sealed payload, renders the template
// in the recipient's locale and hands the message to SMTP. It is a separate
// binary so sending can be scaled — and restarted, and crashed — independently
// of the process that serves HTTP.
//
// It deliberately does NOT talk to Postgres. This is the one process that
// decrypts live reset links and sign-in codes, and giving it a database
// credential as well would make a compromise of it a compromise of everything.
// The outbox row's final state is reported by the backend instead, from the
// dead-letter queue, which needs only an id and a reason.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"foldex/internal/config"
	"foldex/internal/mailer"
	"foldex/internal/mailoutbox"
	"foldex/internal/mailworker"
	"foldex/internal/pkg/keyfile"
	"foldex/internal/pkg/logsafe"
)

// shutdownDeadline matches cmd/server: a process that will not stop is killed
// by the orchestrator anyway, and a longer wait only delays the restart.
const shutdownDeadline = 12 * time.Second

func main() {
	// The redactor wraps the ROOT handler, before anything else exists to log
	// through it — the same ordering cmd/server uses, and for the same reason:
	// this process handles nothing but credentials.
	logger := slog.New(logsafe.NewRedactHandler(
		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.SetDefault(logger)

	os.Exit(run(logger))
}

func run(logger *slog.Logger) int {
	cfg, err := config.LoadMailer()
	if err != nil {
		logger.Error("config load failed", "err", err)
		return 1
	}
	if !cfg.Mail.UsesBroker() {
		// Running this binary against MAIL_TRANSPORT=inproc would consume a
		// queue nothing publishes to, and look healthy doing it.
		logger.Error("mailer requires MAIL_TRANSPORT=amqp", "transport", cfg.Mail.Transport)
		return 1
	}
	// The same refusal the backend applies: the log driver would print reset
	// links and sign-in codes to stdout, and a worker is exactly the process
	// whose stdout ends up in a shared aggregator.
	if cfg.Mail.Driver != "smtp" {
		logger.Warn("mailer is running with a non-SMTP driver — message bodies will be logged, not sent",
			"driver", cfg.Mail.Driver)
	}

	// AllowEphemeral is false here for the same reason it is in cfg/server: a
	// session-only key cannot open payloads the backend sealed with the real
	// one, so the worker would drain the queue marking everything undecryptable.
	authKey, err := keyfile.Load(keyfile.Config{
		Name:           "auth encryption key",
		EnvVar:         "AUTH_ENCRYPTION_KEY",
		PathVar:        "AUTH_ENCRYPTION_KEY_PATH",
		EnvValue:       cfg.AuthEncryptionKey,
		Path:           cfg.AuthEncryptionKeyPath,
		AutoGenerate:   false,
		AllowEphemeral: false,
	}, logger)
	if err != nil {
		logger.Error("auth encryption key", "err", err)
		return 1
	}
	outbox, err := mailoutbox.NewFromMasterKey(authKey)
	if err != nil {
		logger.Error("mail outbox cipher", "err", err)
		return 1
	}

	mail, err := mailer.New(mailer.Config{
		Driver:             cfg.Mail.Driver,
		Host:               cfg.Mail.Host,
		Port:               cfg.Mail.Port,
		Username:           cfg.Mail.Username,
		Password:           cfg.Mail.Password,
		From:               cfg.Mail.From,
		FromName:           cfg.Mail.FromName,
		STARTTLS:           cfg.Mail.STARTTLS,
		TLS:                cfg.Mail.TLS,
		InsecureSkipVerify: cfg.Mail.InsecureSkipVerify,
	}, logger)
	if err != nil {
		logger.Error("mailer", "err", err)
		return 1
	}

	worker := mailworker.New(outbox, mail, mailoutbox.AMQPConfig{
		URL: cfg.Mail.AMQPURL,
		Topology: mailoutbox.Topology{
			Exchange:   cfg.Mail.AMQPExchange,
			Queue:      cfg.Mail.AMQPQueue,
			RoutingKey: cfg.Mail.AMQPRoutingKey,
		},
	}, mailworker.Options{Prefetch: cfg.Mail.AMQPPrefetch}, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	worker.Start(ctx)
	logger.Info("mailer ready", "queue", cfg.Mail.AMQPQueue, "prefetch", cfg.Mail.AMQPPrefetch)

	<-ctx.Done()
	logger.Info("shutting down")

	// Stop joins the consume loop, which cancels the in-flight send. That send
	// fails as `canceled`, and the message is then republished onto its next
	// ladder step and acked — the reroute runs on a context detached from the
	// cancellation precisely so shutdown cannot strand it. So a deploy costs
	// the message one attempt and one backoff step, not its delivery.
	done := make(chan struct{})
	go func() {
		worker.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownDeadline):
		logger.Warn("shutdown deadline elapsed; exiting anyway")
	}
	logger.Info("bye")
	return 0
}
