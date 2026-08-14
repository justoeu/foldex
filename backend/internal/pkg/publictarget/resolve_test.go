package publictarget

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/domainerr"
)

func TestResolve_TargetPolicy(t *testing.T) {
	tests := []struct {
		name            string
		raw             string
		allowNumericIDs bool
		wantCall        string
		wantID          int64
		wantNotFound    bool
	}{
		{name: "positive numeric enabled", raw: "42", allowNumericIDs: true, wantCall: "id", wantID: 42},
		{name: "positive numeric disabled", raw: "42", wantNotFound: true},
		{name: "max int64 enabled", raw: "9223372036854775807", allowNumericIDs: true, wantCall: "id", wantID: math.MaxInt64},
		{name: "max int64 disabled", raw: "9223372036854775807", wantNotFound: true},
		{name: "overflow enabled falls back to slug", raw: "9223372036854775808", allowNumericIDs: true, wantCall: "slug"},
		{name: "overflow disabled falls back to slug", raw: "9223372036854775808", wantCall: "slug"},
		{name: "wrapping overflow falls back to slug", raw: "18446744073709551617", allowNumericIDs: true, wantCall: "slug"},
		{name: "zero enabled falls back to slug", raw: "0", allowNumericIDs: true, wantCall: "slug"},
		{name: "zero disabled falls back to slug", raw: "0", wantCall: "slug"},
		{name: "positive sign enabled falls back to slug", raw: "+42", allowNumericIDs: true, wantCall: "slug"},
		{name: "positive sign disabled falls back to slug", raw: "+42", wantCall: "slug"},
		{name: "negative sign enabled falls back to slug", raw: "-42", allowNumericIDs: true, wantCall: "slug"},
		{name: "negative sign disabled falls back to slug", raw: "-42", wantCall: "slug"},
		{name: "numeric prefix slug enabled", raw: "42-notes", allowNumericIDs: true, wantCall: "slug"},
		{name: "numeric prefix slug disabled", raw: "42-notes", wantCall: "slug"},
		{name: "numeric suffix slug enabled", raw: "notes-42", allowNumericIDs: true, wantCall: "slug"},
		{name: "numeric suffix slug disabled", raw: "notes-42", wantCall: "slug"},
		{name: "ordinary slug enabled", raw: "release-notes", allowNumericIDs: true, wantCall: "slug"},
		{name: "ordinary slug disabled", raw: "release-notes", wantCall: "slug"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := []string{}
			got, err := Resolve(t.Context(), tt.raw, tt.allowNumericIDs,
				func(_ context.Context, id int64) (string, error) {
					calls = append(calls, "id")
					assert.Equal(t, tt.wantID, id)
					return "by-id", nil
				},
				func(_ context.Context, slug string) (string, error) {
					calls = append(calls, "slug")
					assert.Equal(t, tt.raw, slug)
					return "by-slug", nil
				},
			)
			if tt.wantNotFound {
				require.ErrorIs(t, err, domainerr.ErrNotFound)
				assert.Empty(t, calls)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []string{tt.wantCall}, calls)
			assert.Equal(t, "by-"+tt.wantCall, got)
		})
	}
}

func TestResolve_PropagatesInjectedRepositoryErrors(t *testing.T) {
	wantErr := errors.New("repository unavailable")
	_, err := Resolve(t.Context(), "42", true,
		func(context.Context, int64) (string, error) { return "", wantErr },
		func(context.Context, string) (string, error) { return "", nil },
	)
	require.ErrorIs(t, err, wantErr)

	_, err = Resolve(t.Context(), "slug", false,
		func(context.Context, int64) (string, error) { return "", nil },
		func(context.Context, string) (string, error) { return "", wantErr },
	)
	require.ErrorIs(t, err, wantErr)
}
