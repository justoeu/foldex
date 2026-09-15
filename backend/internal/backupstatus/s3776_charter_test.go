package backupstatus

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/pkg/authctx"
)

// INV-187: a whole-instance artifact has no per-account slice, so only the
// owner may take it. A per-user ZIP goes to its own account. Extracting
// Download must not loosen this second layer.
func TestS3776_INV187_DownloadIsOwnerOnlyForWholeInstanceArtifacts(t *testing.T) {
	const dump = "backups/dump/2026/09/09/foldex.dump.age"
	const rustfs = "backups/rustfs/abc/def.bin"
	mine := "backups/users/7/20260909-000000.zip.age"

	owner := authctx.Principal{Role: authctx.RoleOwner, UserID: 1}
	admin := authctx.Principal{Role: authctx.RoleAdmin, UserID: 1}
	editor := authctx.Principal{Role: authctx.RoleEditor, UserID: 1}
	admin7 := authctx.Principal{Role: authctx.RoleAdmin, UserID: 7}
	admin8 := authctx.Principal{Role: authctx.RoleAdmin, UserID: 8}

	assert.True(t, mayDownload(owner, dump))
	assert.False(t, mayDownload(admin, dump))
	assert.False(t, mayDownload(editor, dump))
	assert.False(t, mayDownload(admin, rustfs))

	assert.True(t, mayDownload(admin7, mine))
	assert.False(t, mayDownload(admin8, mine), "another account's archive is another account's rows")
	assert.True(t, mayDownload(authctx.Principal{Role: authctx.RoleOwner, UserID: 99}, mine))
}

func TestS3776_INV187_MalformedUserZipKeyIsOwnerOnly(t *testing.T) {
	admin := authctx.Principal{Role: authctx.RoleAdmin, UserID: 7}
	for _, key := range []string{
		"backups/users/",
		"backups/users/7",
		"backups/users//x.zip.age",
		"backups/users/0/x.zip.age",
		"backups/users/-3/x.zip.age",
		"backups/users/7x/x.zip.age",
		"backups/dump/7/x.age",
		"",
	} {
		_, ok := artifactOwner(key)
		assert.False(t, ok, "key %q must resolve to no owner", key)
		assert.False(t, mayDownload(admin, key), "key %q must not be downloadable by a non-owner", key)
	}
}
