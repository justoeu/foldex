package auth

import (
	"testing"
	"time"

	"foldex/internal/abusepolicy"
)

// The sweeper's memory horizon must outlive the widest login window an owner
// can configure.
//
// The login-by-IP bucket counts DISTINCT ACCOUNTS inside that window, and its
// members live in the entry the sweeper deletes. If the horizon were the
// shorter of the two, a sweep would forget a sweep in progress: an origin nine
// accounts into a spray would be handed a clean slate for having paused. The
// two numbers live in different packages, one is owner-configurable, and
// nothing but this test connects them.
func TestMemoryRetainOutlivesTheWidestLoginWindow(t *testing.T) {
	t.Parallel()
	widest := time.Duration(abusepolicy.MaxLoginWindowMinutes) * time.Minute
	retain := (&Sweeper{}).memoryRetain()
	if retain < widest {
		t.Fatalf("memoryRetain() = %s but an owner may configure a login window of %s; "+
			"the sweep would drop member sets that are still inside their window, forgiving "+
			"a spray for pausing", retain, widest)
	}
}
