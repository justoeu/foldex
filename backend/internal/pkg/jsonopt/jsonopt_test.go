package jsonopt_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/jsonopt"
)

func TestDecodeOptionalInt64(t *testing.T) {
	t.Parallel()
	set, v, err := jsonopt.DecodeOptionalInt64(nil)
	require.NoError(t, err)
	require.False(t, set)
	require.Nil(t, v)

	set, v, err = jsonopt.DecodeOptionalInt64(json.RawMessage("null"))
	require.NoError(t, err)
	require.True(t, set)
	require.Nil(t, v)

	set, v, err = jsonopt.DecodeOptionalInt64(json.RawMessage("42"))
	require.NoError(t, err)
	require.True(t, set)
	require.Equal(t, int64(42), *v)
}

func TestDecodeOptionalString(t *testing.T) {
	t.Parallel()
	set, v, err := jsonopt.DecodeOptionalString(json.RawMessage(`"  hi  "`), true)
	require.NoError(t, err)
	require.True(t, set)
	require.Equal(t, "hi", *v)

	set, v, err = jsonopt.DecodeOptionalString(json.RawMessage(`"  "`), true)
	require.NoError(t, err)
	require.True(t, set)
	require.Nil(t, v)

	set, v, err = jsonopt.DecodeOptionalStringRaw(json.RawMessage(`""`))
	require.NoError(t, err)
	require.True(t, set)
	require.NotNil(t, v)
	require.Equal(t, "", *v)
}
