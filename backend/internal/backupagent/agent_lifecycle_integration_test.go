//go:build integration

package backupagent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/testdb"
)

// lifecycleConfig keeps the agent quiet (no anchors) and fast (1s poll).
func lifecycleConfig() Config {
	return Config{
		RequestedPollSec: 1,
		StaleRunMin:      240,
		RetentionMode:    "agent",
		MetricsAddr:      "127.0.0.1:0",
		MetricsToken:     "test-token",
	}
}

func TestAgent_LifecycleClaimsRequestedAndServesObservability(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	store := newRecorderStore()
	agent, err := New(lifecycleConfig(), pool, store, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	// The dump anchor is disabled, so the only registry entry sleeps — swap
	// its run func for an instant success so the requested-claim loop has
	// something observable to execute.
	require.Len(t, agent.jobs, 1)
	agent.jobs[0].run = func(context.Context) (*Artifact, map[string]any, string, error) {
		return &Artifact{Key: "lifecycle", Bytes: 7, SHA256: "aa"}, nil, "", nil
	}
	// The skew check would exec a real pg_dump; the lifecycle under test is
	// the loops, not the binary.
	agent.skewWarning = nil

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	agent.Start(runCtx)
	defer agent.Stop()

	require.NotEmpty(t, agent.httpAddr, "the listener binds synchronously in Start")

	// /healthz answers while the database is reachable.
	resp, err := http.Get("http://" + agent.httpAddr + "/healthz")
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), `"ok"`)

	// /metrics honours the shared token contract end to end over the wire.
	req, _ := http.NewRequest(http.MethodGet, "http://"+agent.httpAddr+"/metrics", nil)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// An operator's manual request (the backend's INSERT) is claimed by the
	// poll loop and executed without any scheduler involvement.
	_, err = pool.Exec(ctx, `INSERT INTO backup_run (job, status, scheduled_for) VALUES ('dump','requested', now())`)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM backup_run ORDER BY id DESC LIMIT 1`).Scan(&status); err != nil {
			return false
		}
		return status == "succeeded"
	}, 15*time.Second, 200*time.Millisecond,
		"the requested row must be claimed by the poll loop and finish succeeded")

	// Stop is idempotent and leaves no goroutine holding the WaitGroup.
	agent.Stop()
	agent.Stop()
}

func TestWaitReady_GatesOnDatabaseAndStore(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	store := newRecorderStore()
	a := &Agent{cfg: lifecycleConfig(), pool: pool, store: store, runs: NewRunStore(pool),
		metrics: NewMetrics(), logger: slog.New(slog.DiscardHandler)}

	assert.True(t, a.waitReady(ctx), "live db + answering store means ready")

	// A store that errors keeps catch-up waiting instead of minting an
	// immediate failed(upload_failed) row at compose-up.
	store.walkErr = io.ErrClosedPipe
	shortCtx, done := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer done()
	assert.False(t, a.waitReady(shortCtx), "an unreachable store must hold catch-up, not fail it")
	store.walkErr = nil

	// A store with objects proves readiness through the early-stop sentinel.
	store.listing = []ObjectInfo{{Key: dumpKeyPrefix + "2026/01/01/x.dump.age", Size: 1}}
	assert.True(t, a.waitReady(ctx))
}

func TestVersionSkewWarning_FlagsAMismatchedClient(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	// A fake pg_dump ahead of the real one on PATH: version 99 against the
	// containerized server, which is exactly the drift the warning names.
	dir := t.TempDir()
	script := filepath.Join(dir, "pg_dump")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho 'pg_dump (PostgreSQL) 99.1'\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	job, err := NewDumpJob(Config{AllowPlaintext: true}, pool, newRecorderStore(), testLogger())
	require.NoError(t, err)
	warning := job.VersionSkewWarning(ctx)
	require.NotEmpty(t, warning, "a major mismatch must be named at boot")
	assert.Contains(t, warning, "99")

	// Matching majors stay silent — the warning is for drift, not noise.
	var server string
	require.NoError(t, pool.QueryRow(ctx, `SHOW server_version`).Scan(&server))
	require.NoError(t, os.WriteFile(script,
		[]byte(fmt.Sprintf("#!/bin/sh\necho 'pg_dump (PostgreSQL) %s'\n", server)), 0o755))
	assert.Empty(t, job.VersionSkewWarning(ctx))
}
