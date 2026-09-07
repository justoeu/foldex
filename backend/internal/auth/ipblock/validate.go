package ipblock

import (
	"errors"
	"net/netip"
	"time"
)

// IPBlock is one permanent blocklist entry — ADR-46.
type IPBlock struct {
	ID        int64     `json:"id"`
	IP        string    `json:"ip"`
	Reason    *string   `json:"reason"`
	CreatedBy *string   `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	ErrBlockMalformed = errors.New("not an ip address")
	ErrBlockSelf      = errors.New("that is the address you are connected from")
	ErrBlockLoopback  = errors.New("loopback is how the instance is administered locally")
	ErrBlockProxy     = errors.New("that address is a configured trusted proxy")
	ErrBlockFull      = errors.New("the blocklist is full")
)

// MaxIPBlocks bounds the table. The enforcement path holds every entry in a
// map in memory and consults it on every request; an unbounded list is an
// unbounded per-request working set, installed by a control whose whole purpose
// is to be clicked in a hurry.
const MaxIPBlocks = 1000

// ValidateBlockIP applies the rails BEFORE anything is written.
//
// Every one of them exists because this control's failure mode is not "a block
// that does not work" — it is an instance nobody can reach, installed by the
// person who most needed to reach it, through a button placed next to a scary
// red number. There is no undo from outside: the unblock endpoint is behind the
// same lock as everything else.
func ValidateBlockIP(candidate, callerIP string, isTrustedProxy func(string) bool) (string, error) {
	addr, err := netip.ParseAddr(Normalize(candidate))
	if err != nil {
		return "", ErrBlockMalformed
	}
	norm := addr.String()
	if norm == Normalize(callerIP) {
		return "", ErrBlockSelf
	}
	if addr.IsLoopback() || addr.IsUnspecified() {
		return "", ErrBlockLoopback
	}
	if isTrustedProxy != nil && isTrustedProxy(norm) {
		return "", ErrBlockProxy
	}
	return norm, nil
}
