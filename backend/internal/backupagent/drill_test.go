package backupagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDrillRuns stands in for RunStore: the unit tests have no database, and
// the pick/link steps are exactly the seam the drillRuns interface exists for.
type fakeDrillRuns struct {
	src     *DumpRunRef
	pickErr error
	linked  [][2]int64
	linkErr error
}

func (f *fakeDrillRuns) LatestSucceededDump(context.Context) (*DumpRunRef, error) {
	if f.pickErr != nil {
		return nil, f.pickErr
	}
	return f.src, nil
}

func (f *fakeDrillRuns) SetDrillSource(_ context.Context, runID, sourceRunID int64) error {
	f.linked = append(f.linked, [2]int64{runID, sourceRunID})
	return f.linkErr
}

// execRecorder is the drill's command seam for tests: it records every
// invocation and substitutes cheap shell stand-ins, so the pipeline order and
// the teardown are provable without initdb/pg_ctl on the host.
type execRecorder struct {
	mu    sync.Mutex
	calls [][]string
	// fail maps a binary name to a stderr line; that step then exits 1.
	fail map[string]string
	// wantPlain, when set, turns the pg_restore stand-in into a grep over its
	// input file — proving the DECRYPTED dump is what restore was pointed at.
	wantPlain string
}

func (r *execRecorder) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{name}, args...))
	r.mu.Unlock()
	if msg, ok := r.fail[name]; ok {
		return exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("echo %q >&2; exit 1", msg))
	}
	if name == "pg_restore" && r.wantPlain != "" {
		file := args[len(args)-1]
		return exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("grep -q %q %q", r.wantPlain, file))
	}
	return exec.CommandContext(ctx, "true")
}

func (r *execRecorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		name := c[0]
		if name == "pg_ctl" && len(c) > 1 {
			name += " " + c[1]
		}
		names = append(names, name)
	}
	return names
}

func (r *execRecorder) call(name string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c[0] == name {
			return c
		}
	}
	return nil
}

// drillFixture wires a full happy-path drill against stubs: a real age
// identity on disk, a really encrypted artifact in the recorder store, and a
// readCounts stub answering what the source meta expects.
type drillFixture struct {
	job    *DrillJob
	runs   *fakeDrillRuns
	store  *recorderStore
	rec    *execRecorder
	spool  string
	plain  string
	cipher []byte
}

func newDrillFixture(t *testing.T) *drillFixture {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	idFile := filepath.Join(t.TempDir(), "identity.txt")
	require.NoError(t, os.WriteFile(idFile, []byte(identity.String()+"\n"), 0o600))

	plain := "PGDMP-fake-custom-format-payload"
	var cipher bytes.Buffer
	w, err := age.Encrypt(&cipher, identity.Recipient())
	require.NoError(t, err)
	_, err = w.Write([]byte(plain))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	key := "backups/dump/2026/08/20/foldex-20260820-033000.dump.age"
	store := newRecorderStore()
	store.uploads[key] = cipher.Bytes()
	sum := sha256.Sum256(cipher.Bytes())

	runs := &fakeDrillRuns{src: &DumpRunRef{
		ID:     7,
		Key:    key,
		SHA256: hex.EncodeToString(sum[:]),
		Meta: map[string]any{
			"tables":         map[string]any{"link": float64(3), "note": float64(1)},
			"schema_version": float64(40),
		},
	}}

	spool := t.TempDir()
	job, err := NewDrillJob(Config{PGUser: "user_foldex", AgeIdentityFile: idFile, SpoolDir: spool}, runs, store, testLogger())
	require.NoError(t, err)
	rec := &execRecorder{wantPlain: plain}
	job.command = rec.command
	job.readCounts = func(context.Context, string, string) (map[string]int64, int64, error) {
		return map[string]int64{"link": 3, "note": 1}, 40, nil
	}
	return &drillFixture{job: job, runs: runs, store: store, rec: rec, spool: spool, plain: plain, cipher: cipher.Bytes()}
}

// leftovers returns what the drill left under its spool dir — the teardown
// contract says: nothing, on every path.
func (f *drillFixture) leftovers(t *testing.T) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(f.spool)
	require.NoError(t, err)
	return entries
}

