package listquery_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/listquery"
)

func TestParse(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/?q=hi&sort=alpha&tag=1&tag=bad&tag=2&limit=50&offset=10&folder_id=7&ungrouped=1", nil)
	p := listquery.Parse(r)
	require.Equal(t, "hi", p.Q)
	require.Equal(t, "alpha", p.Sort)
	require.Equal(t, []int64{1, 2}, p.TagIDs)
	require.Equal(t, 50, p.Limit)
	require.Equal(t, 10, p.Offset)
	require.NotNil(t, p.FolderID)
	require.Equal(t, int64(7), *p.FolderID)
	require.True(t, p.Ungrouped)
}
