//go:build integration

package push_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/push"
	"foldex/internal/testdb"
)

func TestSubscriptionRepo_SaveListDelete(t *testing.T) {
	pool := testdb.New(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := push.NewRepository(pool)
	ctx := context.Background()

	s1, err := repo.Save(ctx, uid, "https://push.example/ep1", "p256dh-1", "auth-1")
	require.NoError(t, err)
	assert.Positive(t, s1.ID)
	assert.Equal(t, "https://push.example/ep1", s1.Endpoint)

	// Upsert same endpoint refreshes keys
	s2, err := repo.Save(ctx, uid, "https://push.example/ep1", "p256dh-2", "auth-2")
	require.NoError(t, err)
	assert.Equal(t, s1.ID, s2.ID)
	assert.Equal(t, "p256dh-2", s2.P256dh)

	_, err = repo.Save(ctx, uid, "https://push.example/ep2", "k", "a")
	require.NoError(t, err)

	list, err := repo.List(ctx, uid)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list), 2)

	require.NoError(t, repo.DeleteByEndpoint(ctx, "https://push.example/ep1"))
	list, err = repo.List(ctx, uid)
	require.NoError(t, err)
	for _, s := range list {
		assert.NotEqual(t, "https://push.example/ep1", s.Endpoint)
	}

	var id2 int64
	for _, s := range list {
		if s.Endpoint == "https://push.example/ep2" {
			id2 = s.ID
		}
	}
	require.Positive(t, id2)
	require.NoError(t, repo.MarkUsed(ctx, id2))
}
