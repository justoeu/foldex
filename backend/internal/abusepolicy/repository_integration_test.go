//go:build integration

package abusepolicy_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/abusepolicy"
	"foldex/internal/testdb"
)

// TestMain owns the lifetime of the package's shared Postgres container.
//
// os.Exit skips deferred work, and a t.Cleanup hung off whichever test ran
// first would tear the database down while the rest of the package still
// needed it. The Makefile disables testcontainers' reaper, so nothing else
// would collect the container — internal/security enforces this hook.
func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

// An instance that never opened the screen must run the compiled defaults, and
// the read must not be an error: this is on the login path.
func TestGet_AnAbsentRowIsTheDefaultPolicyAndNotAnError(t *testing.T) {
	pool := testdb.Shared(t)
	repo := abusepolicy.NewRepository(pool)

	got, err := repo.Get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, abusepolicy.Default(), got)
}

func TestSet_RoundTripsEveryKnob(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	repo := abusepolicy.NewRepository(pool)

	want := abusepolicy.Default()
	want.LoginDistinctAccountsPerIP = 7
	want.LoginFailuresPerAccount = 9
	want.LoginWindowMinutes = 45
	want.APIWritesPerMinute = 300
	want.APIExpensivePerHour = 50
	coalesce := 0
	want.PublicClickCoalesceSeconds = &coalesce
	want.AnomalySprayAccounts = 4
	want.AnomalyHammerFailures = 12
	want.AnomalyWindowMinutes = 60
	require.NoError(t, repo.Set(ctx, want))

	got, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	require.NotNil(t, got.PublicClickCoalesceSeconds)
	assert.Equal(t, 0, *got.PublicClickCoalesceSeconds,
		"a stored 0 is coalescing OFF, which is a supported configuration and "+
			"must survive the round trip as itself rather than as absent")

	// The second write is an UPDATE, not a duplicate-key error.
	want.LoginWindowMinutes = 20
	require.NoError(t, repo.Set(ctx, want))
	got, err = repo.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, 20, got.LoginWindowMinutes)
}

// A corrupted row must not become a total outage. This is read on the login
// path, and an instance whose settings row got mangled still has to be able to
// authenticate the person who would repair it.
func TestGet_AMangledDocumentIsTheDefault(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	repo := abusepolicy.NewRepository(pool)

	_, err := pool.Exec(ctx,
		`INSERT INTO app_setting (key, value) VALUES ('abuse_policy', $1)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, `{not json at all`)
	require.NoError(t, err)

	got, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, abusepolicy.Default(), got)
}

// The difference INV-169 paid for: a document that PARSES but holds one
// out-of-range knob reverts that KNOB, never the whole document. Reverting
// everything would silently switch off limits the owner deliberately set while
// the screen still showed them.
func TestGet_AnOutOfRangeKnobRevertsAloneAndTheRestSurvives(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	repo := abusepolicy.NewRepository(pool)

	_, err := pool.Exec(ctx,
		`INSERT INTO app_setting (key, value) VALUES ('abuse_policy', $1)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		`{"login_distinct_accounts_per_ip":999999,"login_failures_per_account":9,`+
			`"login_window_minutes":45,"api_writes_per_minute":300,`+
			`"api_expensive_per_hour":50,"public_click_coalesce_seconds":0,`+
			`"anomaly_spray_accounts":4,"anomaly_hammer_failures":12,`+
			`"anomaly_window_minutes":60}`)
	require.NoError(t, err)

	got, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, abusepolicy.Default().LoginDistinctAccountsPerIP,
		got.LoginDistinctAccountsPerIP, "the out-of-range knob reverts")
	assert.Equal(t, 9, got.LoginFailuresPerAccount, "and only it")
	assert.Equal(t, 45, got.LoginWindowMinutes)
	assert.Equal(t, 300, got.APIWritesPerMinute)
	assert.Equal(t, 50, got.APIExpensivePerHour)
	assert.Equal(t, 4, got.AnomalySprayAccounts)
	assert.Equal(t, 12, got.AnomalyHammerFailures)
	assert.Equal(t, 60, got.AnomalyWindowMinutes)
	require.NotNil(t, got.PublicClickCoalesceSeconds)
	assert.Equal(t, 0, *got.PublicClickCoalesceSeconds)
}

// Set refuses rather than clamps, and the message names the real numbers
// because the API returns it verbatim to the form.
func TestSet_RefusesAnOutOfBoundsDocumentWithTheRealNumbers(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	repo := abusepolicy.NewRepository(pool)

	bad := abusepolicy.Default()
	bad.LoginDistinctAccountsPerIP = 1
	err := repo.Set(ctx, bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "login_distinct_accounts_per_ip")
	assert.Contains(t, err.Error(), "3")

	// And nothing was written: a refused save must not half-apply.
	got, gerr := repo.Get(ctx)
	require.NoError(t, gerr)
	assert.Equal(t, abusepolicy.Default(), got)
}

// Get satisfies the enforcement seam, so the cache can be built on it.
func TestRepository_SatisfiesReader(t *testing.T) {
	var _ abusepolicy.Reader = abusepolicy.NewRepository(testdb.Shared(t))
}
