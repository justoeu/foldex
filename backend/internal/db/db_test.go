package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_RejectsInvalidDSN(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := New(ctx, "not-a-dsn")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse dsn")
}

func TestNew_FailsOnUnreachableHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// TEST-NET-1 (RFC 5737), reserved for documentation, never routable.
	_, err := New(ctx, "postgres://x:x@192.0.2.1:1/nope?sslmode=disable&connect_timeout=1")
	require.Error(t, err)
}

// The behavioural proof — that no span leaves this process carrying user.name
// — is TestQuerySpansNeverCarryThePostgresRole, which needs a database. This
// one is the fast half: it pins the SET, so a well-meaning "let's add the user
// back, it's useful for debugging" edit fails here first, without Docker.
func TestConnAttrs_KeepsTheServerAndDropsThePostgresRole(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://user_foldex:pw@dbhost:5433/foldex?sslmode=disable")
	require.NoError(t, err)

	keys := map[string]string{}
	for _, kv := range connAttrs(cfg.ConnConfig) {
		keys[string(kv.Key)] = kv.Value.Emit()
	}

	assert.Equal(t, "dbhost", keys["server.address"])
	assert.Equal(t, "5433", keys["server.port"])
	assert.Equal(t, "foldex", keys["db.namespace"])
	assert.NotContains(t, keys, "user.name",
		"user.name is the POSTGRES ROLE; a trace that also carries user.id (the account) "+
			"would read as though the two described the same subject — see connAttrs")
}
