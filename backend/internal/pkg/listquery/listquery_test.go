package listquery_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
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

func TestPlannerBuildsOwnerFirstComposedScopeAndClampedPage(t *testing.T) {
	t.Parallel()
	folderID := int64(17)
	planner := listquery.NewPlanner(listquery.Params{
		Q: "needle", TagIDs: []int64{7, 9}, Sort: "alpha",
		Limit: 999, Offset: -4, FolderID: &folderID, Ungrouped: true,
	})
	scope := planner.AddScope(authctx.UserID(42), listquery.LinkEntity("unlocked(l)"))
	page := planner.AddPage(listquery.LinkOrder())

	require.Equal(t, 1, scope.OwnerArg)
	require.Equal(t, "l.user_id = $1", scope.Where[0], "the owner predicate must lead every entity WHERE")
	require.Contains(t, scope.Where[1], "l.title ILIKE $2")
	require.Contains(t, scope.Where[2], "entity_kind = 'link'")
	require.Contains(t, scope.Where[2], "tag_id = ANY($3)")
	require.Equal(t, "l.folder_id = $4", scope.Where[3], "folder scope must win over ungrouped")
	require.NotContains(t, strings.Join(scope.Where, " "), "unlocked(l)")
	require.Equal(t, "l.pinned DESC, lower(l.title) ASC, l.created_at DESC, l.id ASC", page.OrderBy)
	require.False(t, page.ClickRanking)
	require.Equal(t, 5, page.LimitArg)
	require.Equal(t, 6, page.OffsetArg)
	require.Equal(t, []any{int64(42), "%needle%", []int64{7, 9}, int64(17), 100, 0}, planner.Args())
}

func TestPlannerScopeVariantsAndUnionOwnerArguments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		q    listquery.Params
		want string
	}{
		{name: "ungrouped", q: listquery.Params{Ungrouped: true}, want: "n.folder_id IS NULL"},
		{name: "unscoped excludes locked", q: listquery.Params{}, want: "unlocked(n)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			planner := listquery.NewPlanner(tc.q)
			scope := planner.AddScope(authctx.UserID(8), listquery.NoteEntity("unlocked(n)"))
			require.Equal(t, tc.want, scope.Where[len(scope.Where)-1])
		})
	}

	planner := listquery.NewPlanner(listquery.Params{Q: "x"})
	linkScope := planner.AddScope(authctx.UserID(11), listquery.LinkEntity(""))
	noteScope := planner.AddScope(authctx.UserID(11), listquery.NoteEntity(""))
	require.Equal(t, 1, linkScope.OwnerArg)
	require.Equal(t, 3, noteScope.OwnerArg)
	require.Equal(t, "l.user_id = $1", linkScope.Where[0])
	require.Equal(t, "n.user_id = $3", noteScope.Where[0])
}

func TestPlannerPreservesPinnedFirstSortSemantics(t *testing.T) {
	t.Parallel()
	columns := listquery.UnionOrder()
	tests := []struct {
		sort         string
		want         string
		clickRanking bool
	}{
		{sort: "", want: "pinned DESC, created_at DESC, kind ASC, id ASC"},
		{sort: "created", want: "pinned DESC, created_at DESC, kind ASC, id ASC"},
		{sort: "clicks", want: "pinned DESC, click_count DESC, created_at DESC, kind ASC, id ASC", clickRanking: true},
		{sort: "recent", want: "pinned DESC, COALESCE(last_clicked_at, created_at) DESC, kind ASC, id ASC", clickRanking: true},
		{sort: "alpha", want: "pinned DESC, lower(title) ASC, created_at DESC, kind ASC, id ASC"},
		{sort: "alpha_desc", want: "pinned DESC, lower(title) DESC, created_at DESC, kind ASC, id ASC"},
	}
	for _, tc := range tests {
		t.Run(tc.sort, func(t *testing.T) {
			planner := listquery.NewPlanner(listquery.Params{Sort: tc.sort})
			page := planner.AddPage(columns)
			require.Equal(t, tc.want, page.OrderBy)
			require.Equal(t, tc.clickRanking, page.ClickRanking)
			require.Equal(t, []any{100, 0}, planner.Args())
		})
	}
}