func TestDrillRun_FullPipelineDownloadsVerifiesRestoresAndCleansUp(t *testing.T) {
	f := newDrillFixture(t)

	artifact, meta, reason, err := f.job.Run(context.Background(), 42)
	require.NoError(t, err)
	assert.Empty(t, reason)
	assert.Nil(t, artifact, "a drill ships nothing — its product is the verdict")

	// The source linkage lands the moment the dump is picked.
	assert.Equal(t, [][2]int64{{42, 7}}, f.runs.linked)

	// The cluster lifecycle, in order — and the pg_restore stand-in greps the
	// DECRYPTED plaintext out of the file it was handed, so a wrong or
	// undecrypted input would have failed the run.
	assert.Equal(t, []string{"initdb", "pg_ctl start", "createdb", "pg_restore", "pg_ctl stop"}, f.rec.names())

	initdb := f.rec.call("initdb")
	assert.Contains(t, initdb, "--locale=C")
	assert.Contains(t, initdb, "--encoding=UTF8")
	assert.Contains(t, initdb, "--username=user_foldex",
		"same superuser as production — ownership in the artifact restores without remapping")

	start := f.rec.call("pg_ctl")
	opts := start[len(start)-1]
	for _, knob := range []string{"listen_addresses=''", "fsync=off", "shared_buffers=64MB", "max_connections=10", "autovacuum=off", "unix_socket_directories="} {
		assert.Contains(t, opts, knob)
	}

	createdb := f.rec.call("createdb")
	assert.Contains(t, createdb, "--template=template0",
		"the artifact carries no CREATE DATABASE — template0 decouples it from the source cluster's locale")

	restore := f.rec.call("pg_restore")
	assert.Contains(t, restore, "--jobs=1")
	// The ephemeral cluster has exactly ONE role. Every `ALTER ... OWNER TO`
	// and `GRANT` in the archive names a role that deliberately does not exist
	// here, and --exit-on-error turns the first one into a failed drill for a
	// difference the drill itself created. Observed in production against a
	// managed Postgres: every drill reported drill_restore_failed on
	// `role "postgres" does not exist`, so the instance carried a green dump
	// and a restore that had never once been proven.
	assert.Contains(t, restore, "--no-owner")
	assert.Contains(t, restore, "--no-privileges")
	assert.Contains(t, restore, "--exit-on-error",
		"skipping owners and grants must not soften the verdict on everything else")

	// The verified counts are the meta — what the admin surface renders as
	// "the counts that proved the restore".
	assert.EqualValues(t, 7, meta["source_run_id"])
	assert.Equal(t, map[string]int64{"link": 3, "note": 1}, meta["tables"])
	assert.EqualValues(t, 40, meta["schema_version"])

	assert.Empty(t, f.leftovers(t), "teardown removes the whole ephemeral cluster")
}

func TestDrillRun_NoDumpToValidate(t *testing.T) {
	f := newDrillFixture(t)
	f.runs.pickErr = ErrNoDumpToDrill

	_, _, reason, err := f.job.Run(context.Background(), 1)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillNoDump, reason)
	assert.Empty(t, f.rec.calls, "with nothing to validate, no cluster is ever started")
}

func TestDrillRun_DownloadFailure(t *testing.T) {
	f := newDrillFixture(t)
	f.store.openErr = fmt.Errorf("connection reset")

	_, _, reason, err := f.job.Run(context.Background(), 1)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillDownloadFailed, reason)
	assert.Empty(t, f.rec.calls)
	assert.Empty(t, f.leftovers(t), "teardown runs on the failure path too")
}

func TestDrillRun_DigestMismatchStopsBeforeAnyRestore(t *testing.T) {
	f := newDrillFixture(t)
	// The bucket's bytes changed after the record was written — bitrot or
	// tampering. The recorded sha256 no longer matches what downloads.
	f.store.uploads[f.runs.src.Key] = append([]byte{0x00}, f.cipher...)

	_, _, reason, err := f.job.Run(context.Background(), 1)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillDigestMismatch, reason)
	assert.Empty(t, f.rec.calls, "unverified bytes must never reach pg_restore")
	assert.Empty(t, f.leftovers(t))
}

func TestDrillRun_DecryptFailures(t *testing.T) {
	t.Run("wrong identity", func(t *testing.T) {
		f := newDrillFixture(t)
		other, err := age.GenerateX25519Identity()
		require.NoError(t, err)
		f.job.identities = []age.Identity{other}

		_, _, reason, runErr := f.job.Run(context.Background(), 1)
		require.Error(t, runErr)
		assert.Equal(t, ReasonDrillDecryptFailed, reason)
		assert.Empty(t, f.leftovers(t))
	})

	t.Run("encrypted artifact with no identity configured", func(t *testing.T) {
		f := newDrillFixture(t)
		f.job.identities = nil

		_, _, reason, runErr := f.job.Run(context.Background(), 1)
		require.Error(t, runErr)
		assert.Equal(t, ReasonDrillDecryptFailed, reason)
		assert.NotContains(t, runErr.Error(), "AGE-SECRET-KEY", "never any key material in errors")
	})
}

