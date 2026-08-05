package folders

import (
	"strconv"
	"time"

	"foldex/internal/pkg/attemptlimit"
)

// Per-folder brute-force throttle for the unlock endpoint (ADR-28): after
// maxUnlockAttempts consecutive wrong passwords a folder is locked out for
// unlockLockout before another attempt is accepted. A correct password resets
// the counter.
//
// The mechanism itself lives in internal/pkg/attemptlimit — this file holds
// only the folder-specific POLICY (the cap, the lockout window, the key
// shape). The two were separate implementations until they drifted into ~150
// near-identical lines; the shared one is a strict superset (configurable cap,
// stale-entry Sweep) and carries the concurrency guarantee in one place.
//
// State is in-memory only: single-user/local threat model, so a restart
// clearing the counters (and lifting a lockout early) is acceptable — bcrypt's
// cost per attempt is the real floor.
const (
	maxUnlockAttempts = 5
	unlockLockout     = time.Hour
)

func newUnlockLimiter() *attemptlimit.Limiter {
	return attemptlimit.New(maxUnlockAttempts, unlockLockout)
}

// unlockKeyFor is the limiter key for a folder.
//
// The folder id alone is enough, with no tenant segment: ids are globally
// unique BIGSERIAL and the handler proves ownership (via the uid-scoped
// PasswordHashFor) BEFORE it reserves an attempt slot, so one tenant's budget
// can never be reached through another tenant's request.
func unlockKeyFor(id int64) string { return strconv.FormatInt(id, 10) }
