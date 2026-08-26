package backupagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

// newTestUserZipJob wires every seam by hand: the real constructor needs a
// backup.Service, and this file proves the pipeline AROUND the Export.
func newTestUserZipJob(t *testing.T, store *recorderStore, recipients []string, users []authctx.UserID) *UserZipJob {
	t.Helper()
	parsed, err := parseRecipients(recipients)
	require.NoError(t, err)
	return &UserZipJob{
		cfg:        Config{RetainUserZip: 7, RetentionMode: "agent"},
		store:      store,
		recipients: parsed,
		logger:     testLogger(),
		export: func(_ context.Context, uid authctx.UserID, w io.Writer) error {
			_, err := fmt.Fprintf(w, "zip-payload-for-user-%d", uid)
			return err
		},
		listActive:  func(context.Context) ([]authctx.UserID, error) { return users, nil },
		restoreBusy: func(context.Context) (bool, error) { return false, nil },
		now:         time.Now,
	}
}

func TestUserZipRun_OneUserFailingDoesNotAbortTheOthers(t *testing.T) {
	store := newRecorderStore()
	job := newTestUserZipJob(t, store, nil, []authctx.UserID{1, 2, 3})
	job.export = func(_ context.Context, uid authctx.UserID, w io.Writer) error {
		if uid == 2 {
			return fmt.Errorf("export blew up")
		}
		_, err := fmt.Fprintf(w, "zip-%d", uid)
		return err
	}

	artifact, meta, reason, err := job.Run(context.Background())
	require.NoError(t, err, "one straggler must not cost every other user their backup")
	assert.Empty(t, reason)
	assert.Nil(t, artifact, "user_zip ships N artifacts; the run row records the job, not one key")

	assert.Equal(t, 3, meta["users"])
	assert.Equal(t, []int64{2}, meta["failed_users"])
	assert.NotContains(t, meta, "deferred_users")

	var total int64
	for key, data := range store.uploads {
		assert.False(t, strings.HasPrefix(key, "backups/users/2/"), "the failed user must not upload")
		total += int64(len(data))
	}
	assert.Len(t, store.uploads, 2)
	assert.Equal(t, total, meta["bytes_total"])
}

func TestUserZipRun_AllFailingFailsTheWholeRun(t *testing.T) {
	store := newRecorderStore()
	job := newTestUserZipJob(t, store, nil, []authctx.UserID{1, 2})
	job.export = func(context.Context, authctx.UserID, io.Writer) error {
		return fmt.Errorf("database on fire")
	}

	_, _, reason, err := job.Run(context.Background())
	require.Error(t, err)
	assert.Equal(t, ReasonUserZipFailed, reason)
	assert.Empty(t, store.uploads)
}

func TestUserZipRun_ListingFailureFailsTheRun(t *testing.T) {
	store := newRecorderStore()
	job := newTestUserZipJob(t, store, nil, nil)
	job.listActive = func(context.Context) ([]authctx.UserID, error) {
		return nil, fmt.Errorf("app_user unreachable")
	}

	_, _, reason, err := job.Run(context.Background())
	require.Error(t, err)
	assert.Equal(t, ReasonUserZipFailed, reason)
}

func TestUserZipRun_ZeroActiveUsersIsAnEmptySuccess(t *testing.T) {
	store := newRecorderStore()
	job := newTestUserZipJob(t, store, nil, nil)

	_, meta, reason, err := job.Run(context.Background())
	require.NoError(t, err, "an instance with no active accounts has nothing to back up, not a failure")
	assert.Empty(t, reason)
	assert.Equal(t, 0, meta["users"])
	assert.EqualValues(t, 0, meta["bytes_total"])
}

