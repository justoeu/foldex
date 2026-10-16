package stats

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/testsupport"
)

func TestRepositoryClosedPool_EveryMethodErrors(t *testing.T) {
	t.Parallel()
	repo := NewRepository(testsupport.ClosedPool(t))
	ctx := context.Background()
	uid := authctx.UserID(1)

	_, err := repo.Dashboard(ctx, uid, 60, 5)
	require.Error(t, err, "Dashboard")

	_, err = repo.Dashboard(ctx, uid, 0, 0)
	require.Error(t, err, "Dashboard with clamped days/limit")

	_, err = repo.Summary(ctx, uid)
	require.Error(t, err, "Summary")

	_, err = repo.Daily(ctx, uid, 60)
	require.Error(t, err, "Daily")

	_, err = repo.Daily(ctx, uid, 0)
	require.Error(t, err, "Daily with clamped days")

	_, err = repo.TopLinks(ctx, uid, 10)
	require.Error(t, err, "TopLinks")

	_, err = repo.TopLinks(ctx, uid, 0)
	require.Error(t, err, "TopLinks with clamped limit")

	_, err = repo.TagBuckets(ctx, uid)
	require.Error(t, err, "TagBuckets")

	_, err = repo.ObjectKeys(ctx, uid)
	require.Error(t, err, "ObjectKeys")
}
