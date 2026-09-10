package backupstatus

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/pkg/authctx"
)

/*
The ownership rule, tested directly because it is the SECOND layer.

In production the route sits behind an owner-only locked permission, so a
non-owner never reaches this function at all — which is exactly why it needs
its own test. Reachability today is a configuration; the rule has to hold on
its own the day that configuration changes, or it is not a guard.
*/
func TestMayDownload_AWholeInstanceArtifactBelongsOnlyToTheOwner(t *testing.T) {
	const dump = "backups/dump/2026/09/09/foldex.dump.age"

	assert.True(t, mayDownload(authctx.Principal{Role: authctx.RoleOwner, UserID: 1}, dump))
	// A dump carries every account's rows and has no per-account slice, so
	// there is no reading of "only what you own" that hands it to an admin.
	assert.False(t, mayDownload(authctx.Principal{Role: authctx.RoleAdmin, UserID: 1}, dump))
	assert.False(t, mayDownload(authctx.Principal{Role: authctx.RoleEditor, UserID: 1}, dump))
	assert.False(t, mayDownload(authctx.Principal{Role: authctx.RoleAdmin, UserID: 1},
		"backups/rustfs/abc/def.bin"))
}

func TestMayDownload_APerUserArchiveBelongsToItsOwnAccount(t *testing.T) {
	mine := "backups/users/7/20260909-000000.zip.age"

	assert.True(t, mayDownload(authctx.Principal{Role: authctx.RoleAdmin, UserID: 7}, mine))
	assert.False(t, mayDownload(authctx.Principal{Role: authctx.RoleAdmin, UserID: 8}, mine),
		"another account's archive is another account's rows")
	// The owner may take any of them — that is what owning the instance means.
	assert.True(t, mayDownload(authctx.Principal{Role: authctx.RoleOwner, UserID: 99}, mine))
}

/*
The owner segment is PARSED, not pattern-matched.

A key is a string the database holds and the agent wrote; treating "looks like
backups/users/<something>" as proof of ownership would make any malformed or
crafted key a way past the check. Anything that does not resolve to a positive
integer resolves to no owner, and no owner means owner-only.
*/
func TestArtifactOwner_RefusesEveryShapeThatIsNotAUserID(t *testing.T) {
	for _, key := range []string{
		"backups/users/",
		"backups/users/7",            // a uid, but no object under it
		"backups/users//x.zip.age",   // empty segment
		"backups/users/0/x.zip.age",  // zero is not an account
		"backups/users/-3/x.zip.age", // negative
		"backups/users/7x/x.zip.age", // not a number
		"backups/users/ 7/x.zip.age", // padded
		"backups/dump/7/x.age",       // right shape, wrong namespace
		"",
	} {
		_, ok := artifactOwner(key)
		assert.False(t, ok, "key %q must resolve to no owner", key)
		assert.False(t, mayDownload(authctx.Principal{Role: authctx.RoleAdmin, UserID: 7}, key),
			"key %q must not be downloadable by a non-owner", key)
	}

	uid, ok := artifactOwner("backups/users/7/20260909.zip.age")
	assert.True(t, ok)
	assert.EqualValues(t, 7, uid)
}
