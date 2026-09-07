//go:build integration

package notes_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/notes"
	"foldex/internal/pkg/authctx/authctxtest"
	"foldex/internal/testdb"
)

func boolPtr(b bool) *bool { return &b }

func TestPublicHandler_RendersSanitizedHTML(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := notes.NewRepository(pool)
	created, err := repo.Create(context.Background(), uid, notes.CreateInput{
		Title:    "<b>Bold</b> Title",
		BodyHTML: "<p>hello <strong>world</strong></p>",
	})
	require.NoError(t, err)

	r := chi.NewRouter()
	// Owner session: rendering + escaping are viewer-independent, and the
	// owner path is the one that must keep working without an opt-in.
	r.Use(authctxtest.Middleware(uid))
	notes.NewPublicHandler(repo, true).Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/n/"+created.Slug, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body, err := io.ReadAll(rr.Body)
	require.NoError(t, err)
	html := string(body)
	assert.Contains(t, html, "<strong>world</strong>")
	// Title is plain text rendered through {{.Title}} — must be HTML-escaped,
	// not interpreted as markup.
	assert.Contains(t, html, "&lt;b&gt;Bold&lt;/b&gt; Title")
	assert.NotContains(t, html, "<b>Bold</b> Title")

	got, err := repo.Get(context.Background(), uid, created.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.ClickCount, "viewing the public page must log a click")
}

func TestPublicHandler_ByID(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := notes.NewRepository(pool)
	created, err := repo.Create(context.Background(), uid, notes.CreateInput{Title: "ById", BodyHTML: "<p>x</p>"})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	notes.NewPublicHandler(repo, true).Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/n/"+idStr(created.ID), nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestPublicHandler_NotFound(t *testing.T) {
	pool := testdb.Shared(t)

	_ = testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := notes.NewRepository(pool)
	r := chi.NewRouter()
	notes.NewPublicHandler(repo, true).Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/n/does-not-exist", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// SEC-SEN-001: a title-derived slug is not a capability. Two tenants titling
// a note the same get walking slugs (meeting-notes, meeting-notes-2, …), so an
// anonymous reader who can guess a title can read every tenant's note with
// that title — and 200-vs-404 confirms which titles exist. Without an explicit
// is_public opt-in, the anonymous page must answer 404 and record no view.
func TestPublicHandler_SlugWalkAcrossTenantsIsDeniedWithoutOptIn(t *testing.T) {
	pool := testdb.Shared(t)

	alice := testdb.SeedUser(t, pool, "alice-slugwalk@test.local", "admin")
	bravo := testdb.SeedUser(t, pool, "bravo-slugwalk@test.local", "admin")
	repo := notes.NewRepository(pool)
	first, err := repo.Create(context.Background(), alice, notes.CreateInput{
		Title: "Meeting Notes", BodyHTML: "<p>alice's private agenda</p>",
	})
	require.NoError(t, err)
	require.Equal(t, "meeting-notes", first.Slug, "slug auto-derived from title")
	second, err := repo.Create(context.Background(), bravo, notes.CreateInput{
		Title: "Meeting Notes", BodyHTML: "<p>bravo's private agenda</p>",
	})
	require.NoError(t, err)
	require.Equal(t, "meeting-notes-2", second.Slug, "global namespace suffixes the collision — the walk's next step")

	r := chi.NewRouter()
	notes.NewPublicHandler(repo, false).Mount(r)

	for _, slug := range []string{first.Slug, second.Slug} {
		req := httptest.NewRequest(http.MethodGet, "/n/"+slug, nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code, "anonymous slug walk on %q", slug)
	}

	var views int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM click_log WHERE entity_kind = 'note' AND entity_id = ANY($1)`,
		[]int64{first.ID, second.ID}).Scan(&views))
	assert.Zero(t, views, "a denied walk must not forge view rows either")
}

// The share capability is explicit: only the owner opting a note in makes the
// anonymous page render it.
func TestPublicHandler_OptedInNoteRendersForAnonymous(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := notes.NewRepository(pool)
	created, err := repo.Create(context.Background(), uid, notes.CreateInput{
		Title: "Shared Reading List", BodyHTML: "<p>hello</p>",
	})
	require.NoError(t, err)
	published, err := repo.Update(context.Background(), uid, created.ID, notes.UpdateInput{
		IsPublic: boolPtr(true),
	})
	require.NoError(t, err)
	require.True(t, published.IsPublic, "the owner must be able to opt a note in")

	r := chi.NewRouter()
	notes.NewPublicHandler(repo, false).Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/n/"+created.Slug, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	got, err := repo.Get(context.Background(), uid, created.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.ClickCount, "an opted-in public view still counts")
}

// Owner access is unchanged: the owner's own session keeps reading (and
// counting views of) their note even while it stays private to the world.
func TestPublicHandler_OwnerSessionReadsPrivateNote(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := notes.NewRepository(pool)
	created, err := repo.Create(context.Background(), uid, notes.CreateInput{
		Title: "Private Draft", BodyHTML: "<p>draft</p>",
	})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	notes.NewPublicHandler(repo, false).Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/n/"+created.Slug, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "the owner's session must keep the page working")
}

// A signed-in stranger gets the same 404 an anonymous reader gets — the page
// is not merely "authenticated users only", it is owner-or-opted-in.
func TestPublicHandler_AnotherUsersSessionIsDenied(t *testing.T) {
	pool := testdb.Shared(t)

	alice := testdb.SeedUser(t, pool, "alice-session@test.local", "admin")
	bravo := testdb.SeedUser(t, pool, "bravo-session@test.local", "admin")
	repo := notes.NewRepository(pool)
	created, err := repo.Create(context.Background(), alice, notes.CreateInput{
		Title: "Alice Private", BodyHTML: "<p>alice</p>",
	})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(bravo))
	notes.NewPublicHandler(repo, false).Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/n/"+created.Slug, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code, "another tenant's session must not read the note")
}

func TestPublicHandler_PublicNumericIDsFeatureFlag(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := notes.NewRepository(pool)
	created, err := repo.Create(context.Background(), uid, notes.CreateInput{Title: "Flag Note", BodyHTML: "<p>x</p>"})
	require.NoError(t, err)

	tests := []struct {
		name            string
		target          string
		allowNumericIDs bool
		wantStatus      int
	}{
		{name: "numeric on", target: idStr(created.ID), allowNumericIDs: true, wantStatus: http.StatusOK},
		{name: "numeric off", target: idStr(created.ID), allowNumericIDs: false, wantStatus: http.StatusNotFound},
		{name: "slug while numeric off", target: created.Slug, allowNumericIDs: false, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := chi.NewRouter()
			r.Use(authctxtest.Middleware(uid))
			notes.NewPublicHandler(repo, tt.allowNumericIDs).Mount(r)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/n/"+tt.target, nil)
			r.ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}
