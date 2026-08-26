package backupagent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestMirrorJob(source, dest *recorderStore) *MirrorJob {
	return &MirrorJob{
		source:               source,
		dest:                 dest,
		logger:               testLogger().With("job", JobMirror),
		lastSuccess:          func(context.Context) (time.Time, error) { return time.Time{}, nil },
		restoreBusy:          func(context.Context) (bool, error) { return false, nil },
		restoreProbeEvery:    time.Millisecond,
		restoreProbeDeadline: 10 * time.Millisecond,
	}
}

func TestMirrorWatermark_SubtractsTheOverlapAndKeepsZeroZero(t *testing.T) {
	assert.True(t, mirrorWatermark(time.Time{}).IsZero(),
		"never succeeded: zero watermark means everything is 'modified since' — a first full copy")

	last := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, last.Add(-time.Hour), mirrorWatermark(last),
		"the 1h overlap covers clock skew and objects written during the previous run's own listing")
}

func TestMirrorDelta_DecidesByPresenceSizeAndWatermarkNeverETag(t *testing.T) {
	watermark := time.Date(2026, 8, 25, 11, 0, 0, 0, time.UTC)
	before := watermark.Add(-time.Minute)
	after := watermark.Add(time.Minute)

	src := []ObjectInfo{
		{Key: "absent", Size: 10, LastModified: before},
		{Key: "same", Size: 10, ETag: "abc-1", LastModified: before},
		{Key: "size-differs", Size: 10, LastModified: before},
		{Key: "touched-after", Size: 10, LastModified: after},
		{Key: "touched-at", Size: 10, LastModified: watermark},
		// The ETag mutant detector: etags differ (multipart part sizes always
		// make them differ between uploaders), yet size matches and the
		// object is older than the watermark. A diff that consults ETag
		// copies this key forever; the correct diff skips it.
		{Key: "etag-differs-only", Size: 10, ETag: "aaaa-3", LastModified: before},
	}
	dst := map[string]ObjectInfo{
		"same":              {Key: "same", Size: 10, ETag: "abc-1"},
		"size-differs":      {Key: "size-differs", Size: 99},
		"touched-after":     {Key: "touched-after", Size: 10},
		"touched-at":        {Key: "touched-at", Size: 10},
		"etag-differs-only": {Key: "etag-differs-only", Size: 10, ETag: "bbbb-7"},
	}

	var keys []string
	for _, o := range mirrorDelta(src, dst, watermark, false) {
		keys = append(keys, o.Key)
	}
	assert.ElementsMatch(t, []string{"absent", "size-differs", "touched-after", "touched-at"}, keys)
}

