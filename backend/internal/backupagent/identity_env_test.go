package backupagent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func inlineIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	return id
}

// The reason this path exists: a platform that materialises secrets as env
// vars, and writes files at a mode the operator does not choose, can still
// run the drill and the download bridge without a chmod wrapper in front of
// the entrypoint.
func TestLoad_InlineIdentitySatisfiesTheDrill(t *testing.T) {
	setBaseline(t)
	id := inlineIdentity(t)
	t.Setenv("BACKUP_DRILL_AT", "04:30 sun")
	t.Setenv("BACKUP_AGE_IDENTITY", id.String())
	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, cfg.HasAgeIdentity())
	assert.Empty(t, cfg.AgeIdentityFile)
	assert.Equal(t, id.String(), cfg.AgeIdentity)
}

func TestLoad_RefusesBothIdentitySources(t *testing.T) {
	setBaseline(t)
	t.Setenv("BACKUP_AGE_IDENTITY_FILE", "/run/secrets/backup-age-identity")
	t.Setenv("BACKUP_AGE_IDENTITY", inlineIdentity(t).String())
	_, err := Load()
	require.Error(t, err, "two sources for one key is how the wrong one gets rotated")
	assert.Contains(t, err.Error(), "both set")
	assert.NotContains(t, err.Error(), "AGE-SECRET-KEY", "the refusal never echoes the key")
}

func TestLoad_ScrubsTheInlineIdentityFromTheEnvironment(t *testing.T) {
	setBaseline(t)
	id := inlineIdentity(t)
	t.Setenv("BACKUP_AGE_IDENTITY", id.String())
	cfg, err := Load()
	require.NoError(t, err)
	_, still := os.LookupEnv("BACKUP_AGE_IDENTITY")
	assert.False(t, still, "read once into Config, then gone: os.Environ() is what pg_dump inherits")

	// The consequence, not just the mechanism: the one child that inherits
	// the full environment must not carry the key that opens every backup.
	cmd := pgDumpCommand(context.Background(), cfg, "")
	assert.NotContains(t, strings.Join(cmd.Env, "\n"), id.String())
}

func TestLoadIdentities_InlineContract(t *testing.T) {
	id := inlineIdentity(t)

	t.Run("parses the inline identity, comments included", func(t *testing.T) {
		ids, err := loadIdentities(Config{AgeIdentity: "# created: today\n" + id.String()})
		require.NoError(t, err)
		assert.Len(t, ids, 1)
	})

	t.Run("garbage is rejected without being echoed", func(t *testing.T) {
		_, err := loadIdentities(Config{AgeIdentity: "totally-not-an-identity"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BACKUP_AGE_IDENTITY")
		assert.NotContains(t, err.Error(), "totally-not-an-identity")
	})

	t.Run("no source configured is a capability gap, not an error", func(t *testing.T) {
		ids, err := loadIdentities(Config{})
		require.NoError(t, err)
		assert.Empty(t, ids)
	})

	t.Run("the file path is untouched by the inline one", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "id.txt")
		require.NoError(t, os.WriteFile(path, []byte(id.String()+"\n"), 0o600))
		ids, err := loadIdentities(Config{AgeIdentityFile: path})
		require.NoError(t, err)
		assert.Len(t, ids, 1)
	})
}

// A world-readable file is still refused — the inline form is an ALTERNATIVE
// to the mode rule, never a relaxation of it.
func TestLoadIdentities_InlineDoesNotRelaxTheFileRule(t *testing.T) {
	id := inlineIdentity(t)
	path := filepath.Join(t.TempDir(), "id.txt")
	require.NoError(t, os.WriteFile(path, []byte(id.String()+"\n"), 0o644))
	_, err := loadIdentities(Config{AgeIdentityFile: path})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chmod 600")
}

// The bridge is the case that motivated the change: a Coolify deployment
// with the identity inline must still be able to open an artifact. Built
// through New, not by hand, so a regression that gates the bridge on the file
// path alone cannot ship green.
func TestNew_InlineIdentityOpensTheDownloadBridge(t *testing.T) {
	store := newRecorderStore()
	plain := []byte("PGDMP inline-identity dump body")
	id := seedEncrypted(t, store, dumpKeyPrefix+"2026/09/10/foldex.dump.age", plain).(*age.X25519Identity)

	agent, err := New(Config{AllowPlaintext: true, ArtifactToken: "t0ken", AgeIdentity: id.String()}, nil, store, nil, testLogger())
	require.NoError(t, err)
	require.Len(t, agent.identities, 1)

	obj, err := store.OpenObject(t.Context(), dumpKeyPrefix+"2026/09/10/foldex.dump.age")
	require.NoError(t, err)
	defer obj.Close()
	opened, err := agent.openPlaintext(dumpKeyPrefix+"2026/09/10/foldex.dump.age", obj)
	require.NoError(t, err)
	got, err := io.ReadAll(opened)
	require.NoError(t, err)
	assert.Equal(t, plain, got)

	_, err = New(Config{AllowPlaintext: true, ArtifactToken: "t0ken", AgeIdentity: "nope"}, nil, store, nil, testLogger())
	require.Error(t, err, "a bad inline identity fails the boot with the bridge on, exactly like a bad file")

	noID, err := New(Config{AllowPlaintext: true, ArtifactToken: "t0ken"}, nil, store, nil, testLogger())
	require.NoError(t, err)
	_, err = noID.openPlaintext("x.dump.age", strings.NewReader(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BACKUP_AGE_IDENTITY")
}

func TestIdentitySource_IsALabelNeverTheValue(t *testing.T) {
	id := inlineIdentity(t)
	assert.Equal(t, "inline", Config{AgeIdentity: id.String()}.IdentitySource())
	assert.Equal(t, "file", Config{AgeIdentityFile: "/run/secrets/id"}.IdentitySource())
	assert.Equal(t, "none", Config{}.IdentitySource())
}

func TestLoad_WhitespaceOnlyInlineIdentityIsUnset(t *testing.T) {
	setBaseline(t)
	t.Setenv("BACKUP_AGE_IDENTITY_FILE", "/run/secrets/backup-age-identity")
	t.Setenv("BACKUP_AGE_IDENTITY", "   ")
	cfg, err := Load()
	require.NoError(t, err, "blank is unset, not a second source")
	assert.Equal(t, "file", cfg.IdentitySource())
}

func TestNewDrillJob_InlineIdentityFailsFastLikeAFile(t *testing.T) {
	id := inlineIdentity(t)
	job, err := NewDrillJob(Config{PGUser: "user_foldex", AgeIdentity: id.String(), SpoolDir: t.TempDir()}, &fakeDrillRuns{}, newRecorderStore(), testLogger())
	require.NoError(t, err)
	require.Len(t, job.identities, 1)

	_, err = NewDrillJob(Config{AgeIdentity: "nope"}, &fakeDrillRuns{}, newRecorderStore(), testLogger())
	require.Error(t, err, "a bad inline identity fails the boot, exactly like a bad file")
}

func TestCapability_EitherIdentitySourceEnablesTheDrill(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"file", Config{AgeIdentityFile: "/run/secrets/id"}},
		{"inline", Config{AgeIdentity: "AGE-SECRET-KEY-1..."}},
	} {
		a := &Agent{cfg: tc.cfg}
		ok, reason := a.capability("drill")
		assert.True(t, ok, tc.name)
		assert.Empty(t, reason, tc.name)
	}
	ok, reason := (&Agent{}).capability("drill")
	assert.False(t, ok)
	assert.Equal(t, "no_identity", reason)
}
