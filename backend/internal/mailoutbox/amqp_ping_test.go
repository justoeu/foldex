package mailoutbox

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPing_EmptyURL(t *testing.T) {
	t.Parallel()
	err := Ping(context.Background(), AMQPConfig{})
	require.ErrorIs(t, err, ErrNoBrokerURL)
}

func TestPing_InvalidURLDoesNotEchoTheSecret(t *testing.T) {
	t.Parallel()
	err := Ping(context.Background(), AMQPConfig{URL: "amqp://user:super-secret@%zz"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "super-secret")
	assert.Equal(t, "mailoutbox: AMQP_URL is not a valid URL", err.Error())
}

func TestPing_UnreachablePortFailsInsideTheDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Ping(ctx, AMQPConfig{URL: "amqp://guest:guest@127.0.0.1:1/"})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 1500*time.Millisecond)
	assert.Equal(t, "mailoutbox: broker unreachable", err.Error())
	assert.False(t, strings.Contains(err.Error(), "guest"))
}

func TestPing_AcceptWithoutHandshakeFailsInsideDeadline(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, accErr := ln.Accept()
		if accErr != nil {
			return
		}
		defer conn.Close()
		time.Sleep(5 * time.Second)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = Ping(ctx, AMQPConfig{URL: "amqp://guest:guest@" + ln.Addr().String() + "/"})
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second)
	assert.Equal(t, "mailoutbox: broker unreachable", err.Error())
}

func TestPing_ExpiredContextDoesNotDial(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	err := Ping(ctx, AMQPConfig{URL: "amqp://guest:guest@127.0.0.1:5672/"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
