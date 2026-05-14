package db

import (
	"context"
	"testing"

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
