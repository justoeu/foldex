package main

import (
	"context"
	"testing"
	"time"
)

func TestHTTPServerKeepsOrdinaryRequestsOnShortDeadlines(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:9089", nil)

	if srv.ReadTimeout <= 0 || srv.ReadTimeout > 2*time.Minute {
		t.Fatalf("ReadTimeout = %s, want a positive deadline no longer than 2m", srv.ReadTimeout)
	}
	if srv.WriteTimeout <= 0 || srv.WriteTimeout > 2*time.Minute {
		t.Fatalf("WriteTimeout = %s, want a positive deadline no longer than 2m", srv.WriteTimeout)
	}
	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s, want 5s", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout = %s, want 60s", srv.IdleTimeout)
	}
}

func TestShutdownJoinsAuthSweeper(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan bool, 1)
	hooks := shutdownHooks{
		shutdownHTTP:     func(context.Context) error { return nil },
		stopMail:         func() {},
		stopWorkers:      func() {},
		closeScreenshots: func() {},
		waitSweeper: func() {
			close(started)
			<-release
		},
	}

	go func() { done <- waitForShutdown(context.Background(), hooks) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("auth sweeper wait did not start")
	}
	select {
	case <-done:
		t.Fatal("graceful shutdown completed before the auth sweeper stopped")
	default:
	}
	close(release)
	select {
	case completed := <-done:
		if !completed {
			t.Fatal("graceful shutdown timed out after the auth sweeper stopped")
		}
	case <-time.After(time.Second):
		t.Fatal("graceful shutdown did not complete after the auth sweeper stopped")
	}
}
