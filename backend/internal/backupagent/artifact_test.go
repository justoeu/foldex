package backupagent

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPassphrase = "correct-horse-battery-staple"

// agentWithArtifact builds the smallest Agent the bridge needs: a store, a
// logger, and the private identity that opens what the store holds.
func agentWithArtifact(t *testing.T, store Uploader, identities ...age.Identity) *Agent {
	t.Helper()
	return &Agent{cfg: Config{ArtifactToken: "t0ken"}, store: store, logger: testLogger(), identities: identities}
}

// seedEncrypted puts an age-encrypted object in the store and returns the
// plaintext it wraps, plus the identity that opens it.
func seedEncrypted(t *testing.T, store *recorderStore, key string, plain []byte) age.Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, id.Recipient())
	require.NoError(t, err)
	_, err = w.Write(plain)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, store.PutObjectStream(t.Context(), key, &buf, int64(buf.Len()), "application/octet-stream"))
	return id
}

func postArtifact(a *Agent, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/internal/artifact", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	a.serveArtifact(rec, req)
	return rec
}

/*
 * The whole feature in one assertion: what the bucket holds under the
 * instance's X25519 key comes back openable with the passphrase the caller
 * chose, and the bytes are the same bytes.
 */
func TestServeArtifact_RoundTripsUnderTheCallersPassphrase(t *testing.T) {
	store := newRecorderStore()
	plain := []byte("PGDMP fake custom-format dump body")
	id := seedEncrypted(t, store, dumpKeyPrefix+"2026/09/09/foldex.dump.age", plain)
	a := agentWithArtifact(t, store, id)

	rec := postArtifact(a, artifactRequest{Key: dumpKeyPrefix + "2026/09/09/foldex.dump.age", Passphrase: testPassphrase})
	require.Equal(t, http.StatusOK, rec.Code)

	identity, err := age.NewScryptIdentity(testPassphrase)
	require.NoError(t, err)
	out, err := age.Decrypt(bytes.NewReader(rec.Body.Bytes()), identity)
	require.NoError(t, err)
	got, err := io.ReadAll(out)
	require.NoError(t, err)
	assert.Equal(t, plain, got)
}

// The response must be UNREADABLE without the passphrase. A bug that streamed
// the plaintext through would still "work" end to end for the operator who
// knows their own password — this is the assertion that catches it.
func TestServeArtifact_TheBodyIsNotThePlaintext(t *testing.T) {
	store := newRecorderStore()
	plain := []byte("PGDMP fake custom-format dump body")
	id := seedEncrypted(t, store, dumpKeyPrefix+"a.dump.age", plain)
	a := agentWithArtifact(t, store, id)

	rec := postArtifact(a, artifactRequest{Key: dumpKeyPrefix + "a.dump.age", Passphrase: testPassphrase})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), string(plain))

	wrong, err := age.NewScryptIdentity("a-different-passphrase-entirely")
	require.NoError(t, err)
	_, err = age.Decrypt(bytes.NewReader(rec.Body.Bytes()), wrong)
	assert.Error(t, err, "the wrong passphrase must not open the artifact")
}

// The passphrase and the bucket's credentials must never appear in what the
// backend receives — the molde of TestAgent_HeartbeatCarriesNoCredential.
func TestServeArtifact_TheResponseCarriesNoSecretOfItsOwn(t *testing.T) {
	store := newRecorderStore()
	id := seedEncrypted(t, store, dumpKeyPrefix+"a.dump.age", []byte("body"))
	a := agentWithArtifact(t, store, id)
	a.cfg.S3AccessKey = "AKIAEXAMPLEKEY"
	a.cfg.S3SecretKey = "s3cr3t-example-value"

	rec := postArtifact(a, artifactRequest{Key: dumpKeyPrefix + "a.dump.age", Passphrase: testPassphrase})
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.NotContains(t, body, testPassphrase)
	assert.NotContains(t, body, "AKIAEXAMPLEKEY")
	assert.NotContains(t, body, "s3cr3t-example-value")
}

/*
 * `key` arrives over HTTP and the store is an S3 client that would fetch
 * anything in the bucket. The prefix set is what keeps this endpoint from
 * becoming "read any object", which is the entire bucket, which is every
 * user's content and every bcrypt hash.
 */
