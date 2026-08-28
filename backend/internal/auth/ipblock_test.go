package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rails are the whole safety of this feature. Its failure mode is not "a
// block that does not work" — it is an instance nobody can reach, installed by
// the person who most needed to reach it, from a button next to a scary number.
func TestValidateBlockIP_RefusesTheAddressYouAreCallingFrom(t *testing.T) {
	_, err := ValidateBlockIP("203.0.113.9", "203.0.113.9", nil)
	assert.ErrorIs(t, err, ErrBlockSelf)
}

// The self rail compares NORMALIZED addresses. An operator connected over IPv6
// whose own address arrives as "::ffff:203.0.113.9" while the screen offers
// "203.0.113.9" is one spelling away from locking themselves out.
func TestValidateBlockIP_SelfRailSurvivesRespelling(t *testing.T) {
	for _, caller := range []string{"::ffff:203.0.113.9", "203.0.113.9:54321", "[::ffff:203.0.113.9]"} {
		_, err := ValidateBlockIP("203.0.113.9", caller, nil)
		assert.ErrorIs(t, err, ErrBlockSelf, "caller spelling %q", caller)
	}
}

// Loopback is how a local operator administers the instance, and it is the
// ENTIRE access path when AUTH_ENABLED=0. Blocking it removes the escape hatch
// that exists for exactly this kind of lockout.
func TestValidateBlockIP_RefusesLoopbackAndUnspecified(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "127.0.0.53", "::1", "0.0.0.0", "::"} {
		_, err := ValidateBlockIP(ip, "203.0.113.9", nil)
		assert.ErrorIs(t, err, ErrBlockLoopback, "address %q", ip)
	}
}

// Behind nginx every request arrives from the proxy whenever the forwarding
// chain is not configured as intended — and the screen would then show that
// address as the busiest origin, which is precisely what invites the click.
// The test uses a NETWORK, because the configuration accepts CIDRs and string
// equality would pass anything the network merely covers.
func TestValidateBlockIP_RefusesATrustedProxyByNetwork(t *testing.T) {
	inTenNet := func(ip string) bool { return len(ip) >= 3 && ip[:3] == "10." }
	_, err := ValidateBlockIP("10.4.2.7", "203.0.113.9", inTenNet)
	assert.ErrorIs(t, err, ErrBlockProxy)

	ok, err := ValidateBlockIP("198.51.100.4", "203.0.113.9", inTenNet)
	require.NoError(t, err)
	assert.Equal(t, "198.51.100.4", ok)
}

func TestValidateBlockIP_RefusesGarbage(t *testing.T) {
	for _, ip := range []string{"", "not-an-ip", "999.1.1.1", "10.0.0.0/8", "; DROP TABLE"} {
		_, err := ValidateBlockIP(ip, "203.0.113.9", nil)
		assert.ErrorIs(t, err, ErrBlockMalformed, "input %q", ip)
	}
}

// A stored address and a compared one must be one string. Two spellings of the
// same host would mean a block that is installed and never matches.
func TestValidateBlockIP_NormalizesWhatItReturns(t *testing.T) {
	got, err := ValidateBlockIP("::ffff:198.51.100.4", "203.0.113.9", nil)
	require.NoError(t, err)
	assert.Equal(t, "198.51.100.4", got, "an IPv4-mapped address must collapse to its v4 form")

	got, err = ValidateBlockIP("198.51.100.4:8080", "203.0.113.9", nil)
	require.NoError(t, err)
	assert.Equal(t, "198.51.100.4", got, "a port must not become part of the identity")
}

// The cache sits in front of every request. Failing closed on a database blip
// would turn a transient error into a total outage — and the people locked out
// would include the ones who could fix it.
func TestBlocklist_FailsOpenWhenTheLoadErrors(t *testing.T) {
	list := NewBlocklist(func(context.Context) ([]string, error) {
		return nil, errors.New("database is down")
	})
	assert.False(t, list.Blocked(context.Background(), "203.0.113.9"),
		"a failed refresh must not block anybody")
}

// A failed refresh must not become one reload attempt per request on top of a
// database that is already struggling.
func TestBlocklist_BacksOffAfterAFailedLoad(t *testing.T) {
	var calls int
	var mu sync.Mutex
	list := NewBlocklist(func(context.Context) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return nil, errors.New("down")
	})
	for i := 0; i < 20; i++ {
		list.Blocked(context.Background(), "203.0.113.9")
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, calls, "the TTL must apply to failures too")
}

// A refresh that fails after a good one must keep serving the good snapshot
// rather than silently unblocking everybody.
func TestBlocklist_KeepsTheLastGoodSnapshotOnFailure(t *testing.T) {
	fail := false
	var mu sync.Mutex
	list := NewBlocklist(func(context.Context) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return nil, errors.New("down")
		}
		return []string{"203.0.113.9"}, nil
	})
	require.True(t, list.Blocked(context.Background(), "203.0.113.9"))

	mu.Lock()
	fail = true
	mu.Unlock()
	list.Invalidate()
	assert.True(t, list.Blocked(context.Background(), "203.0.113.9"),
		"a failed refresh must keep the previous answer, not discard it")
}

// A block installed through the API has to take effect while the person who
// installed it is still watching, not up to a TTL later.
func TestBlocklist_InvalidateMakesTheNextLookupReload(t *testing.T) {
	blocked := []string{}
	var mu sync.Mutex
	list := NewBlocklist(func(context.Context) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, blocked...), nil
	})
	require.False(t, list.Blocked(context.Background(), "203.0.113.9"))

	mu.Lock()
	blocked = []string{"203.0.113.9"}
	mu.Unlock()
	assert.False(t, list.Blocked(context.Background(), "203.0.113.9"),
		"without Invalidate the snapshot must still be the cached one")
	list.Invalidate()
	assert.True(t, list.Blocked(context.Background(), "203.0.113.9"))
}

// An empty address is what an unparseable RemoteAddr normalizes to. Treating it
// as a lookup key would let one malformed entry match every such request.
func TestBlocklist_EmptyAddressIsNeverBlocked(t *testing.T) {
	list := NewBlocklist(func(context.Context) ([]string, error) { return []string{""}, nil })
	assert.False(t, list.Blocked(context.Background(), ""))
}

// The lookup runs on every request; the refresh must not race itself.
func TestBlocklist_ConcurrentLookupsAreSafe(t *testing.T) {
	list := NewBlocklist(func(context.Context) ([]string, error) {
		return []string{"203.0.113.9"}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			list.Invalidate()
			list.Blocked(context.Background(), "203.0.113.9")
		}()
	}
	wg.Wait()
}

func TestBlocklistTTL_IsShortEnoughToWatchAndLongEnoughToCache(t *testing.T) {
	assert.LessOrEqual(t, BlocklistTTL, time.Minute)
	assert.GreaterOrEqual(t, BlocklistTTL, 5*time.Second)
}