func TestDrillRun_PlaintextArtifactSkipsDecrypt(t *testing.T) {
	f := newDrillFixture(t)
	// A .dump with no .age suffix: the BACKUP_ALLOW_PLAINTEXT deployment
	// shape. What was uploaded is what pg_restore receives.
	key := "backups/dump/2026/08/21/foldex-20260821-033000.dump"
	f.store.uploads[key] = []byte(f.plain)
	sum := sha256.Sum256([]byte(f.plain))
	f.runs.src.Key = key
	f.runs.src.SHA256 = hex.EncodeToString(sum[:])
	f.job.identities = nil

	_, _, reason, err := f.job.Run(context.Background(), 1)
	require.NoError(t, err, "plaintext mode must not demand an identity")
	assert.Empty(t, reason)
	assert.Contains(t, f.rec.names(), "pg_restore")
}

func TestDrillRun_RestoreFailureTearsDownTheStartedCluster(t *testing.T) {
	f := newDrillFixture(t)
	f.rec.fail = map[string]string{"pg_restore": "pg_restore: error: unsupported version"}

	_, _, reason, err := f.job.Run(context.Background(), 1)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillRestoreFailed, reason)
	// The cluster HAD started, so teardown must stop it — and the raw stderr
	// stays in the error (log side), never in the reason (backup_run side).
	assert.Equal(t, []string{"initdb", "pg_ctl start", "createdb", "pg_restore", "pg_ctl stop"}, f.rec.names())
	assert.Empty(t, f.leftovers(t), "a failed restore must not leak the ephemeral cluster")
}

func TestDrillRun_InitFailureNeverStopsWhatNeverStarted(t *testing.T) {
	f := newDrillFixture(t)
	f.rec.fail = map[string]string{"initdb": "initdb: cannot be run as root"}

	_, _, reason, err := f.job.Run(context.Background(), 1)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillRestoreFailed, reason)
	assert.Equal(t, []string{"initdb"}, f.rec.names(),
		"pg_ctl stop against a data dir that never started would only add a second error")
	assert.Empty(t, f.leftovers(t))
}

func TestDrillRun_CountsMismatch(t *testing.T) {
	f := newDrillFixture(t)
	f.job.readCounts = func(context.Context, string, string) (map[string]int64, int64, error) {
		return map[string]int64{"link": 2, "note": 1}, 40, nil
	}

	_, _, reason, err := f.job.Run(context.Background(), 1)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillCountsMismatch, reason)
	assert.Empty(t, f.leftovers(t))
}

func compareRaw(t *testing.T, raw map[string]any, got map[string]int64, ver int64) error {
	t.Helper()
	meta, err := parseDumpMeta(raw)
	if err != nil {
		return err
	}
	return compareCounts(meta, got, ver)
}

func TestCompareCounts_Verdicts(t *testing.T) {
	meta := map[string]any{
		"tables":         map[string]any{"link": float64(3), "note": float64(1)},
		"schema_version": float64(40),
	}
	got := map[string]int64{"link": 3, "note": 1}

	assert.NoError(t, compareRaw(t, meta, got, 40))

	err := compareRaw(t, meta, map[string]int64{"link": 2, "note": 1}, 40)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "link")

	err = compareRaw(t, meta, map[string]int64{"note": 1}, 40)
	require.Error(t, err, "a table counted at dump time but absent from the restore is a lost table, not a pass")

	err = compareRaw(t, meta, got, 39)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema_migrations")

	assert.NoError(t, compareRaw(t, map[string]any{}, got, 40),
		"a dump whose meta predates the counts is compared on nothing — restore success is all it can prove")
	assert.NoError(t, compareRaw(t, map[string]any{"schema_version": float64(40)}, map[string]int64{}, 40))

	err = compareRaw(t, map[string]any{"tables": map[string]any{"link": "three"}}, got, 40)
	require.Error(t, err, "an unreadable recorded count is a mismatch, never a silent skip")
}

