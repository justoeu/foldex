package backupagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorderStore captures uploads and serves a canned listing — the Uploader
// contract without RustFS or S3.
type recorderStore struct {
	mu       sync.Mutex
	uploads  map[string][]byte
	listing  []ObjectInfo
	putErr   error
	deleted  [][]string
	walkErr  error
	deleteEr error
}

func newRecorderStore() *recorderStore {
	return &recorderStore{uploads: map[string][]byte{}}
}

func (r *recorderStore) PutObjectStream(_ context.Context, key string, reader io.Reader, size int64, _ string) error {
	if r.putErr != nil {
		return r.putErr
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return io.ErrShortWrite
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.uploads[key] = data
	return nil
}

func (r *recorderStore) WalkObjects(_ context.Context, prefix string, visit func(ObjectInfo) error) error {
	if r.walkErr != nil {
		return r.walkErr
	}
	for _, o := range r.listing {
		if strings.HasPrefix(o.Key, prefix) {
			if err := visit(o); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *recorderStore) DeleteObjects(_ context.Context, keys []string) error {
	if r.deleteEr != nil {
		return r.deleteEr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, keys)
	return nil
}

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// stubDump swaps pg_dump for a shell command, so the pipeline — encrypt, hash,
// spool, upload, prune — is provable on any machine. Production always uses
// pgDumpCommand; the seam exists exactly for this file.
func stubDump(script string) func(context.Context, Config) *exec.Cmd {
	return func(ctx context.Context, _ Config) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
}

func newTestDumpJob(t *testing.T, store *recorderStore, recipients []string) *DumpJob {
	t.Helper()
	cfg := Config{
		AgeRecipients: recipients,
		RetainDaily:   7, RetainWeekly: 4, RetainMonthly: 6,
		RetentionMode: "agent",
	}
	job, err := NewDumpJob(cfg, nil, store, testLogger())
	require.NoError(t, err)
	return job
}

func TestDumpRun_ShipsAnEncryptedVerifiableArtifact(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	store := newRecorderStore()
	job := newTestDumpJob(t, store, []string{identity.Recipient().String()})
	job.command = stubDump("printf 'PGDMP-fake-custom-format-payload'")
	fixed := time.Date(2026, 8, 25, 3, 30, 0, 0, time.UTC)
	job.now = func() time.Time { return fixed }

	artifact, meta, reason, runErr := job.Run(context.Background())
	require.NoError(t, runErr)
	assert.Empty(t, reason)
	require.NotNil(t, artifact)

	assert.Equal(t, "backups/dump/2026/08/25/foldex-20260825-033000.dump.age", artifact.Key)
	uploaded, ok := store.uploads[artifact.Key]
	require.True(t, ok, "the artifact must actually reach the store")
	assert.Equal(t, int64(len(uploaded)), artifact.Bytes)

	// The recorded sha is of the CIPHERTEXT — what sha256sum sees in the
	// bucket. Hashing the plaintext instead would make the one externally
	// checkable number unverifiable.
	sum := sha256.Sum256(uploaded)
	assert.Equal(t, hex.EncodeToString(sum[:]), artifact.SHA256)
	assert.NotContains(t, string(uploaded), "PGDMP-fake", "plaintext must not reach the bucket")

	// And the stored bytes round-trip through standard age.
	r, err := age.Decrypt(bytes.NewReader(uploaded), identity)
	require.NoError(t, err)
	plain, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "PGDMP-fake-custom-format-payload", string(plain))

	assert.Equal(t, true, meta["encrypted"])
}

func TestDumpRun_PlaintextModeStillHashesWhatItShips(t *testing.T) {
	store := newRecorderStore()
	job := newTestDumpJob(t, store, nil)
	job.command = stubDump("printf 'PGDMP-plain'")

	artifact, _, reason, err := job.Run(context.Background())
	require.NoError(t, err)
	assert.Empty(t, reason)
	assert.True(t, strings.HasSuffix(artifact.Key, ".dump"), "no .age suffix when nothing is encrypted: %s", artifact.Key)
	assert.Equal(t, "PGDMP-plain", string(store.uploads[artifact.Key]))
}

func TestDumpRun_FailuresMapToNormalizedReasons(t *testing.T) {
	t.Run("pg_dump exits nonzero", func(t *testing.T) {
		store := newRecorderStore()
		job := newTestDumpJob(t, store, nil)
		job.command = stubDump("printf 'partial'; echo 'connection to server failed' >&2; exit 1")

		_, _, reason, err := job.Run(context.Background())
		require.Error(t, err)
		assert.Equal(t, ReasonDumpFailed, reason)
		assert.Empty(t, store.uploads, "a failed dump must never upload its partial output")
	})

	t.Run("empty output is not a backup", func(t *testing.T) {
		store := newRecorderStore()
		job := newTestDumpJob(t, store, nil)
		job.command = stubDump("true")

		_, _, reason, err := job.Run(context.Background())
		require.Error(t, err)
		assert.Equal(t, ReasonDumpFailed, reason)
		assert.Empty(t, store.uploads)
	})

	t.Run("upload failure", func(t *testing.T) {
		store := newRecorderStore()
		store.putErr = io.ErrClosedPipe
		job := newTestDumpJob(t, store, nil)
		job.command = stubDump("printf 'PGDMP'")

		_, _, reason, err := job.Run(context.Background())
		require.Error(t, err)
		assert.Equal(t, ReasonUploadFailed, reason)
	})
}

func TestDumpRun_PruneRunsOnlyInAgentModeAndNeverFailsTheRun(t *testing.T) {
	// Two consecutive days: with Daily=1 the newer survives and the older is
	// the prune victim. A single listed day would be its own daily slot and
	// nothing would ever be prunable.
	old := dumpKey(time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC), false)
	newer := dumpKey(time.Date(2026, 1, 3, 3, 0, 0, 0, time.UTC), false)
	t.Run("agent mode prunes", func(t *testing.T) {
		store := newRecorderStore()
		store.listing = []ObjectInfo{{Key: old, Size: 10}, {Key: newer, Size: 10}}
		job := newTestDumpJob(t, store, nil)
		job.cfg.RetainDaily, job.cfg.RetainWeekly, job.cfg.RetainMonthly = 1, 0, 0
		job.command = stubDump("printf 'PGDMP'")

		_, meta, _, err := job.Run(context.Background())
		require.NoError(t, err)
		require.Len(t, store.deleted, 1)
		assert.Contains(t, store.deleted[0], old)
		assert.EqualValues(t, 1, meta["pruned_objects"])
	})

	t.Run("bucket mode never deletes", func(t *testing.T) {
		store := newRecorderStore()
		store.listing = []ObjectInfo{{Key: old, Size: 10}, {Key: newer, Size: 10}}
		job := newTestDumpJob(t, store, nil)
		job.cfg.RetentionMode = "bucket"
		job.command = stubDump("printf 'PGDMP'")

		_, _, _, err := job.Run(context.Background())
		require.NoError(t, err)
		assert.Empty(t, store.deleted, "bucket mode: lifecycle owns expiry, the agent's prune is a declared no-op")
	})

	t.Run("prune failure downgrades to run metadata", func(t *testing.T) {
		store := newRecorderStore()
		store.listing = []ObjectInfo{{Key: old, Size: 10}, {Key: newer, Size: 10}}
		store.deleteEr = io.ErrClosedPipe
		job := newTestDumpJob(t, store, nil)
		job.cfg.RetainDaily = 1
		job.command = stubDump("printf 'PGDMP'")

		artifact, meta, reason, err := job.Run(context.Background())
		require.NoError(t, err, "the dump landed; a failed prune must not turn success into failure")
		assert.Empty(t, reason)
		assert.NotNil(t, artifact)
		assert.Equal(t, ReasonPruneFailed, meta["prune_error"])
	})
}

func TestMajor_ParsesRealVersionStrings(t *testing.T) {
	assert.Equal(t, "18", major("18.4"))
	assert.Equal(t, "18", major("pg_dump (PostgreSQL) 18.4"))
	assert.Equal(t, "17", major("17.2 (Debian 17.2-1)"))
	assert.Equal(t, "", major("garbage"))
}

func TestPgDumpCommand_PasswordTravelsInEnvNeverArgv(t *testing.T) {
	cfg := Config{PGHost: "db", PGPort: 5432, PGUser: "user_foldex",
		PGPassword: "sup3r-secret", PGDatabase: "foldex", PGSSLMode: "disable"}
	cmd := pgDumpCommand(context.Background(), cfg)

	for _, arg := range cmd.Args {
		assert.NotContains(t, arg, "sup3r-secret",
			"argv is world-readable in /proc — the password must never appear there")
	}
	assert.Contains(t, cmd.Args, "--format=custom")
	assert.NotContains(t, cmd.Args, "-C", "no CREATE DATABASE in the artifact (SDD §5.1)")
	assert.Contains(t, cmd.Args, "foldex")

	var sawPassword, sawSSL bool
	for _, kv := range cmd.Env {
		if kv == "PGPASSWORD=sup3r-secret" {
			sawPassword = true
		}
		if kv == "PGSSLMODE=disable" {
			sawSSL = true
		}
	}
	assert.True(t, sawPassword, "the password reaches pg_dump through the environment")
	assert.True(t, sawSSL)
}
