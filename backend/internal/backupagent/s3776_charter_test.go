package backupagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3776_DrillRun_ShipsNothingAndStopsBeforeRestoreOnUnverifiedBytes(t *testing.T) {
	f := newDrillFixture(t)
	artifact, meta, reason, err := f.job.Run(context.Background(), 42)
	require.NoError(t, err)
	assert.Empty(t, reason)
	assert.Nil(t, artifact, "a drill ships nothing — its product is the verdict")
	assert.EqualValues(t, 7, meta["source_run_id"])
	assert.Contains(t, f.rec.names(), "pg_restore")
	assert.Contains(t, f.rec.call("pg_restore"), "--no-owner")
	assert.Contains(t, f.rec.call("pg_restore"), "--no-privileges")
	assert.Empty(t, f.leftovers(t))

	empty := newDrillFixture(t)
	empty.runs.pickErr = ErrNoDumpToDrill
	_, _, reason, err = empty.job.Run(context.Background(), 1)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillNoDump, reason)
	assert.Empty(t, empty.rec.calls, "with nothing to validate, no cluster is ever started")

	bitrot := newDrillFixture(t)
	bitrot.store.uploads[bitrot.runs.src.Key] = append([]byte{0x00}, bitrot.cipher...)
	_, _, reason, err = bitrot.job.Run(context.Background(), 1)
	require.Error(t, err)
	assert.Equal(t, ReasonDrillDigestMismatch, reason)
	assert.Empty(t, bitrot.rec.calls, "unverified bytes must never reach pg_restore")
}

func TestS3776_DrillRun_PlaintextSkipsDecrypt(t *testing.T) {
	f := newDrillFixture(t)
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

func TestS3776_MirrorRun_CopiesDeltaAndNeverPropagatesDeletions(t *testing.T) {
	now := time.Now()
	source := newRecorderStore()
	source.uploads["screens/1.png"] = []byte("one")
	source.uploads["screens/2.png"] = []byte("two2")
	source.listing = []ObjectInfo{
		{Key: "screens/1.png", Size: 3, LastModified: now},
		{Key: "screens/2.png", Size: 4, LastModified: now.Add(-48 * time.Hour)},
	}
	dest := newRecorderStore()
	dest.listing = []ObjectInfo{{Key: mirrorKeyPrefix + "screens/2.png", Size: 4}}

	job := newTestMirrorJob(source, dest)
	job.lastSuccess = func(context.Context) (time.Time, error) { return now.Add(-6 * time.Hour), nil }

	artifact, meta, reason, err := job.Run(context.Background())
	require.NoError(t, err)
	assert.Empty(t, reason)
	require.NotNil(t, artifact.Mirror)
	assert.Empty(t, artifact.Key, "the mirror ships a delta, not one artifact object")
	assert.Equal(t, []byte("one"), dest.uploads[mirrorKeyPrefix+"screens/1.png"])
	assert.NotContains(t, dest.uploads, mirrorKeyPrefix+"screens/2.png")
	assert.EqualValues(t, 2, artifact.Mirror.ObjectsScanned)
	assert.EqualValues(t, 1, artifact.Mirror.ObjectsCopied)
	assert.EqualValues(t, 1, meta["objects_skipped"])

	emptyOrigin := newRecorderStore()
	kept := newRecorderStore()
	kept.listing = []ObjectInfo{{Key: mirrorKeyPrefix + "screens/gone.png", Size: 9}}
	wipe, _, reason, err := newTestMirrorJob(emptyOrigin, kept).Run(context.Background())
	require.NoError(t, err)
	assert.Empty(t, reason)
	assert.EqualValues(t, 0, wipe.Mirror.ObjectsCopied)
	assert.Empty(t, kept.deleted, "a wipe at the origin must never delete the backup copy")
}
