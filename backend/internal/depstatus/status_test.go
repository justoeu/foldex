package depstatus

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotOmitsUnconfiguredDependencies(t *testing.T) {
	t.Parallel()
	c := New()
	snap := c.Snapshot(context.Background())
	require.Empty(t, snap.Resources)
	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	assert.JSONEq(t, `{"resources":[]}`, string(raw))
}

func TestSnapshotRecordsOkAndUnreachableWithoutTheProbeError(t *testing.T) {
	t.Parallel()
	c := New()
	c.Add(ObjectStore, func(context.Context) error { return nil })
	c.Add(MailBroker, func(context.Context) error {
		return errors.New(`Get "http://192.168.68.70:9000/foldex/": dial tcp 192.168.68.70:9000: i/o timeout`)
	})
	snap := c.Snapshot(context.Background())
	require.Equal(t, []Resource{
		{ID: ObjectStore, State: StateOK},
		{ID: MailBroker, State: StateUnreachable},
	}, snap.Resources)

	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	body := string(raw)
	for _, leak := range []string{"192.168.68.70", "9000", "timeout", "foldex/", "http://"} {
		assert.NotContains(t, body, leak)
	}
}

func TestAlwaysUnreachableDoesNotDial(t *testing.T) {
	t.Parallel()
	c := New()
	c.Add(ObjectStore, AlwaysUnreachable)
	assert.Equal(t, StateUnreachable, c.Snapshot(context.Background()).Resources[0].State)
}

func TestSnapshotReusesTheCacheUntilTTL(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	c := New(WithTTL(time.Hour))
	c.Add(ObjectStore, func(context.Context) error {
		n.Add(1)
		return nil
	})
	_ = c.Snapshot(context.Background())
	_ = c.Snapshot(context.Background())
	assert.Equal(t, int32(1), n.Load())
}

func TestSnapshotRefreshesAfterTTL(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	c := New(WithTTL(time.Millisecond))
	c.Add(ObjectStore, func(context.Context) error {
		n.Add(1)
		return nil
	})
	_ = c.Snapshot(context.Background())
	time.Sleep(5 * time.Millisecond)
	_ = c.Snapshot(context.Background())
	assert.GreaterOrEqual(t, n.Load(), int32(2))
}

func TestAddReplacesADuplicateID(t *testing.T) {
	t.Parallel()
	c := New()
	c.Add(ObjectStore, AlwaysUnreachable)
	c.Add(ObjectStore, func(context.Context) error { return nil })
	snap := c.Snapshot(context.Background())
	require.Len(t, snap.Resources, 1)
	assert.Equal(t, StateOK, snap.Resources[0].State)
}

func TestNilCheckerSnapshotIsEmpty(t *testing.T) {
	t.Parallel()
	var c *Checker
	assert.Empty(t, c.Snapshot(context.Background()).Resources)
}

func TestTransitionLogsDoNotCarryTheProbeError(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	c := New(WithTTL(time.Millisecond), WithLogger(logger))
	up := atomic.Bool{}
	c.Add(ObjectStore, func(context.Context) error {
		if up.Load() {
			return nil
		}
		return errors.New("dial tcp 10.0.0.1:5672: connection refused")
	})
	_ = c.Snapshot(context.Background())
	logged := buf.String()
	assert.Contains(t, logged, "dependency unreachable")
	assert.Contains(t, logged, "object_store")
	assert.NotContains(t, logged, "10.0.0.1")
	assert.NotContains(t, logged, "5672")

	up.Store(true)
	time.Sleep(5 * time.Millisecond)
	buf.Reset()
	_ = c.Snapshot(context.Background())
	recovered := buf.String()
	assert.Contains(t, recovered, "dependency recovered")
	assert.NotContains(t, recovered, "10.0.0.1")
}

func TestCollectDoesNotWaitOnAHungProbe(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	c := New(WithTTL(time.Hour), WithTimeout(2*time.Second))
	c.Add(ObjectStore, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	go c.Snapshot(context.Background())
	<-started

	done := make(chan struct{})
	go func() {
		ch := make(chan prometheus.Metric, 4)
		c.Collect(ch)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Collect blocked on an in-flight probe")
	}
}

func TestCollectorEmitsCachedGaugesWithoutProbing(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	c := New(WithTTL(time.Hour))
	c.Add(ObjectStore, func(context.Context) error {
		n.Add(1)
		return nil
	})
	c.Add(MailBroker, AlwaysUnreachable)
	_ = c.Snapshot(context.Background())
	before := n.Load()

	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(c))
	got, err := reg.Gather()
	require.NoError(t, err)
	assert.Equal(t, before, n.Load(), "Collect must not dial")

	byName := map[string]float64{}
	for _, fam := range got {
		if fam.GetName() != "foldex_dependency_up" {
			continue
		}
		for _, m := range fam.Metric {
			label := m.GetLabel()[0].GetValue()
			byName[label] = m.GetGauge().GetValue()
		}
	}
	assert.Equal(t, 1.0, byName["object_store"])
	assert.Equal(t, 0.0, byName["mail_broker"])
}
