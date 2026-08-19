//go:build integration

package push_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/push"
	"foldex/internal/testdb"
)

func TestSubscriptionRepo_SaveListDelete(t *testing.T) {
	pool := testdb.Shared(t)

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

	require.NoError(t, repo.DeleteGone(ctx, uid, []int64{s1.ID}))
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
	require.NoError(t, repo.MarkUsed(ctx, uid, []int64{id2}))
	list, err = repo.List(ctx, uid)
	require.NoError(t, err)
	for _, s := range list {
		if s.ID == id2 {
			assert.NotNil(t, s.LastUsedAt)
		}
	}
}

func TestSubscriptionRepo_CapAllowsOwnedEndpointUpsert(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "cap-owner@test.local", "editor")
	repo := push.NewRepository(pool)
	ctx := context.Background()

	var first push.Subscription
	for i := range push.MaxSubscriptionsPerUser {
		sub, err := repo.Save(ctx, uid, fmt.Sprintf("https://push.example/cap/%d", i), "key", "auth")
		require.NoError(t, err)
		if i == 0 {
			first = sub
		}
	}

	updated, err := repo.Save(ctx, uid, first.Endpoint, "updated-key", "updated-auth")
	require.NoError(t, err)
	assert.Equal(t, first.ID, updated.ID)
	assert.Equal(t, "updated-key", updated.P256dh)

	_, err = repo.Save(ctx, uid, "https://push.example/cap/overflow", "key", "auth")
	require.ErrorIs(t, err, push.ErrSubscriptionLimit)

	subs, err := repo.List(ctx, uid)
	require.NoError(t, err)
	assert.Len(t, subs, push.MaxSubscriptionsPerUser)
}

func TestSubscriptionRepo_ConcurrentSavesNeverExceedCap(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "concurrent-cap@test.local", "editor")
	repo := push.NewRepository(pool)
	ctx := context.Background()

	for i := range push.MaxSubscriptionsPerUser - 1 {
		_, err := repo.Save(ctx, uid, fmt.Sprintf("https://push.example/existing/%d", i), "key", "auth")
		require.NoError(t, err)
	}

	blocker, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var lockedUser int64
	require.NoError(t, blocker.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 FOR UPDATE`, int64(uid)).Scan(&lockedUser))

	results := make(chan error, 2)
	for i := range 2 {
		go func() {
			_, saveErr := repo.Save(ctx, uid, fmt.Sprintf("https://push.example/concurrent/%d", i), "key", "auth")
			results <- saveErr
		}()
	}
	waitForBlockedSubscriptionSaves(t, pool, 2)
	require.NoError(t, blocker.Commit(ctx))

	var saved, refused int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			saved++
		case errors.Is(err, push.ErrSubscriptionLimit):
			refused++
		default:
			require.NoError(t, err)
		}
	}
	assert.Equal(t, 1, saved)
	assert.Equal(t, 1, refused)

	subs, err := repo.List(ctx, uid)
	require.NoError(t, err)
	assert.Len(t, subs, push.MaxSubscriptionsPerUser)
}

func TestSubscriptionRepo_ListIsOwnerScopedAndBounded(t *testing.T) {
	pool := testdb.Shared(t)
	owner := testdb.SeedUser(t, pool, "bounded-owner@test.local", "editor")
	other := testdb.SeedUser(t, pool, "bounded-other@test.local", "editor")
	ctx := context.Background()

	for i := range push.MaxSubscriptionsPerUser + 3 {
		_, err := pool.Exec(ctx, `
			INSERT INTO push_subscription (user_id, endpoint, p256dh, auth)
			VALUES ($1, $2, 'key', 'auth')
		`, int64(owner), fmt.Sprintf("https://push.example/legacy/%d", i))
		require.NoError(t, err)
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO push_subscription (user_id, endpoint, p256dh, auth)
		VALUES ($1, 'https://push.example/other', 'key', 'auth')
	`, int64(other))
	require.NoError(t, err)

	subs, err := push.NewRepository(pool).List(ctx, owner)
	require.NoError(t, err)
	require.Len(t, subs, push.MaxSubscriptionsPerUser)
	for _, sub := range subs {
		assert.NotEqual(t, "https://push.example/other", sub.Endpoint)
	}
}

