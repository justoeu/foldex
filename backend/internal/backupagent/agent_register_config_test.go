package backupagent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backupjobs"
)

// handAgent is the minimal Agent RegisterJob can run against: a registry, a
// logger and a live agenda channel — no pool, no store, no Start.
func handAgent() *Agent {
	return &Agent{
		cfg:         Config{AllowPlaintext: true},
		logger:      testLogger(),
		schedChange: make(chan struct{}),
	}
}

func TestRegisterJobAppendsAndDelegatesToRun(t *testing.T) {
	a := handAgent()
	wantArtifact := &backupjobs.Artifact{Key: "exports/u1.zip"}
	wantErr := errors.New("job says no")
	calls := 0
	a.RegisterJob("custom_job", func(ctx context.Context) (*backupjobs.Artifact, map[string]any, string, error) {
		calls++
		require.NotNil(t, ctx)
		return wantArtifact, map[string]any{"n": 7}, "sentinel_reason", wantErr
	})

	require.Len(t, a.jobs, 1)
	assert.Equal(t, "custom_job", a.jobs[0].name)
	assert.True(t, a.registered("custom_job"))

	artifact, meta, reason, err := a.jobs[0].run(context.Background(), 4242)
	assert.Same(t, wantArtifact, artifact, "the wrapper forwards run's artifact untouched")
	assert.Equal(t, map[string]any{"n": 7}, meta)
	assert.Equal(t, "sentinel_reason", reason)
	assert.ErrorIs(t, err, wantErr)
	assert.Equal(t, 1, calls)
}

func TestRegisterJobRefreshesTimingsAndWakesSchedulers(t *testing.T) {
	a := handAgent()
	before := a.changeCh()

	a.RegisterJob(backupjobs.JobUserZip, func(context.Context) (*backupjobs.Artifact, map[string]any, string, error) {
		return nil, nil, "", nil
	})

	_, ok := a.timingFor(backupjobs.JobUserZip)
	assert.True(t, ok, "the new job joins the live agenda immediately")

	select {
	case <-before:
	default:
		t.Fatal("RegisterJob must close the previous schedChange so schedule loops recompute")
	}
	assert.True(t, before != a.changeCh(), "the agenda channel is replaced after the swap")
}

// timingFor reads the agenda map under the same lock timing() uses.
func (a *Agent) timingFor(job string) (backupjobs.Timing, bool) {
	a.schedMu.RLock()
	defer a.schedMu.RUnlock()
	tm, ok := a.timings[job]
	return tm, ok
}

func TestRegisterJobFlipsUserZipCapability(t *testing.T) {
	a := handAgent()

	capable, reason := a.capability(backupjobs.JobUserZip)
	assert.False(t, capable)
	assert.Equal(t, "no_source_credentials", reason)

	a.RegisterJob(backupjobs.JobUserZip, func(context.Context) (*backupjobs.Artifact, map[string]any, string, error) {
		return nil, nil, "", nil
	})

	capable, reason = a.capability(backupjobs.JobUserZip)
	assert.True(t, capable, "registration is exactly what makes user_zip runnable")
	assert.Empty(t, reason)
}

func TestRegisterJobPreservesRegistrationOrder(t *testing.T) {
	a := handAgent()
	noop := func(context.Context) (*backupjobs.Artifact, map[string]any, string, error) { return nil, nil, "", nil }
	a.RegisterJob("first", noop)
	a.RegisterJob("second", noop)

	require.Len(t, a.jobs, 2)
	assert.Equal(t, "first", a.jobs[0].name)
	assert.Equal(t, "second", a.jobs[1].name)
}

func TestHasRustFSCreds(t *testing.T) {
	full := Config{RustFSEndpoint: "rustfs:9000", RustFSAccessKey: "ak", RustFSSecretKey: "sk"}
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"all present", full, true},
		{"padded values still count", Config{RustFSEndpoint: " rustfs:9000 ", RustFSAccessKey: " ak ", RustFSSecretKey: " sk "}, true},
		{"nothing set", Config{}, false},
		{"missing endpoint", Config{RustFSAccessKey: "ak", RustFSSecretKey: "sk"}, false},
		{"missing access key", Config{RustFSEndpoint: "rustfs:9000", RustFSSecretKey: "sk"}, false},
		{"missing secret key", Config{RustFSEndpoint: "rustfs:9000", RustFSAccessKey: "ak"}, false},
		{"whitespace endpoint", Config{RustFSEndpoint: "   ", RustFSAccessKey: "ak", RustFSSecretKey: "sk"}, false},
		{"whitespace access key", Config{RustFSEndpoint: "rustfs:9000", RustFSAccessKey: " \t", RustFSSecretKey: "sk"}, false},
		{"whitespace secret key", Config{RustFSEndpoint: "rustfs:9000", RustFSAccessKey: "ak", RustFSSecretKey: "  "}, false},
		{"bucket is not part of the gate", Config{RustFSEndpoint: "rustfs:9000", RustFSAccessKey: "ak", RustFSSecretKey: "sk", RustFSBucket: ""}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.cfg.HasRustFSCreds())
		})
	}
}
