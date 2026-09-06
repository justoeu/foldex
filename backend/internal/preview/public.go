package preview

import (
	"context"

	"foldex/internal/pkg/netpolicy"
)

// IsPublicURL is a documented alias of netpolicy.IsPublicURL (INV-079).
// Kept so the preview worker and its tests share one name without duplicating
// SSRF guards. Strict by design — there is no env opt-out. The default preview
// HTML fetch stays permissive because intranet links are foldex's primary use
// case; this gate is only the expensive screenshot fallback.
func IsPublicURL(ctx context.Context, pageURL string) bool {
	return netpolicy.IsPublicURL(ctx, pageURL)
}