func TestUserZipShipOne_EncryptsAndHashesTheCiphertext(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	store := newRecorderStore()
	job := newTestUserZipJob(t, store, []string{identity.Recipient().String()}, []authctx.UserID{7})
	fixed := time.Date(2026, 8, 26, 2, 0, 0, 0, time.UTC)
	job.now = func() time.Time { return fixed }

	key, size, sha, err := job.shipOne(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, "backups/users/7/20260826-020000.zip.age", key)

	uploaded, ok := store.uploads[key]
	require.True(t, ok, "the archive must actually reach the store")
	assert.EqualValues(t, len(uploaded), size)

	// The recorded sha is of the CIPHERTEXT — the number sha256sum verifies
	// against the bucket without decrypting anything.
	sum := sha256.Sum256(uploaded)
	assert.Equal(t, hex.EncodeToString(sum[:]), sha)
	assert.NotContains(t, string(uploaded), "zip-payload", "plaintext must not reach the bucket")

	// And the stored bytes round-trip through standard age.
	r, err := age.Decrypt(bytes.NewReader(uploaded), identity)
	require.NoError(t, err)
	plain, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "zip-payload-for-user-7", string(plain))
}

func TestUserZipShipOne_PlaintextModeDropsTheAgeSuffix(t *testing.T) {
	store := newRecorderStore()
	job := newTestUserZipJob(t, store, nil, []authctx.UserID{3})

	key, _, _, err := job.shipOne(context.Background(), 3)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(key, ".zip"), "no .age suffix when nothing is encrypted: %s", key)
	assert.Equal(t, "zip-payload-for-user-3", string(store.uploads[key]))
}

func userZipTestKeys(uid authctx.UserID, n int) []string {
	keys := make([]string, 0, n)
	base := time.Date(2026, 8, 1, 2, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		keys = append(keys, userZipKey(uid, base.AddDate(0, 0, i), true))
	}
	return keys
}

func TestUserZipPrune_KeepsTheNewestPerUserAndNeverCrossesUsers(t *testing.T) {
	store := newRecorderStore()
	job := newTestUserZipJob(t, store, nil, nil)

	mine := userZipTestKeys(1, 9)
	other := userZipTestKeys(2, 9)
	foreign := "backups/users/1/notes.txt"
	for _, k := range append(append(append([]string{}, mine...), other...), foreign) {
		store.listing = append(store.listing, ObjectInfo{Key: k, Size: 10})
	}

	pruned, err := job.pruneUser(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, 2, pruned)
	require.Len(t, store.deleted, 1)
	assert.ElementsMatch(t, mine[:2], store.deleted[0], "only user 1's two OLDEST archives are victims")
	for _, batch := range store.deleted {
		for _, k := range batch {
			assert.False(t, strings.HasPrefix(k, "backups/users/2/"), "another user's directory is never touched")
			assert.NotEqual(t, foreign, k, "a key this job did not write is never a deletion target")
		}
	}
}

func TestUserZipPrune_UnderTheLimitAndDisabledAreNoOps(t *testing.T) {
	store := newRecorderStore()
	job := newTestUserZipJob(t, store, nil, nil)
	for _, k := range userZipTestKeys(1, 5) {
		store.listing = append(store.listing, ObjectInfo{Key: k, Size: 10})
	}

	pruned, err := job.pruneUser(context.Background(), 1)
	require.NoError(t, err)
	assert.Zero(t, pruned)
	assert.Empty(t, store.deleted)

	// A misconfigured 0 must not read as "delete every backup of every user".
	job.cfg.RetainUserZip = 0
	pruned, err = job.pruneUser(context.Background(), 1)
	require.NoError(t, err)
	assert.Zero(t, pruned)
	assert.Empty(t, store.deleted)
}

func TestUserZipRun_BucketRetentionModeNeverDeletes(t *testing.T) {
	store := newRecorderStore()
	job := newTestUserZipJob(t, store, nil, []authctx.UserID{1})
	job.cfg.RetentionMode = "bucket"
	for _, k := range userZipTestKeys(1, 20) {
		store.listing = append(store.listing, ObjectInfo{Key: k, Size: 10})
	}

	_, _, _, err := job.Run(context.Background())
	require.NoError(t, err)
	assert.Empty(t, store.deleted, "bucket mode: lifecycle owns expiry, the agent's prune is a declared no-op")
}

