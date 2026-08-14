// Package publictarget defines how public link and note paths choose between
// legacy numeric IDs and slugs.
package publictarget

import (
	"context"

	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/publicid"
)

// Resolve applies the PUBLIC_NUMERIC_IDS policy, then delegates the endpoint-
// specific lookup to the matching injected function.
func Resolve[T any](
	ctx context.Context,
	raw string,
	allowNumericIDs bool,
	byID func(context.Context, int64) (T, error),
	bySlug func(context.Context, string) (T, error),
) (T, error) {
	if id, numeric := publicid.Parse(raw); numeric {
		if !allowNumericIDs {
			var zero T
			return zero, domainerr.ErrNotFound
		}
		return byID(ctx, id)
	}
	return bySlug(ctx, raw)
}
