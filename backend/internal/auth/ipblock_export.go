package auth

import (
	"context"

	"foldex/internal/auth/ipblock"
)

type IPBlock = ipblock.IPBlock
type Blocklist = ipblock.Blocklist

const (
	MaxIPBlocks  = ipblock.MaxIPBlocks
	BlocklistTTL = ipblock.BlocklistTTL
)

var (
	ErrBlockMalformed = ipblock.ErrBlockMalformed
	ErrBlockSelf      = ipblock.ErrBlockSelf
	ErrBlockLoopback  = ipblock.ErrBlockLoopback
	ErrBlockProxy     = ipblock.ErrBlockProxy
	ErrBlockFull      = ipblock.ErrBlockFull
)

func ValidateBlockIP(candidate, callerIP string, isTrustedProxy func(string) bool) (string, error) {
	return ipblock.ValidateBlockIP(candidate, callerIP, isTrustedProxy)
}

func NormalizeIP(raw string) string { return ipblock.Normalize(raw) }

func NewBlocklist(load func(context.Context) ([]string, error)) *ipblock.Blocklist {
	return ipblock.NewBlocklist(load)
}