func TestUserZipRun_PruneFailureDowngradesToRunMetadata(t *testing.T) {
	store := newRecorderStore()
	job := newTestUserZipJob(t, store, nil, []authctx.UserID{1})
	job.cfg.RetainUserZip = 1
	store.deleteEr = io.ErrClosedPipe
	for _, k := range userZipTestKeys(1, 3) {
		store.listing = append(store.listing, ObjectInfo{Key: k, Size: 10})
	}

	_, meta, reason, err := job.Run(context.Background())
	require.NoError(t, err, "the archive landed; a failed prune must not turn success into failure")
	assert.Empty(t, reason)
	assert.Equal(t, ReasonPruneFailed, meta["prune_error"])
}

func TestUserZipRun_DefersToAnInFlightRestore(t *testing.T) {
	t.Run("all deferred is still a success", func(t *testing.T) {
		store := newRecorderStore()
		job := newTestUserZipJob(t, store, nil, []authctx.UserID{1, 2})
		job.restoreBusy = func(context.Context) (bool, error) { return true, nil }

		_, meta, reason, err := job.Run(context.Background())
		require.NoError(t, err, "a deferred cycle is coordination, never a failure (INV-104)")
		assert.Empty(t, reason)
		assert.Equal(t, []int64{1, 2}, meta["deferred_users"])
		assert.NotContains(t, meta, "failed_users")
		assert.Empty(t, store.uploads)
	})

	t.Run("the probe runs per user, so a finished restore frees the rest", func(t *testing.T) {
		store := newRecorderStore()
		job := newTestUserZipJob(t, store, nil, []authctx.UserID{1, 2})
		calls := 0
		job.restoreBusy = func(context.Context) (bool, error) {
			calls++
			return calls == 1, nil
		}

		_, meta, _, err := job.Run(context.Background())
		require.NoError(t, err)
		assert.Equal(t, []int64{1}, meta["deferred_users"])
		assert.Len(t, store.uploads, 1)
		for key := range store.uploads {
			assert.True(t, strings.HasPrefix(key, "backups/users/2/"))
		}
	})
}

func TestIsUserZipKey_ClassifiesOnlyWhatTheJobWrites(t *testing.T) {
	prefix := userZipPrefix(1)
	assert.True(t, isUserZipKey(prefix, "backups/users/1/20260826-020000.zip.age"))
	assert.True(t, isUserZipKey(prefix, "backups/users/1/20260826-020000.zip"))
	assert.False(t, isUserZipKey(prefix, "backups/users/1/nested/20260826-020000.zip"))
	assert.False(t, isUserZipKey(prefix, "backups/users/1/notes.txt"))
	assert.False(t, isUserZipKey(prefix, "backups/users/1/20260826-020000"))
	assert.False(t, isUserZipKey(prefix, "backups/users/11/20260826-020000.zip"))
}

func TestUserZipRun_AProbeErrorIsAFailureNeverADeferral(t *testing.T) {
	store := newRecorderStore()
	job := newTestUserZipJob(t, store, nil, []authctx.UserID{1})
	job.restoreBusy = func(context.Context) (bool, error) { return false, io.ErrClosedPipe }

	_, meta, reason, err := job.Run(context.Background())
	require.Error(t, err, "every user failing must fail the run")
	assert.Equal(t, ReasonUserZipFailed, reason)
	_, deferred := meta["deferred_users"]
	assert.False(t, deferred,
		"an unanswerable probe is not a restore in flight — deferring would retry forever against a broken lock path")
}

func TestUserZipShipOne_ZeroByteExportIsNotABackup(t *testing.T) {
	store := newRecorderStore()
	job := newTestUserZipJob(t, store, nil, []authctx.UserID{1})
	job.export = func(context.Context, authctx.UserID, io.Writer) error { return nil }

	_, _, reason, err := job.Run(context.Background())
	require.Error(t, err)
	assert.Equal(t, ReasonUserZipFailed, reason)
	assert.Empty(t, store.uploads, "an empty archive must never land looking like a backup")
}