// TestCompareCounts_RefusesAnUnexpectedTablesShape is BP-MEN-006: a type
// assert to map[string]any treated every other tables shape (the dump's own
// map[string]int64, a JSON array, a string) as "no counts, pass". Wrong
// shape is a refusal, not a silent success.
func TestCompareCounts_RefusesAnUnexpectedTablesShape(t *testing.T) {
	got := map[string]int64{"link": 3}

	_, err := parseDumpMeta(map[string]any{
		"tables":         []any{"link"},
		"schema_version": float64(40),
	})
	require.Error(t, err, "an unexpected tables shape must not fail-open as a silent pass")

	err = compareRaw(t, map[string]any{
		"tables":         "link=3",
		"schema_version": float64(40),
	}, got, 40)
	require.Error(t, err, "a string tables field is not 'no counts'")

	// dump.go writes map[string]int64 into the open meta map. That is not
	// map[string]any; asserting the JSONB shape fail-opened the native one.
	err = compareRaw(t, map[string]any{
		"tables":         map[string]int64{"link": 3},
		"schema_version": int64(40),
	}, map[string]int64{"link": 999}, 40)
	require.Error(t, err, "the dump's native tables map must still be compared")
}

func TestMetaInt_CoercesEveryJSONBShape(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want int64
		ok   bool
	}{
		{int64(3), 3, true},
		{3, 3, true},
		{float64(3), 3, true},
		{json.Number("3"), 3, true},
		{"3", 0, false},
		{nil, 0, false},
	} {
		got, ok := metaInt(tc.in)
		assert.Equal(t, tc.ok, ok, "%T", tc.in)
		if tc.ok {
			assert.Equal(t, tc.want, got)
		}
	}
}

func TestLoadIdentityFile_FileContract(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)

	t.Run("parses a real identity file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "id.txt")
		require.NoError(t, os.WriteFile(path, []byte("# created: today\n"+identity.String()+"\n"), 0o600))
		ids, err := loadIdentityFile(path)
		require.NoError(t, err)
		assert.Len(t, ids, 1)
	})

	t.Run("missing file fails the boot, not the first drill", func(t *testing.T) {
		_, err := loadIdentityFile(filepath.Join(t.TempDir(), "nope.txt"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BACKUP_AGE_IDENTITY_FILE")
	})

	t.Run("garbage is rejected without being echoed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "id.txt")
		require.NoError(t, os.WriteFile(path, []byte("totally-not-an-identity"), 0o600))
		_, err := loadIdentityFile(path)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "totally-not-an-identity",
			"file content never reaches logs — the likely paste mistake here is a private key")
	})
}

func TestNewDrillJob_BadIdentityFileFailsConstruction(t *testing.T) {
	_, err := NewDrillJob(Config{AgeIdentityFile: filepath.Join(t.TempDir(), "absent.txt")}, &fakeDrillRuns{}, newRecorderStore(), testLogger())
	require.Error(t, err, "a misconfigured drill must fail at boot (keyfile posture), not weeks later at 04:30")
}

func TestDrill_LinkageFailureDoesNotAbortTheValidation(t *testing.T) {
	f := newDrillFixture(t)
	f.runs.linkErr = io.ErrClosedPipe
	_, _, reason, err := f.job.Run(context.Background(), 42)
	require.NoError(t, err, "losing the drill_of_run_id column linkage is not worth abandoning the validation")
	assert.Empty(t, reason)
}

func TestDrill_RestoreRunsWithExitOnError(t *testing.T) {
	f := newDrillFixture(t)
	_, _, _, err := f.job.Run(context.Background(), 42)
	require.NoError(t, err)
	restore := f.rec.call("pg_restore")
	require.NotNil(t, restore)
	assert.Contains(t, restore, "--exit-on-error",
		"without it a partially corrupted dump restores with errors swallowed and the drill blesses it")
	start := f.rec.call("pg_ctl")
	require.NotNil(t, start)
	assert.Contains(t, strings.Join(start, " "), "-c port="+drillPort,
		"the explicit port is the guard against a stray PGPORT pointing clients at a socket the server never opened")
}

func TestDrill_ReadCountsFailureIsARestoreFailure(t *testing.T) {
	f := newDrillFixture(t)
	f.job.readCounts = func(context.Context, string, string) (map[string]int64, int64, error) {
		return nil, 0, io.ErrClosedPipe
	}
	_, _, reason, err := f.job.Run(context.Background(), 42)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillRestoreFailed, reason,
		"an unanswerable restored cluster is a restore problem, not a counts mismatch")
}

