package backupagent

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncrypt_RoundTripsThroughStandardAge(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)

	recipients, err := parseRecipients([]string{identity.Recipient().String()})
	require.NoError(t, err)

	var ciphertext bytes.Buffer
	w, err := encryptTo(&ciphertext, recipients)
	require.NoError(t, err)
	payload := strings.Repeat("pg_dump custom-format bytes ", 4096)
	_, err = io.WriteString(w, payload)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	assert.NotContains(t, ciphertext.String(), "pg_dump custom-format",
		"the artifact in the bucket must not contain the plaintext")

	// The DR contract: standard age (here the library, in the field `age -d`)
	// opens the artifact with no Foldex code involved.
	r, err := age.Decrypt(&ciphertext, identity)
	require.NoError(t, err)
	roundTripped, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, payload, string(roundTripped))
}

func TestEncrypt_PlaintextPassThroughOnlyWithNoRecipients(t *testing.T) {
	var out bytes.Buffer
	w, err := encryptTo(&out, nil)
	require.NoError(t, err)
	_, err = io.WriteString(w, "plain")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	assert.Equal(t, "plain", out.String())
}

func TestParseRecipients_RejectsGarbageAndIdentities(t *testing.T) {
	_, err := parseRecipients([]string{"not-a-key"})
	assert.Error(t, err)

	identity, genErr := age.GenerateX25519Identity()
	require.NoError(t, genErr)
	// Pasting the PRIVATE key where the public one goes must fail loudly, not
	// encrypt to a recipient derived from a leaked secret.
	_, err = parseRecipients([]string{identity.String()})
	assert.Error(t, err)
}