func TestMirrorDelta_ZeroWatermarkCopiesEverything(t *testing.T) {
	src := []ObjectInfo{
		{Key: "a", Size: 1},
		{Key: "b", Size: 2, LastModified: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	dst := map[string]ObjectInfo{"a": {Key: "a", Size: 1}, "b": {Key: "b", Size: 2}}
	assert.Len(t, mirrorDelta(src, dst, time.Time{}, false), 2,
		"first run: no prior success means no trustworthy destination state — full copy")
}

func TestMirrorRun_CopiesTheDeltaUnderThePrefixAndCounts(t *testing.T) {
	now := time.Now()
	source := newRecorderStore()
	source.uploads["screens/1.png"] = []byte("one")
	source.uploads["screens/2.png"] = []byte("two2")
	source.uploads["notes/3.bin"] = []byte("three")
	source.listing = []ObjectInfo{
		{Key: "screens/1.png", Size: 3, LastModified: now},
		{Key: "screens/2.png", Size: 4, LastModified: now.Add(-48 * time.Hour)},
		{Key: "notes/3.bin", Size: 5, LastModified: now},
	}
	dest := newRecorderStore()
	// screens/2.png is already mirrored with the same size and is older than
	// the watermark: the only object this pass must NOT copy.
	dest.listing = []ObjectInfo{{Key: mirrorKeyPrefix + "screens/2.png", Size: 4}}

	job := newTestMirrorJob(source, dest)
	job.lastSuccess = func(context.Context) (time.Time, error) { return now.Add(-6 * time.Hour), nil }

	artifact, meta, reason, err := job.Run(context.Background())
	require.NoError(t, err)
	assert.Empty(t, reason)
	require.NotNil(t, artifact)
	require.NotNil(t, artifact.Mirror)
	assert.Empty(t, artifact.Key, "the mirror ships a delta, not one artifact object")

	assert.Equal(t, []byte("one"), dest.uploads[mirrorKeyPrefix+"screens/1.png"],
		"destination key = prefix + source key, byte-identical content")
	assert.Equal(t, []byte("three"), dest.uploads[mirrorKeyPrefix+"notes/3.bin"])
	assert.NotContains(t, dest.uploads, mirrorKeyPrefix+"screens/2.png")

	assert.EqualValues(t, 3, artifact.Mirror.ObjectsScanned)
	assert.EqualValues(t, 2, artifact.Mirror.ObjectsCopied)
	assert.EqualValues(t, 8, artifact.Mirror.BytesCopied)
	assert.EqualValues(t, 1, meta["objects_skipped"])
}

func TestMirrorRun_NeverPropagatesDeletions(t *testing.T) {
	source := newRecorderStore() // empty: everything was deleted at the origin
	dest := newRecorderStore()
	dest.listing = []ObjectInfo{{Key: mirrorKeyPrefix + "screens/gone.png", Size: 9}}

	job := newTestMirrorJob(source, dest)
	artifact, _, reason, err := job.Run(context.Background())
	require.NoError(t, err)
	assert.Empty(t, reason)
	assert.EqualValues(t, 0, artifact.Mirror.ObjectsCopied)
	assert.Empty(t, dest.deleted,
		"a wipe at the origin (bug or ransomware) must never delete the backup copy — deletions do not propagate")
}

func TestMirrorRun_CopyFailureFailsFastWithNormalizedReason(t *testing.T) {
	now := time.Now()
	source := newRecorderStore()
	const objects = 50
	for i := range objects {
		key := fmt.Sprintf("screens/%02d.png", i)
		source.uploads[key] = []byte("x")
		source.listing = append(source.listing, ObjectInfo{Key: key, Size: 1, LastModified: now})
	}
	dest := newRecorderStore()
	dest.putErr = io.ErrClosedPipe

	job := newTestMirrorJob(source, dest)
	_, _, reason, err := job.Run(context.Background())
	require.Error(t, err)
	assert.Equal(t, ReasonMirrorCopyFailed, reason)
	source.mu.Lock()
	opened := source.openCalls
	source.mu.Unlock()
	assert.Less(t, opened, objects,
		"fail-fast: the first error must cancel the pool instead of grinding through every doomed copy")
}

func TestMirrorRun_ScanFailureIsItsOwnReason(t *testing.T) {
	source := newRecorderStore()
	source.walkErr = io.ErrClosedPipe
	job := newTestMirrorJob(source, newRecorderStore())

	_, _, reason, err := job.Run(context.Background())
	require.Error(t, err)
	assert.Equal(t, ReasonMirrorScanFailed, reason)
}

func TestMirrorRun_WaitsOutARestoreThenProceeds(t *testing.T) {
	source := newRecorderStore()
	dest := newRecorderStore()
	job := newTestMirrorJob(source, dest)

	// Busy for the first probes, then clear: the job waits instead of failing.
	probes := 0
	job.restoreBusy = func(context.Context) (bool, error) {
		probes++
		return probes < 3, nil
	}
	job.restoreProbeDeadline = time.Second
	_, _, reason, err := job.Run(context.Background())
	require.NoError(t, err)
	assert.Empty(t, reason)
	assert.GreaterOrEqual(t, probes, 3)
}

func TestMirrorRun_StillBusyAtDeadlineFailsAsRestoreInFlight(t *testing.T) {
	job := newTestMirrorJob(newRecorderStore(), newRecorderStore())
	job.restoreBusy = func(context.Context) (bool, error) { return true, nil }

	_, _, reason, err := job.Run(context.Background())
	require.Error(t, err)
	assert.Equal(t, ReasonRestoreInFlight, reason,
		"a restore that outlives the wait becomes a normalized failure the next slot retries — never a raw error string")
}

func TestNew_RegistersTheMirrorOnlyWithIntervalAndSource(t *testing.T) {
	cfg := Config{AllowPlaintext: true, MirrorIntervalMin: 60}

	agent, err := New(cfg, nil, newRecorderStore(), newRecorderStore(), testLogger())
	require.NoError(t, err)
	// The registry always carries dump and drill (the drill registers even
	// unscheduled, for the manual button); the mirror is the third entry.
	require.Len(t, agent.jobs, 3)
	mirror := agent.jobs[len(agent.jobs)-1]
	assert.Equal(t, JobMirror, mirror.name)
	assert.Equal(t, time.Hour, mirror.interval)
	assert.True(t, mirror.enabled(), "an interval schedule alone must enable the loop — no anchor needed")

	cfg.MirrorIntervalMin = 0
	agent, err = New(cfg, nil, newRecorderStore(), newRecorderStore(), testLogger())
	require.NoError(t, err)
	assert.Len(t, agent.jobs, 2, "interval 0 keeps the mirror out of the registry")
	for _, spec := range agent.jobs {
		assert.NotEqual(t, JobMirror, spec.name)
	}
}

func TestMirror_EncryptedModeShipsAgeObjectsAndIgnoresSize(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	source := newRecorderStore()
	source.uploads["screenshots/1.png"] = []byte("png-bytes-plaintext")
	source.listing = []ObjectInfo{{Key: "screenshots/1.png", Size: 19, LastModified: time.Now()}}
	dest := newRecorderStore()
	job := newTestMirrorJob(source, dest)
	recipients, err := parseRecipients([]string{identity.Recipient().String()})
	require.NoError(t, err)
	job.recipients = recipients

	_, meta, reason, runErr := job.Run(context.Background())
	require.NoError(t, runErr)
	assert.Empty(t, reason)
	assert.Equal(t, true, meta["encrypted"])

	cipher, ok := dest.uploads[mirrorKeyPrefix+"screenshots/1.png.age"]
	require.True(t, ok, "the destination key carries the .age envelope")
	assert.NotContains(t, string(cipher), "png-bytes-plaintext",
		"user media is the ONE payload the encrypted dump does not carry — the mirror must not ship it in the clear")
	r, err := age.Decrypt(bytes.NewReader(cipher), identity)
	require.NoError(t, err)
	plain, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "png-bytes-plaintext", string(plain))

	// Size never re-copies in encrypted mode: ciphertext length ≠ source
	// length by construction, and comparing them would re-copy the whole
	// bucket forever — the ETag failure mode with a new face.
	dst := map[string]ObjectInfo{"screenshots/1.png.age": {Key: "screenshots/1.png.age", Size: int64(len(cipher))}}
	src := []ObjectInfo{{Key: "screenshots/1.png", Size: 19, LastModified: time.Now().Add(-3 * time.Hour)}}
	assert.Empty(t, mirrorDelta(src, dst, time.Now().Add(-2*time.Hour), true))
}

func TestMirror_TraversalKeysAreSkippedLoudlyNeverCopied(t *testing.T) {
	source := newRecorderStore()
	evil := "screenshots/1.x/../../backups/db/evil"
	source.uploads[evil] = []byte("payload")
	source.uploads["screenshots/2.png"] = []byte("fine")
	source.listing = []ObjectInfo{
		{Key: evil, Size: 7, LastModified: time.Now()},
		{Key: "screenshots/2.png", Size: 4, LastModified: time.Now()},
	}
	dest := newRecorderStore()
	job := newTestMirrorJob(source, dest)

	_, meta, _, err := job.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, meta["suspicious_keys_skipped"])
	for key := range dest.uploads {
		assert.NotContains(t, key, "..",
			"a key with traversal material must never reach the destination — on a normalizing store it could overwrite the encrypted dumps")
	}
	assert.Contains(t, dest.uploads, mirrorKeyPrefix+"screenshots/2.png",
		"the legitimate sibling still mirrors: one poison object must not end mirroring forever")
}