func TestDrill_SourceInfraFailureIsNotNoDump(t *testing.T) {
	f := newDrillFixture(t)
	f.runs.pickErr = io.ErrClosedPipe
	_, _, reason, err := f.job.Run(context.Background(), 42)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillSourceFailed, reason,
		"a database that cannot answer must not be reported as \"nothing to validate\"")

	f2 := newDrillFixture(t)
	f2.runs.pickErr = ErrNoDumpToDrill
	f2.runs.src = nil
	_, _, reason, err = f2.job.Run(context.Background(), 42)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillNoDump, reason)
}

func TestDrill_RefusesAPlaintextKeyWhenTheInstanceEncrypts(t *testing.T) {
	f := newDrillFixture(t)
	// artifact_key is a database column: anyone with UPDATE on backup_run can
	// strip the .age suffix. With identities configured, that must be a
	// refusal, not a silent downgrade to restoring unauthenticated bytes.
	plainKey := "backups/dump/2026/08/20/foldex-20260820-033000.dump"
	f.store.uploads[plainKey] = []byte("PGDMP-unauthenticated")
	sum := sha256.Sum256([]byte("PGDMP-unauthenticated"))
	f.runs.src.Key = plainKey
	f.runs.src.SHA256 = hex.EncodeToString(sum[:])

	_, _, reason, err := f.job.Run(context.Background(), 42)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillDecryptFailed, reason)
	assert.Empty(t, f.rec.names(), "no ephemeral cluster may even be created for unauthenticated bytes")
}

func TestDrill_StartTimeoutStillStopsTheCluster(t *testing.T) {
	f := newDrillFixture(t)
	// The recorder fails by BINARY name, so both pg_ctl invocations exit 1 —
	// which is exactly the scenario: a start that died on its --timeout and a
	// stop whose error is logged, never skipped.
	f.rec.fail = map[string]string{"pg_ctl": "start: timed out waiting for server"}
	_, _, reason, err := f.job.Run(context.Background(), 42)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillRestoreFailed, reason)
	// The stop must be attempted even though start FAILED: a start that dies
	// on its --timeout can leave a postmaster still coming up, and skipping
	// the stop is exactly the path that RemoveAlls under a live server.
	assert.Equal(t, []string{"initdb", "pg_ctl start", "pg_ctl stop"}, f.rec.names())
}

func TestLoadIdentityFile_RefusesAWorldReadableFile(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	idFile := filepath.Join(t.TempDir(), "identity.txt")
	require.NoError(t, os.WriteFile(idFile, []byte(identity.String()+"\n"), 0o644))
	_, err = loadIdentityFile(idFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chmod 600",
		"the identity decrypts EVERY backup — group/world bits are a configuration error caught at boot")
	assert.NotContains(t, err.Error(), identity.String())
}

func TestExecCommand_ChildrenNeverSeeTheAgentsSecrets(t *testing.T) {
	t.Setenv("BACKUP_S3_SECRET_KEY", "leakme")
	t.Setenv("BACKUP_AGE_IDENTITY_FILE", "/secrets/id.txt")
	t.Setenv("BACKUP_AGE_IDENTITY", "AGE-SECRET-KEY-1LEAKME")
	cmd := execCommand(context.Background(), "true")
	joined := strings.Join(cmd.Env, "\n")
	assert.NotContains(t, joined, "leakme")
	assert.NotContains(t, joined, "AGE-SECRET-KEY-1LEAKME",
		"the inline identity must not reach pg_restore's children either")
	assert.NotContains(t, joined, "BACKUP_AGE_IDENTITY_FILE",
		"pg_restore executes bytes from the bucket; its children must not inherit what INV-171 isolates")
	assert.Contains(t, joined, "PATH=")
}

func TestSweepOrphanDrillDirs_RemovesOnlyDrillLeftovers(t *testing.T) {
	spool := t.TempDir()
	orphan := filepath.Join(spool, "foldex-drill-dead123")
	require.NoError(t, os.MkdirAll(filepath.Join(orphan, "data"), 0o700))
	bystander := filepath.Join(spool, "someone-elses-file")
	require.NoError(t, os.WriteFile(bystander, []byte("x"), 0o600))

	sweepOrphanDrillDirs(spool, testLogger())
	_, err := os.Stat(orphan)
	assert.True(t, os.IsNotExist(err), "a SIGKILLed drill's multi-GB corpse has no other janitor")
	_, err = os.Stat(bystander)
	assert.NoError(t, err, "the sweep never touches what it did not create")
}
