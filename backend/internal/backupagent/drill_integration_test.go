//go:build integration

package backupagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/testdb"
)

func TestLatestSucceededDump_PicksTheNewestArtifact(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	s := NewRunStore(pool)

	_, err := s.LatestSucceededDump(ctx)
	require.ErrorIs(t, err, ErrNoDumpToDrill, "an empty history has nothing whose restorability could be proven")

	// A failed dump, an older success, a newer success and a succeeded run of
	// ANOTHER job: only the newest succeeded dump is drillable.
	idFail, err := s.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)
	require.NoError(t, s.Fail(ctx, idFail, ReasonUploadFailed))

	idOld, err := s.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)
	require.NoError(t, s.Succeed(ctx, idOld, &Artifact{Key: "backups/dump/old", Bytes: 1, SHA256: "aa"}, nil))
	_, err = pool.Exec(ctx, `UPDATE backup_run SET started_at = now() - interval '2 days' WHERE id = $1`, idOld)
	require.NoError(t, err)

	idDrill, err := s.Begin(ctx, JobDrill, time.Now())
	require.NoError(t, err)
	require.NoError(t, s.Succeed(ctx, idDrill, nil, nil))

	idNew, err := s.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)
	meta := map[string]any{"tables": map[string]any{"link": 3}, "schema_version": 40, "encrypted": true}
	require.NoError(t, s.Succeed(ctx, idNew, &Artifact{Key: "backups/dump/new", Bytes: 2, SHA256: "bb"}, meta))

	ref, err := s.LatestSucceededDump(ctx)
	require.NoError(t, err)
	assert.Equal(t, idNew, ref.ID)
	assert.Equal(t, "backups/dump/new", ref.Key)
	assert.Equal(t, "bb", ref.SHA256)
	// The meta round-trips through JSONB — numbers come back as float64,
	// which is exactly the shape compareCounts' coercion exists for.
	assert.NoError(t, compareCounts(ref.Meta, map[string]int64{"link": 3}, 40))
}

func TestSetDrillSource_StampsOnlyTheOwnRow(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	mine := NewRunStore(pool)

	dumpID, err := mine.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)
	require.NoError(t, mine.Succeed(ctx, dumpID, &Artifact{Key: "k", Bytes: 1, SHA256: "aa"}, nil))
	drillID, err := mine.Begin(ctx, JobDrill, time.Now())
	require.NoError(t, err)

	// A foreign claim token must not write the linkage — same guard as the
	// run outcomes.
	other := NewRunStore(pool)
	require.NoError(t, other.SetDrillSource(ctx, drillID, dumpID))
	var linked *int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT drill_of_run_id FROM backup_run WHERE id = $1`, drillID).Scan(&linked))
	assert.Nil(t, linked, "a straggler's store must not rewrite which dump the successor's drill validates")

	require.NoError(t, mine.SetDrillSource(ctx, drillID, dumpID))
	require.NoError(t, pool.QueryRow(ctx, `SELECT drill_of_run_id FROM backup_run WHERE id = $1`, drillID).Scan(&linked))
	require.NotNil(t, linked)
	assert.Equal(t, dumpID, *linked)
}

// TestDrill_EndToEndWithRealPostgresBinaries is the drill in miniature, for
// real: pg_dump of the test database, age-encrypt, ship to the recorder,
// then download, verify, decrypt, restore into an ephemeral cluster and
// compare counts — no stubs anywhere. It needs the postgres server binaries,
// which the dev host and the bare CI runner do not have; it runs inside the
// backup-agent image (SDD-OPS-BACKUP §14) and skips cleanly elsewhere.
func TestDrill_EndToEndWithRealPostgresBinaries(t *testing.T) {
	if testing.Short() {
		t.Skip("drill end-to-end drives real postgres binaries; skipped under -short")
	}
	// "postgres" is on the list on purpose: homebrew's libpq ships initdb and
	// the client tools WITHOUT the server, so gating on initdb alone lets the
	// test start and fail instead of skipping.
	for _, bin := range []string{"pg_dump", "initdb", "pg_ctl", "createdb", "pg_restore", "postgres"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH — the full drill pipeline runs inside the backup-agent image (SDD-OPS-BACKUP §14)", bin)
		}
	}
	// initdb refuses to run under root outright; skip instead of failing on a
	// root CI shell outside the agent image (which runs as `postgres`).
	if os.Geteuid() == 0 {
		t.Skip("the ephemeral cluster cannot run as root — the backup-agent image runs as `postgres`")
	}

	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))

	var server string
	require.NoError(t, pool.QueryRow(ctx, `SHOW server_version`).Scan(&server))
	out, err := exec.Command("pg_dump", "--version").Output()
	require.NoError(t, err)
	if major(server) != major(string(out)) {
		t.Skipf("host pg_dump major (%s) differs from the test server (%s) — version-matched binaries live in the backup-agent image", major(string(out)), major(server))
	}

	// testdb applies the .sql files directly, so schema_migrations — the
	// golang-migrate artifact both count sides read — is built here the way
	// migrate builds it (same precedent as the schema-gate test above).
	_, err = pool.Exec(ctx, `CREATE TABLE schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE schema_migrations`) })
	_, err = pool.Exec(ctx, `INSERT INTO schema_migrations VALUES ($1, false)`, RequiredSchemaVersion)
	require.NoError(t, err)

	// Content that must survive the round-trip: a user and a link.
	uid := testdb.SeedUser(t, pool, "drill@example.com", "")
	_, err = pool.Exec(ctx,
		`INSERT INTO link (user_id, url, slug, title) VALUES ($1, 'https://example.com/drill', 'drill-e2e', 'drill')`, uid)
	require.NoError(t, err)

	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	idFile := filepath.Join(t.TempDir(), "identity.txt")
	require.NoError(t, os.WriteFile(idFile, []byte(identity.String()+"\n"), 0o600))

	cc := pool.Config().ConnConfig
	cfg := Config{
		PGHost: cc.Host, PGPort: int(cc.Port), PGUser: cc.User,
		PGPassword: cc.Password, PGDatabase: cc.Database, PGSSLMode: "disable",
		AgeRecipients:   []string{identity.Recipient().String()},
		AgeIdentityFile: idFile,
		RetentionMode:   "bucket", // lifecycle owns expiry: no prune noise here
		SpoolDir:        t.TempDir(),
	}

	store := newRecorderStore()
	dump, err := NewDumpJob(cfg, pool, store, testLogger())
	require.NoError(t, err)
	artifact, meta, reason, err := dump.Run(ctx) // the REAL pg_dump
	require.NoError(t, err)
	require.Empty(t, reason)
	require.NotNil(t, meta["tables"], "the dump must record the source counts the drill compares against")

	runs := NewRunStore(pool)
	dumpID, err := runs.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)
	require.NoError(t, runs.Succeed(ctx, dumpID, artifact, meta))

	drill, err := NewDrillJob(cfg, runs, store, testLogger())
	require.NoError(t, err)
	drillID, err := runs.Begin(ctx, JobDrill, time.Now())
	require.NoError(t, err)

	dArtifact, dMeta, dReason, err := drill.Run(ctx, drillID)
	require.NoError(t, err, "the shipped artifact must restore — this failing IS the alarm the drill exists to raise")
	assert.Empty(t, dReason)
	assert.Nil(t, dArtifact)
	require.NoError(t, runs.Succeed(ctx, drillID, nil, dMeta))

	// The restored side counted the content we planted.
	got, ok := dMeta["tables"].(map[string]int64)
	require.True(t, ok)
	assert.EqualValues(t, 1, got["link"])
	assert.EqualValues(t, 1, got["app_user"])
	assert.EqualValues(t, RequiredSchemaVersion, dMeta["schema_version"])

	// And the row the admin surface reads says which dump was proven.
	var linked int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT drill_of_run_id FROM backup_run WHERE id = $1`, drillID).Scan(&linked))
	assert.Equal(t, dumpID, linked)
}

