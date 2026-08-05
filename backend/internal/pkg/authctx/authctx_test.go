package authctx_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

func TestWithPrincipalRoundTrips(t *testing.T) {
	want := authctx.Principal{
		UserID:    42,
		Role:      authctx.RoleAdmin,
		SessionID: 7,
		Via:       authctx.ViaSession,
	}

	got, ok := authctx.FromContext(authctx.WithPrincipal(context.Background(), want))

	require.True(t, ok)
	assert.Equal(t, want, got)
}

func TestFromContextOnBareContext(t *testing.T) {
	_, ok := authctx.FromContext(context.Background())

	assert.False(t, ok)
}

func TestUser(t *testing.T) {
	ctx := authctx.WithPrincipal(context.Background(), authctx.Principal{UserID: 9})

	uid, ok := authctx.User(ctx)
	require.True(t, ok)
	assert.Equal(t, authctx.UserID(9), uid)

	_, ok = authctx.User(context.Background())
	assert.False(t, ok)
}

func TestMustUser(t *testing.T) {
	ctx := authctx.WithPrincipal(context.Background(), authctx.Principal{UserID: 5})

	assert.Equal(t, authctx.UserID(5), authctx.MustUser(ctx))
}

// A missing principal must panic rather than yield user 0: a zero owner would
// silently match no rows, or be written into one.
func TestMustUserPanicsWithoutPrincipal(t *testing.T) {
	assert.Panics(t, func() { authctx.MustUser(context.Background()) })
}

func TestRoleIsAdmin(t *testing.T) {
	assert.True(t, authctx.RoleAdmin.IsAdmin())
	assert.False(t, authctx.RoleUser.IsAdmin())
	assert.False(t, authctx.Role("").IsAdmin())
}

// The principal must not leak across unrelated context keys.
func TestPrincipalKeyIsPrivate(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, authctx.Principal{UserID: 1})

	_, ok := authctx.FromContext(ctx)

	assert.False(t, ok)
}