func TestSubscriptionRepo_StaleOwnerResultsCannotMutateReassignedEndpoint(t *testing.T) {
	pool := testdb.Shared(t)
	userA := testdb.SeedUser(t, pool, "stale-a@test.local", "editor")
	userB := testdb.SeedUser(t, pool, "stale-b@test.local", "editor")
	repo := push.NewRepository(pool)
	ctx := context.Background()
	const endpoint = "https://push.example/reassigned"

	stale, err := repo.Save(ctx, userA, endpoint, "a-key", "a-auth")
	require.NoError(t, err)
	reassigned, err := repo.Save(ctx, userB, endpoint, "b-key", "b-auth")
	require.NoError(t, err)
	require.Equal(t, stale.ID, reassigned.ID)

	require.NoError(t, repo.MarkUsed(ctx, userA, []int64{stale.ID}))
	require.NoError(t, repo.DeleteGone(ctx, userA, []int64{stale.ID}))

	subsA, err := repo.List(ctx, userA)
	require.NoError(t, err)
	assert.Empty(t, subsA)
	subsB, err := repo.List(ctx, userB)
	require.NoError(t, err)
	require.Len(t, subsB, 1)
	assert.Equal(t, stale.ID, subsB[0].ID)
	assert.Equal(t, "b-key", subsB[0].P256dh)
	assert.Nil(t, subsB[0].LastUsedAt)
}

func waitForBlockedSubscriptionSaves(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var blocked int
		err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query LIKE '%SELECT id FROM app_user WHERE id = $1 FOR NO KEY UPDATE%'
		`).Scan(&blocked)
		return err == nil && blocked >= want
	}, 3*time.Second, 10*time.Millisecond)
}

// Disabling an account revokes its sessions and kills its API tokens, but a
// Web Push subscription is a browser channel that survives both — it was
// installed by the service worker and nothing in the disable path touches it.
// Without an owner-status check the disabled account keeps receiving "this page
// changed" notifications on a device that can no longer sign in.
//
// Checked at LIST, which is the single door Notify goes through, so the answer
// reflects the account's state at delivery rather than whenever the sweep
// happened to claim the link.
func TestSubscriptionRepo_ListSkipsADisabledOwner(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "disabled-owner@test.local", "editor")
	repo := push.NewRepository(pool)
	ctx := context.Background()

	_, err := repo.Save(ctx, uid, "https://push.example/disabled-ep", "k", "a")
	require.NoError(t, err)

	live, err := repo.List(ctx, uid)
	require.NoError(t, err)
	require.Len(t, live, 1, "an active owner must still get their subscriptions")

	_, err = pool.Exec(ctx, `UPDATE app_user SET status = 'disabled' WHERE id = $1`, int64(uid))
	require.NoError(t, err)

	after, err := repo.List(ctx, uid)
	require.NoError(t, err)
	assert.Empty(t, after, "a disabled owner must not be delivered to")

	// The row itself survives: disabling is reversible, and deleting the
	// channel here would silently unsubscribe a browser that could be
	// re-enabled a minute later with no way to notice it had happened.
	var stored int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM push_subscription WHERE user_id = $1`, int64(uid)).Scan(&stored))
	assert.Equal(t, 1, stored)

	_, err = pool.Exec(ctx, `UPDATE app_user SET status = 'active' WHERE id = $1`, int64(uid))
	require.NoError(t, err)
	back, err := repo.List(ctx, uid)
	require.NoError(t, err)
	assert.Len(t, back, 1, "re-enabling the account restores delivery")
}