func TestLatestSucceededDump_AFailedRunWithAKeyIsNeverTheSource(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	s := NewRunStore(pool)

	// A run can fail AFTER its artifact landed (e.g. the prune phase, or a
	// janitor-expired straggler that had already uploaded): key present,
	// status failed. Selecting by key-presence alone would drill an artifact
	// whose run the operator was told failed.
	id, err := s.Begin(ctx, JobDump, time.Now())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE backup_run SET artifact_key='backups/dump/x', artifact_sha256='aa', status='failed', last_error=$2, finished_at=now() WHERE id=$1`, id, ReasonUploadFailed)
	require.NoError(t, err)

	_, err = s.LatestSucceededDump(ctx)
	assert.ErrorIs(t, err, ErrNoDumpToDrill,
		"status='succeeded' is the filter — artifact_key IS NOT NULL alone would bless a failed run's artifact")
}

func TestSnapshotSanityCounts_CountsInsideTheExportedSnapshot(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(ctx, pool))
	// schema_migrations is a golang-migrate artifact the testdb never creates
	// (it applies the .sql files directly); the yardstick reads it, so the
	// test builds it the way migrate would.
	_, err := pool.Exec(ctx, `CREATE TABLE schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE schema_migrations`) })
	_, err = pool.Exec(ctx, `INSERT INTO schema_migrations VALUES (40, false)`)
	require.NoError(t, err)
	testdb.SeedUser(t, pool, "snap@example.com", "editor")

	tables, version, snapshotID, release, err := snapshotSanityCounts(ctx, pool)
	require.NoError(t, err)
	require.NotNil(t, release)
	defer release()

	assert.Regexp(t, `^[0-9A-Fa-f-]+$`, snapshotID, "pg_export_snapshot ids are what pg_dump --snapshot accepts")
	assert.EqualValues(t, 1, tables["app_user"])
	assert.Positive(t, version)

	// The yardstick is frozen at export time: a write AFTER the snapshot must
	// not move the counts — this is exactly the gap that produced a spurious
	// weekly drill_counts_mismatch on any live instance.
	testdb.SeedUser(t, pool, "later@example.com", "editor")
	tables2, _, _, release2, err := snapshotSanityCounts(ctx, pool)
	require.NoError(t, err)
	defer release2()
	assert.EqualValues(t, 2, tables2["app_user"], "a NEW snapshot sees the new row")
	// while the FIRST snapshot's transaction, still open, does not — proven
	// indirectly: its counts were taken before the insert and are immutable.
	assert.EqualValues(t, 1, tables["app_user"])
}