func TestServeArtifact_RefusesAKeyOutsideTheBackupNamespaces(t *testing.T) {
	store := newRecorderStore()
	id := seedEncrypted(t, store, "secrets/keys.age", []byte("not yours"))
	a := agentWithArtifact(t, store, id)

	for _, key := range []string{
		"secrets/keys.age",
		"",
		"backups/dump/../../secrets/keys.age",
		"/backups/dump/a.age",
		"Backups/dump/a.age",
	} {
		rec := postArtifact(a, artifactRequest{Key: key, Passphrase: testPassphrase})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "key %q must be refused", key)
		assert.Equal(t, 0, store.openCalls, "a refused key must never reach the store")
	}
}

// A four-character passphrase is not a passphrase. scrypt's work factor does
// not rescue it, and the file leaves the instance.
func TestServeArtifact_RefusesAPassphraseTooShortToProtectAnything(t *testing.T) {
	store := newRecorderStore()
	id := seedEncrypted(t, store, dumpKeyPrefix+"a.dump.age", []byte("body"))
	a := agentWithArtifact(t, store, id)

	rec := postArtifact(a, artifactRequest{Key: dumpKeyPrefix + "a.dump.age", Passphrase: "hunter2"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, 0, store.openCalls)
}

// A plaintext deployment (BACKUP_ALLOW_PLAINTEXT, no ".age") still gets an
// ENCRYPTED download: the passphrase protects the wire and the disk it lands
// on, regardless of how the bucket stores it.
func TestServeArtifact_EncryptsEvenWhenTheStoredObjectIsPlaintext(t *testing.T) {
	store := newRecorderStore()
	plain := []byte("plain body")
	require.NoError(t, store.PutObjectStream(t.Context(), dumpKeyPrefix+"a.dump", bytes.NewReader(plain), int64(len(plain)), ""))
	a := agentWithArtifact(t, store)

	rec := postArtifact(a, artifactRequest{Key: dumpKeyPrefix + "a.dump", Passphrase: testPassphrase})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), string(plain))

	identity, err := age.NewScryptIdentity(testPassphrase)
	require.NoError(t, err)
	out, err := age.Decrypt(bytes.NewReader(rec.Body.Bytes()), identity)
	require.NoError(t, err)
	got, err := io.ReadAll(out)
	require.NoError(t, err)
	assert.Equal(t, plain, got)
}

/*
 * The guard, and specifically its DISABLED arm: an operator who never sets
 * BACKUP_AGENT_TOKEN keeps INV-171's wall exactly as it was. A bridge that
 * defaulted to open would hand that decision to whoever forgot the variable.
 */
func TestArtifactAuth_AnUnsetTokenDisablesTheBridgeEntirely(t *testing.T) {
	called := false
	h := artifactAuth("", func(http.ResponseWriter, *http.Request) { called = true })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/artifact", nil)
	req.Header.Set("Authorization", "Bearer ")
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.False(t, called, "an empty token must not be satisfiable by an empty header")
}

func TestArtifactAuth_RefusesAWrongOrMissingToken(t *testing.T) {
	called := false
	h := artifactAuth("t0ken", func(http.ResponseWriter, *http.Request) { called = true })

	for _, header := range []string{"", "Bearer", "Bearer wrong", "t0ken", "bearer t0ken"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/internal/artifact", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "header %q", header)
		assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
	}
	assert.False(t, called)
}

func TestArtifactAuth_AdmitsTheRightToken(t *testing.T) {
	called := false
	h := artifactAuth("t0ken", func(w http.ResponseWriter, _ *http.Request) { called = true })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/artifact", nil)
	req.Header.Set("Authorization", "Bearer t0ken")
	h.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestListArtifacts_AnswersOnlyInsideTheBackupNamespaces(t *testing.T) {
	store := newRecorderStore()
	store.listing = []ObjectInfo{{Key: userZipKeyPrefix + "7.zip.age", Size: 42}}
	a := agentWithArtifact(t, store)

	rec := httptest.NewRecorder()
	a.listArtifacts(rec, httptest.NewRequest(http.MethodGet, "/internal/artifacts?prefix="+userZipKeyPrefix, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "7.zip.age")

	rec = httptest.NewRecorder()
	a.listArtifacts(rec, httptest.NewRequest(http.MethodGet, "/internal/artifacts?prefix=secrets/", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestServeArtifact_RefusesAMalformedBody(t *testing.T) {
	a := agentWithArtifact(t, newRecorderStore())
	req := httptest.NewRequest(http.MethodPost, "/internal/artifact", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	a.serveArtifact(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
