package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/secrets"
)

func TestNewSessionIssueCreatesTTLBoundHashedCredentials(t *testing.T) {
	ttl := SessionTTL{Access: 7 * time.Minute, Refresh: 11 * time.Hour}
	before := time.Now()
	issue, err := newSessionIssue(ttl)
	after := time.Now()
	require.NoError(t, err)

	credentials := []struct {
		name string
		raw  string
		hash []byte
	}{
		{name: "access", raw: issue.tokens.Access, hash: issue.hashes.access},
		{name: "refresh", raw: issue.tokens.Refresh, hash: issue.hashes.refresh},
		{name: "csrf", raw: issue.tokens.CSRF, hash: issue.hashes.csrf},
	}
	for _, credential := range credentials {
		t.Run(credential.name, func(t *testing.T) {
			assert.NotEmpty(t, credential.raw)
			assert.Equal(t, secrets.Hash(credential.raw), credential.hash)
		})
	}

	assert.False(t, issue.tokens.AccessExpiry.Before(before.Add(ttl.Access)))
	assert.False(t, issue.tokens.AccessExpiry.After(after.Add(ttl.Access)))
	assert.False(t, issue.tokens.RefreshExpiry.Before(before.Add(ttl.Refresh)))
	assert.False(t, issue.tokens.RefreshExpiry.After(after.Add(ttl.Refresh)))
	assert.Equal(t, ttl.Refresh-ttl.Access,
		issue.tokens.RefreshExpiry.Sub(issue.tokens.AccessExpiry))
}
