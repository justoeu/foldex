//go:build integration

package links_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/pkg/domainerr"
	sharedslug "foldex/internal/pkg/slug"
	"foldex/internal/tags"
	"foldex/internal/testdb"

	"foldex/internal/pkg/authctx"

	"foldex/internal/pkg/authctx/authctxtest"
)

func setup(t *testing.T) (context.Context, authctx.UserID, *links.Repository, *tags.Repository) {
	t.Helper()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	return context.Background(), uid, links.NewRepository(pool), tags.NewRepository(pool)
}

func recordCheckResult(t *testing.T, ctx context.Context, repo *links.Repository, id int64, claimedAt time.Time, result links.CheckResult) {
	t.Helper()
	applied, err := repo.SystemRecordCheckResult(ctx, id, claimedAt, result)
	require.NoError(t, err)
	require.True(t, applied)
}

func claimChecks(t *testing.T, ctx context.Context, repo *links.Repository, ids ...int64) map[int64]links.DueLink {
	t.Helper()
	due, err := repo.SystemFindDueForCheck(ctx, 1000)
	require.NoError(t, err)
	claims := make(map[int64]links.DueLink, len(ids))
	for _, claim := range due {
		claims[claim.ID] = claim
	}
	for _, id := range ids {
		require.Contains(t, claims, id)
	}
	return claims
}

func TestRepository_CreateAndGetWithTags(t *testing.T) {
	ctx, uid, lrepo, trepo := setup(t)

	tagJira, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "jira", Color: "#1f6feb"})
	require.NoError(t, err)
	tagDocs, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "docs", Color: "#a78bfa"})
	require.NoError(t, err)

	created, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL:         "https://jira.example/INV-1",
		Title:       "INV-1",
		TagIDs:      []int64{tagJira.ID, tagDocs.ID},
		PendingTags: []tags.CreateInput{{Name: "queued", Color: "#22C55E"}},
	})
	require.NoError(t, err)
	require.NotZero(t, created.ID)
	assert.Equal(t, "pending", created.PreviewStatus)
	require.Len(t, created.Tags, 3)
	assert.Contains(t, []string{created.Tags[0].Name, created.Tags[1].Name, created.Tags[2].Name}, "queued")

	// Verify Get also returns tags
	got, err := lrepo.Get(ctx, uid, created.ID)
	require.NoError(t, err)
	assert.Len(t, got.Tags, 3)
}

func TestRepository_CreateSlugRetryAndExhaustionReleaseTransactions(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := links.NewRepository(pool)

	_, err := pool.Exec(ctx, `
		INSERT INTO link (user_id, url, title, slug)
		SELECT $1, 'https://link-retry.example/' || n, 'Retry Link',
		       CASE WHEN n = 1 THEN 'retry-link' ELSE 'retry-link-' || n END
		FROM generate_series(1, 2) n
	`, int64(uid))
	require.NoError(t, err)
	before := pool.Stat().AcquiredConns()
	created, err := repo.Create(ctx, uid, links.CreateInput{
		URL: "https://link-retry.example/final", Title: "Retry Link",
		PendingTags: []tags.CreateInput{{Name: "retry-tag", Color: "#22C55E"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "retry-link-3", created.Slug)
	require.Len(t, created.Tags, 1)
	assert.Equal(t, before, pool.Stat().AcquiredConns(), "successful retries must release every replaced transaction")

	_, err = pool.Exec(ctx, `
		INSERT INTO link (user_id, url, title, slug)
		SELECT $1, 'https://link-exhaust.example/' || n, 'Exhaust Link',
		       CASE WHEN n = 1 THEN 'exhaust-link' ELSE 'exhaust-link-' || n END
		FROM generate_series(1, $2) n
	`, int64(uid), sharedslug.CreateMaxAttempts)
	require.NoError(t, err)
	before = pool.Stat().AcquiredConns()
	beforeAttempts := pool.Stat().AcquireCount()
	_, err = repo.Create(ctx, uid, links.CreateInput{
		URL: "https://link-exhaust.example/final", Title: "Exhaust Link",
		PendingTags: []tags.CreateInput{{Name: "exhaust-tag", Color: "#22C55E"}},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, sharedslug.ErrCreateExhausted)
	assert.EqualValues(t, sharedslug.CreateMaxAttempts, pool.Stat().AcquireCount()-beforeAttempts)
	assert.Equal(t, before, pool.Stat().AcquiredConns(), "exhaustion must not leave a transaction checked out")
	var tagCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM tag WHERE user_id = $1 AND name = 'exhaust-tag'`, int64(uid)).Scan(&tagCount))
	assert.Zero(t, tagCount, "pending tags must not escape an exhausted parent create")
}

func TestRepository_ListFiltersByQAndTagAND(t *testing.T) {
	ctx, uid, lrepo, trepo := setup(t)
	tagA, _ := trepo.Create(ctx, uid, tags.CreateInput{Name: "a", Color: "#fff"})
	tagB, _ := trepo.Create(ctx, uid, tags.CreateInput{Name: "b", Color: "#fff"})

	_, _ = lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/alpha", Title: "Alpha", TagIDs: []int64{tagA.ID}})
	_, _ = lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/beta", Title: "Beta", TagIDs: []int64{tagA.ID, tagB.ID}})
	_, _ = lrepo.Create(ctx, uid, links.CreateInput{URL: "https://other.com/gamma", Title: "Gamma", TagIDs: []int64{tagB.ID}})

	// Text filter
	out, err := lrepo.List(ctx, uid, links.ListQuery{Q: "example.com"})
	require.NoError(t, err)
	assert.Len(t, out, 2)

	// Tag AND filter: must have BOTH a and b
	out, err = lrepo.List(ctx, uid, links.ListQuery{TagIDs: []int64{tagA.ID, tagB.ID}})
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "Beta", out[0].Title)
}

func TestRepository_UpdateReplacesTagSet(t *testing.T) {
	ctx, uid, lrepo, trepo := setup(t)
	tagA, _ := trepo.Create(ctx, uid, tags.CreateInput{Name: "a", Color: "#fff"})
	tagB, _ := trepo.Create(ctx, uid, tags.CreateInput{Name: "b", Color: "#fff"})

	link, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://x", Title: "x", TagIDs: []int64{tagA.ID},
	})
	require.NoError(t, err)
	require.Len(t, link.Tags, 1)

	newTitle := "renamed"
	newTags := []int64{tagB.ID}
	updated, err := lrepo.Update(ctx, uid, link.ID, links.UpdateInput{
		Title:  &newTitle,
		TagIDs: &newTags,
	})
	require.NoError(t, err)
	assert.Equal(t, "renamed", updated.Title)
	require.Len(t, updated.Tags, 1)
	assert.Equal(t, "b", updated.Tags[0].Name, "tag set must be replaced atomically")
}

func TestRepository_ClickAndResolveIsAtomic(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	created, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://hn.example", Title: "HN"})
	require.NoError(t, err)

	url, err := lrepo.ClickAndResolve(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://hn.example", url)

	got, _ := lrepo.Get(ctx, uid, created.ID)
	assert.EqualValues(t, 1, got.ClickCount)
	require.NotNil(t, got.LastClickedAt)
}

func TestRepository_ClickAndResolveNotFound(t *testing.T) {
	ctx, _, lrepo, _ := setup(t)
	_, err := lrepo.ClickAndResolve(ctx, 999)
	assert.ErrorIs(t, err, domainerr.ErrNotFound)
}

func TestRepository_UpdatePreview(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	created, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x", Title: "x"})
	fav, og, desc := "https://x/fav.ico", "https://x/og.png", "desc"
	require.NoError(t, lrepo.SystemUpdatePreview(ctx, created.ID, links.StatusOK, &fav, &og, &desc, nil))

	got, _ := lrepo.Get(ctx, uid, created.ID)
	assert.Equal(t, string(links.StatusOK), got.PreviewStatus)
	require.NotNil(t, got.FaviconURL)
	assert.Equal(t, fav, *got.FaviconURL)
}

// TestUpdatePreview_StatusCAS_PendingOnly proves terminal ok/failed
// only apply while status is still pending; a second ok after ok is a no-op.
func TestUpdatePreview_StatusCAS_PendingOnly(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	created, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://cas.example", Title: "cas"})
	require.NoError(t, err)
	require.Equal(t, string(links.StatusPending), created.PreviewStatus)

	require.NoError(t, lrepo.SystemUpdatePreview(ctx, created.ID, links.StatusOK, nil, nil, nil, nil))
	got, err := lrepo.Get(ctx, uid, created.ID)
	require.NoError(t, err)
	assert.Equal(t, string(links.StatusOK), got.PreviewStatus)

	// Stale worker finishing after status left pending must not flip ok→ok via
	// overwriting metadata path when already ok — CAS rejects non-pending.
	msg := "stale"
	require.NoError(t, lrepo.SystemUpdatePreview(ctx, created.ID, links.StatusFailed, nil, nil, nil, &msg))
	got, err = lrepo.Get(ctx, uid, created.ID)
	require.NoError(t, err)
	assert.Equal(t, string(links.StatusOK), got.PreviewStatus, "failed must not overwrite ok")

	// refresh → pending is unconditional, then ok CAS succeeds again.
	require.NoError(t, lrepo.SystemUpdatePreview(ctx, created.ID, links.StatusPending, nil, nil, nil, nil))
	require.NoError(t, lrepo.SystemUpdatePreview(ctx, created.ID, links.StatusOK, nil, nil, nil, nil))
	got, err = lrepo.Get(ctx, uid, created.ID)
	require.NoError(t, err)
	assert.Equal(t, string(links.StatusOK), got.PreviewStatus)
}

func TestSystemUpdateOGImage_ManualUploadWinsCAS(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	created, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://image-cas.example", Title: "cas"})
	require.NoError(t, err)
	work, err := lrepo.SystemGetPreview(ctx, created.ID)
	require.NoError(t, err)
	previous, err := lrepo.ReplaceOGImage(ctx, uid, created.ID, "/api/files/images/manual.jpg")
	require.NoError(t, err)
	assert.Nil(t, previous)

	applied, err := lrepo.SystemUpdateOGImage(ctx, created.ID, "/api/files/screenshots/fallback.jpg", work.UpdatedAt, work.Generation)
	require.NoError(t, err)
	assert.False(t, applied)
	got, err := lrepo.Get(ctx, uid, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got.OGImageURL)
	assert.Equal(t, "/api/files/images/manual.jpg", *got.OGImageURL)
}

func TestReplaceOGImage_ReturnsExactSupersededURL(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	created, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://manual-replace.example", Title: "replace"})
	require.NoError(t, err)
	first := "/api/files/images/first.jpg"
	previous, err := lrepo.ReplaceOGImage(ctx, uid, created.ID, first)
	require.NoError(t, err)
	assert.Nil(t, previous)

	previous, err = lrepo.ReplaceOGImage(ctx, uid, created.ID, "/api/files/images/second.jpg")
	require.NoError(t, err)
	require.NotNil(t, previous)
	assert.Equal(t, first, *previous)
}

func TestReplaceOGImage_ConcurrentManualUploadsReturnTheirPredecessor(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	created, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://manual-concurrent.example", Title: "concurrent"})
	require.NoError(t, err)
	initial := "/api/files/images/initial.jpg"
	_, err = lrepo.ReplaceOGImage(ctx, uid, created.ID, initial)
	require.NoError(t, err)

	urls := []string{"/api/files/images/a.jpg", "/api/files/images/b.jpg"}
	previous := make(chan string, len(urls))
	errs := make(chan error, len(urls))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, imageURL := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			old, replaceErr := lrepo.ReplaceOGImage(ctx, uid, created.ID, imageURL)
			if replaceErr != nil {
				errs <- replaceErr
				return
			}
			previous <- *old
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for replaceErr := range errs {
		require.NoError(t, replaceErr)
	}
	close(previous)

	var predecessors []string
	for value := range previous {
		predecessors = append(predecessors, value)
	}
	require.Len(t, predecessors, 2)
	assert.Contains(t, predecessors, initial)
	got, err := lrepo.Get(ctx, uid, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got.OGImageURL)
	assert.Contains(t, urls, *got.OGImageURL)
	loser := urls[0]
	if *got.OGImageURL == loser {
		loser = urls[1]
	}
	assert.Contains(t, predecessors, loser, "the winner must receive the losing operation's URL for cleanup")
}

func TestSystemFinishScreenshotFallback_DoesNotFinishNewerRefresh(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	created, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://finish-cas.example", Title: "cas"})
	require.NoError(t, err)
	work, err := lrepo.SystemGetPreview(ctx, created.ID)
	require.NoError(t, err)
	time.Sleep(time.Millisecond)
	require.NoError(t, lrepo.SystemUpdatePreview(ctx, created.ID, links.StatusPending, nil, nil, nil, nil))

	applied, err := lrepo.SystemFinishScreenshotFallback(ctx, created.ID, work.UpdatedAt, work.Generation)
	require.NoError(t, err)
	assert.False(t, applied)
	got, err := lrepo.Get(ctx, uid, created.ID)
	require.NoError(t, err)
	assert.Equal(t, string(links.StatusPending), got.PreviewStatus)
}

func TestSystemUpdatePreviewIfUnchanged_DoesNotOverwriteNewerRefresh(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	created, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://preview-cas.example", Title: "cas"})
	require.NoError(t, err)
	work, err := lrepo.SystemGetPreview(ctx, created.ID)
	require.NoError(t, err)
	require.NoError(t, lrepo.SystemUpdatePreview(ctx, created.ID, links.StatusPending, nil, nil, nil, nil))

	staleDescription := "stale"
	applied, err := lrepo.SystemUpdatePreviewIfUnchanged(ctx, created.ID, work.UpdatedAt, work.Generation, links.StatusOK, nil, nil, &staleDescription, nil)
	require.NoError(t, err)
	assert.False(t, applied)
	got, err := lrepo.Get(ctx, uid, created.ID)
	require.NoError(t, err)
	assert.Equal(t, string(links.StatusPending), got.PreviewStatus)
	assert.Nil(t, got.Description)
}

func TestSystemUpdatePreview_GenerationRejectsStaleClaimWithEqualTimestamp(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "preview-generation@test.local", "admin")
	lrepo := links.NewRepository(pool)
	created, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://preview-generation.example", Title: "generation"})
	require.NoError(t, err)
	stale, err := lrepo.SystemGetPreview(ctx, created.ID)
	require.NoError(t, err)

	require.NoError(t, lrepo.SystemUpdatePreview(ctx, created.ID, links.StatusPending, nil, nil, nil, nil))
	_, err = pool.Exec(ctx, `UPDATE link SET updated_at = $1 WHERE id = $2`, stale.UpdatedAt, created.ID)
	require.NoError(t, err)

	staleDescription := "stale"
	applied, err := lrepo.SystemUpdatePreviewIfUnchanged(ctx, created.ID, stale.UpdatedAt, stale.Generation, links.StatusOK, nil, nil, &staleDescription, nil)
	require.NoError(t, err)
	assert.False(t, applied)

	current, err := lrepo.SystemGetPreview(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, stale.Generation+1, current.Generation)
	assert.Equal(t, links.StatusPending, current.PreviewStatus)
}

func TestSystemPendingPreviews_ReturnsSlimPendingProjectionWithinLimit(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	first, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://pending-one.example", Title: "one"})
	require.NoError(t, err)
	second, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://pending-two.example", Title: "two"})
	require.NoError(t, err)
	done, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://done.example", Title: "done"})
	require.NoError(t, err)
	require.NoError(t, lrepo.SystemUpdatePreview(ctx, done.ID, links.StatusOK, nil, nil, nil, nil))

	previews, err := lrepo.SystemPendingPreviews(ctx, 1)
	require.NoError(t, err)
	require.Len(t, previews, 1)
	assert.Equal(t, first.ID, previews[0].ID)
	assert.Equal(t, first.URL, previews[0].URL)
	assert.Equal(t, links.StatusPending, previews[0].PreviewStatus)
	assert.NotEqual(t, second.ID, previews[0].ID)
	assert.NotEqual(t, done.ID, previews[0].ID)
}

func TestRepository_DeleteCascadesLinkTag(t *testing.T) {
	ctx, uid, lrepo, trepo := setup(t)
	tag, _ := trepo.Create(ctx, uid, tags.CreateInput{Name: "t", Color: "#fff"})
	link, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x", Title: "x", TagIDs: []int64{tag.ID}})

	require.NoError(t, lrepo.Delete(ctx, uid, link.ID))
	_, err := lrepo.Get(ctx, uid, link.ID)
	assert.ErrorIs(t, err, domainerr.ErrNotFound)
}

// TestRepository_CreateDuplicateURLReturns409 locks the Go #3 fix. Previously
// the link_url_unique violation surfaced as a raw pgx error. The browser
// extension and bulk import flows rely on the URL-taken semantic so the handler
// can emit 409 url_taken and converge to a no-op.
func TestRepository_CreateDuplicateURLReturns409(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	_, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://dup.example", Title: "first"})
	require.NoError(t, err)

	_, err = lrepo.Create(ctx, uid, links.CreateInput{URL: "https://dup.example", Title: "second"})
	require.ErrorIs(t, err, links.ErrURLTaken)
}

// TestHandler_CreateRejectsLargeBody locks the P2.5 fix: POST /api/links with
// a body over 64 KiB is refused with invalid_json (the MaxBytesReader trip
// surfaces as a parse failure to json.Decoder — sufficient for clients).
func TestHandler_CreateRejectsLargeBody(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	_ = ctx
	h := links.NewHandler(lrepo, nopEnqueuer{})
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	r.Route("/api/links", h.Mount)

	// 64 KiB + 1 of valid-looking JSON ("description":"AAAA..."). The decoder
	// would happily accept it without the cap; MaxBytesReader trips first.
	big := make([]byte, 0, (64<<10)+1)
	big = append(big, `{"url":"https://x","title":"t","description":"`...)
	pad := make([]byte, (64<<10)+1)
	for i := range pad {
		pad[i] = 'A'
	}
	big = append(big, pad...)
	big = append(big, `"}`...)

	req := httptest.NewRequest(http.MethodPost, "/api/links/", bytes.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "body over jsonBodyCap must be refused")
	// Assert the response code (not just status) — a future refactor that
	// returned 400 invalid_input from a different shape check could pass the
	// status assertion while quietly disabling the body cap.
	assert.Contains(t, w.Body.String(), `"invalid_json"`, "must surface as invalid_json (MaxBytesReader trip)")
}

// TestHandler_CreateStillReturns201WhenEnqueueFails locks the Phase 2 contract:
// link creation succeeds even if the preview worker queue is saturated. A
// failed enqueue is operational (next requeuePending picks it up), not a
// client-facing error.
func TestHandler_CreateStillReturns201WhenEnqueueFails(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	_ = ctx
	h := links.NewHandler(lrepo, fullEnqueuer{})
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	r.Route("/api/links", h.Mount)

	body := `{"url":"https://example.com","title":"t"}`
	req := httptest.NewRequest(http.MethodPost, "/api/links/", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code, "ErrQueueFull from Enqueue must not surface as a client error")
}

type fullEnqueuer struct{}

func (fullEnqueuer) Enqueue(int64) error {
	// Mirror the worker's contract — ErrQueueFull is the runtime signal we
	// want the handler to swallow without affecting the response.
	return errors.New("queue full")
}

type nopEnqueuer struct{}

func (nopEnqueuer) Enqueue(int64) error { return nil }

// TestRepository_PinnedAlwaysComesFirst locks the §5 invariant: pinned links
// outrank everything else, including the selected sort. Without this test,
// dropping `l.pinned DESC` from the ORDER BY in any sort branch ships green.
func TestRepository_PinnedAlwaysComesFirst(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)

	// Newer link first (default "created" sort) but NOT pinned.
	newer, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://newer", Title: "Newer"})
	require.NoError(t, err)
	// Older link, pinned — should appear FIRST despite being older.
	older, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://older", Title: "Older"})
	require.NoError(t, err)
	pinTrue := true
	_, err = lrepo.Update(ctx, uid, older.ID, links.UpdateInput{Pinned: &pinTrue})
	require.NoError(t, err)

	for _, sort := range []string{"", "recent", "clicks", "alpha", "alpha_desc"} {
		t.Run("sort="+sort, func(t *testing.T) {
			out, err := lrepo.List(ctx, uid, links.ListQuery{Sort: sort})
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(out), 2)
			assert.True(t, out[0].Pinned, "pinned link must always come first under sort=%q", sort)
			assert.Equal(t, older.ID, out[0].ID)
		})
	}
	_ = newer
}

// TestRepository_UpdateDuplicateURLReturns409 mirrors the above for the Update
// path — folding a link onto another's URL via PATCH should also surface as
// 409, not 500.
func TestRepository_UpdateDuplicateURLReturns409(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	a, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://a.example", Title: "A"})
	require.NoError(t, err)
	_, err = lrepo.Create(ctx, uid, links.CreateInput{URL: "https://b.example", Title: "B"})
	require.NoError(t, err)

	bURL := "https://b.example"
	_, err = lrepo.Update(ctx, uid, a.ID, links.UpdateInput{URL: &bURL})
	require.ErrorIs(t, err, links.ErrURLTaken)
}

// TestRepository_UngroupedExcludesLinksInFolders locks CLAUDE.md §4: the home
// query (`?ungrouped=1`) must only surface links with `folder_id IS NULL`.
// Without this, a link in a folder would appear both in the folder card AND
// on the home grid, double-rendered.
func TestRepository_UngroupedExcludesLinksInFolders(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	frepo := folders.NewRepository(pool)
	lrepo := links.NewRepository(pool)

	f, err := frepo.Create(ctx, uid, folders.CreateInput{Name: "Inbox", Color: "#abc"})
	require.NoError(t, err)
	_, err = lrepo.Create(ctx, uid, links.CreateInput{URL: "https://in", Title: "InFolder", FolderID: &f.ID})
	require.NoError(t, err)
	ungrouped, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://out", Title: "Ungrouped"})
	require.NoError(t, err)

	got, err := lrepo.List(ctx, uid, links.ListQuery{Ungrouped: true})
	require.NoError(t, err)
	require.Len(t, got, 1, "?ungrouped=1 must surface only links with folder_id IS NULL")
	assert.Equal(t, ungrouped.ID, got[0].ID)

	// Unscoped list returns both.
	all, err := lrepo.List(ctx, uid, links.ListQuery{})
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

// TestRepository_ListByFolderANDTag locks the §4 composition: a folder scope
// and a tag filter must compose with AND, not OR. Inside folder F, toggling
// tag X narrows the result to links in F that also have X.
func TestRepository_ListByFolderANDTag(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	frepo := folders.NewRepository(pool)
	trepo := tags.NewRepository(pool)
	lrepo := links.NewRepository(pool)

	folder, err := frepo.Create(ctx, uid, folders.CreateInput{Name: "Work", Color: "#abc"})
	require.NoError(t, err)
	tagX, err := trepo.Create(ctx, uid, tags.CreateInput{Name: "x", Color: "#fff"})
	require.NoError(t, err)

	withTag, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://withtag", Title: "WithTag", FolderID: &folder.ID, TagIDs: []int64{tagX.ID},
	})
	require.NoError(t, err)
	noTag, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://notag", Title: "NoTag", FolderID: &folder.ID,
	})
	require.NoError(t, err)

	folderOnly, err := lrepo.List(ctx, uid, links.ListQuery{FolderID: &folder.ID})
	require.NoError(t, err)
	require.Len(t, folderOnly, 2, "folder scope alone returns both")

	combined, err := lrepo.List(ctx, uid, links.ListQuery{FolderID: &folder.ID, TagIDs: []int64{tagX.ID}})
	require.NoError(t, err)
	require.Len(t, combined, 1, "folder + tag must AND, not OR")
	assert.Equal(t, withTag.ID, combined[0].ID)
	_ = noTag
}

// TestRepository_GoEndpointIsOnlyClickInserter locks CLAUDE.md §4: `click_log`
// is the single source of truth for clicks AND `/go/:id` is the only path
// that inserts into it. Reading a link via Get/List/GetBySlug must NOT bump
// the count.
func TestRepository_GoEndpointIsOnlyClickInserter(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	link, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://only-go", Title: "OnlyGo"})
	require.NoError(t, err)

	// Exercise every read path that is NOT /go/:id.
	_, _ = lrepo.Get(ctx, uid, link.ID)
	_, _ = lrepo.GetBySlug(ctx, uid, link.Slug)
	_, _ = lrepo.List(ctx, uid, links.ListQuery{})

	got, err := lrepo.Get(ctx, uid, link.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, got.ClickCount, "no read path may write click_log")

	// Now exercise the /go/:id atomic path and confirm count moves to 1.
	_, err = lrepo.ClickAndResolve(ctx, link.ID)
	require.NoError(t, err)
	got, err = lrepo.Get(ctx, uid, link.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.ClickCount)
}

// TestSchema_NoCachedClickColumns locks migration 000006: link.click_count and
// link.last_clicked_at were dropped — derived from click_log via LATERAL on
// every SELECT. A future migration that adds them back would silently keep
// the LATERAL but also re-introduce the denormalization drift; this test
// surfaces it at boot.
func TestSchema_NoCachedClickColumns(t *testing.T) {
	pool := testdb.Shared(t)

	_ = testdb.SeedUser(t, pool, "owner@test.local", "admin")
	ctx := context.Background()
	rows, err := pool.Query(ctx, `
        SELECT column_name FROM information_schema.columns
        WHERE table_name = 'link' AND column_name IN ('click_count','last_clicked_at')
    `)
	require.NoError(t, err)
	defer rows.Close()
	var found []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		found = append(found, n)
	}
	require.NoError(t, rows.Err())
	assert.Empty(t, found, "click_count/last_clicked_at must NOT exist on link — they are derived from click_log")
}

func TestRepository_SortByClicks(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	a, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://a", Title: "A"})
	b, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://b", Title: "B"})

	// Bump b twice, a once.
	_, _ = lrepo.ClickAndResolve(ctx, b.ID)
	_, _ = lrepo.ClickAndResolve(ctx, b.ID)
	_, _ = lrepo.ClickAndResolve(ctx, a.ID)

	out, err := lrepo.List(ctx, uid, links.ListQuery{Sort: "clicks"})
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "B", out[0].Title, "highest click_count first")
}

// ---- Change-detection methods (migration 000010) ----------------------------

func TestRepository_CheckInterval_TriStateOnUpdate(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	l, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/a", Title: "x"})
	require.NoError(t, err)
	assert.Nil(t, l.CheckInterval, "default opt-out")

	// Opt in to daily.
	interval := "daily"
	updated, err := lrepo.Update(ctx, uid, l.ID, links.UpdateInput{
		CheckInterval: &interval, CheckIntervalSet: true,
	})
	require.NoError(t, err)
	require.NotNil(t, updated.CheckInterval)
	assert.Equal(t, "daily", *updated.CheckInterval)
	claim := claimChecks(t, ctx, lrepo, l.ID)[l.ID]

	// Simulate worker stamping a fingerprint + detection.
	recordCheckResult(t, ctx, lrepo, l.ID, claim.ClaimedAt, links.CheckResult{Fingerprint: "content:abc"})
	recordCheckResult(t, ctx, lrepo, l.ID, claim.ClaimedAt, links.CheckResult{Fingerprint: "content:def", Changed: true})
	recordCheckResult(t, ctx, lrepo, l.ID, claim.ClaimedAt, links.CheckResult{FetchErr: "timeout"})

	withState, err := lrepo.Get(ctx, uid, l.ID)
	require.NoError(t, err)
	require.NotNil(t, withState.LastFingerprint)
	require.NotNil(t, withState.LastChangeDetectedAt)

	// Opt out → ALL change-check state must clear (CLAUDE.md §4 invariant).
	cleared, err := lrepo.Update(ctx, uid, l.ID, links.UpdateInput{
		CheckInterval: nil, CheckIntervalSet: true,
	})
	require.NoError(t, err)
	assert.Nil(t, cleared.CheckInterval)
	assert.Nil(t, cleared.LastCheckedAt, "opt-out must wipe last_checked_at")
	assert.Nil(t, cleared.LastFingerprint, "opt-out must wipe last_fingerprint")
	assert.Nil(t, cleared.LastChangeDetectedAt, "opt-out must wipe last_change_detected_at")
	assert.Nil(t, cleared.ChangeSeenAt, "opt-out must wipe change_seen_at")
	assert.Nil(t, cleared.LastCheckError, "opt-out must wipe last_check_error")
}

// dueIDs projects the DueLink rows to bare ids.
//
// SystemFindDueForCheck returns []links.DueLink{ID, UserID} since ADR-30 — the
// worker needs the owner to attribute the push. Asserting Contains/NotContains
// with a bare int64 against that slice is VACUOUS: the element types never
// match, so NotContains passes unconditionally and Contains can only ever fail.
// Project first, then assert.
func dueIDs(due []links.DueLink) []int64 {
	out := make([]int64, 0, len(due))
	for _, d := range due {
		out = append(out, d.ID)
	}
	return out
}

func TestRepository_FindDueForCheck_OnlyOptedIn(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	// Opted-in (daily): due immediately because last_checked_at IS NULL.
	a, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/a", Title: "a"})
	di := "daily"
	_, err := lrepo.Update(ctx, uid, a.ID, links.UpdateInput{CheckInterval: &di, CheckIntervalSet: true})
	require.NoError(t, err)

	// Opted-OUT link must NOT appear.
	_, _ = lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/b", Title: "b"})

	due, err := lrepo.SystemFindDueForCheck(ctx, 100)
	require.NoError(t, err)
	assert.Contains(t, dueIDs(due), a.ID, "opted-in link with NULL last_checked_at must be due")
	assert.Len(t, due, 1, "only opted-in links may be due")
	// The sweep is deliberately unscoped (it serves every tenant), so the owner
	// has to travel WITH the row or the push would go to whoever ran the worker.
	assert.Equal(t, uid, due[0].UserID, "the due row must carry its owner")
	assert.Equal(t, a.URL, due[0].URL)
	assert.Equal(t, a.Title, due[0].Title)
	assert.Equal(t, di, due[0].CheckInterval)
	assert.NotZero(t, due[0].ClaimedAt)
}

func TestRepository_ChangeCheckExcludesLockedLinksAndRejectsFolderMoveRace(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "locked-check@test.local", "editor")
	repo := links.NewRepository(pool)
	password := "folder-password"
	protected, err := folders.NewRepository(pool).Create(ctx, uid, folders.CreateInput{
		Name: "Protected", Color: "#abc", Password: &password,
	})
	require.NoError(t, err)
	daily := "daily"
	locked, err := repo.Create(ctx, uid, links.CreateInput{
		URL: "https://locked-check.test", Title: "locked", FolderID: &protected.ID, CheckInterval: &daily,
	})
	require.NoError(t, err)
	open, err := repo.Create(ctx, uid, links.CreateInput{
		URL: "https://open-check.test", Title: "open", CheckInterval: &daily,
	})
	require.NoError(t, err)

	due, err := repo.SystemFindDueForCheck(ctx, 100)
	require.NoError(t, err)
	assert.NotContains(t, dueIDs(due), locked.ID)
	require.Contains(t, dueIDs(due), open.ID)
	var openClaim links.DueLink
	for _, candidate := range due {
		if candidate.ID == open.ID {
			openClaim = candidate
			break
		}
	}
	require.NotZero(t, openClaim.ClaimedAt)
	_, err = repo.Update(ctx, uid, open.ID, links.UpdateInput{FolderID: &protected.ID, FolderIDSet: true})
	require.NoError(t, err)

	applied, err := repo.SystemRecordCheckResult(ctx, open.ID, openClaim.ClaimedAt, links.CheckResult{
		Fingerprint: "content:changed", Changed: true,
	})
	require.NoError(t, err)
	assert.False(t, applied, "moving a claimed link into a locked folder must suppress publication and push")
}

func TestRepository_RecordCheckResultRejectsStaleConfiguration(t *testing.T) {
	ctx, uid, repo, _ := setup(t)
	link, err := repo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/old", Title: "x"})
	require.NoError(t, err)
	daily := "daily"
	_, err = repo.Update(ctx, uid, link.ID, links.UpdateInput{CheckInterval: &daily, CheckIntervalSet: true})
	require.NoError(t, err)
	due, err := repo.SystemFindDueForCheck(ctx, 1)
	require.NoError(t, err)
	require.Len(t, due, 1)

	recordCheckResult(t, ctx, repo, link.ID, due[0].ClaimedAt, links.CheckResult{Fingerprint: "content:old"})
	recordCheckResult(t, ctx, repo, link.ID, due[0].ClaimedAt, links.CheckResult{Fingerprint: "content:changed", Changed: true})
	recordCheckResult(t, ctx, repo, link.ID, due[0].ClaimedAt, links.CheckResult{FetchErr: "timeout"})
	require.NoError(t, repo.MarkChangeSeen(ctx, uid, link.ID))

	newURL := "https://x.test/new"
	reconfigured, err := repo.Update(ctx, uid, link.ID, links.UpdateInput{URL: &newURL})
	require.NoError(t, err)
	assert.Nil(t, reconfigured.LastCheckedAt)
	assert.Nil(t, reconfigured.LastFingerprint)
	assert.Nil(t, reconfigured.LastChangeDetectedAt)
	assert.Nil(t, reconfigured.ChangeSeenAt)
	assert.Nil(t, reconfigured.LastCheckError)

	applied, err := repo.SystemRecordCheckResult(ctx, link.ID, due[0].ClaimedAt, links.CheckResult{
		Fingerprint: "content:stale",
		Changed:     true,
	})
	require.NoError(t, err)
	assert.False(t, applied)

	got, err := repo.Get(ctx, uid, link.ID)
	require.NoError(t, err)
	assert.Nil(t, got.LastFingerprint)
	assert.Nil(t, got.LastChangeDetectedAt)
}

func TestRepository_UnchangedMonitoringFieldsPreserveState(t *testing.T) {
	ctx, uid, repo, _ := setup(t)
	link, err := repo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/same", Title: "old title"})
	require.NoError(t, err)
	daily := "daily"
	_, err = repo.Update(ctx, uid, link.ID, links.UpdateInput{CheckInterval: &daily, CheckIntervalSet: true})
	require.NoError(t, err)
	claim := claimChecks(t, ctx, repo, link.ID)[link.ID]
	recordCheckResult(t, ctx, repo, link.ID, claim.ClaimedAt, links.CheckResult{Fingerprint: "content:baseline"})
	recordCheckResult(t, ctx, repo, link.ID, claim.ClaimedAt, links.CheckResult{Fingerprint: "content:changed", Changed: true})
	recordCheckResult(t, ctx, repo, link.ID, claim.ClaimedAt, links.CheckResult{FetchErr: "timeout"})
	require.NoError(t, repo.MarkChangeSeen(ctx, uid, link.ID))

	title := "new title"
	unchangedURL := link.URL
	updated, err := repo.Update(ctx, uid, link.ID, links.UpdateInput{
		URL:              &unchangedURL,
		Title:            &title,
		CheckInterval:    &daily,
		CheckIntervalSet: true,
	})
	require.NoError(t, err)
	assert.Equal(t, claim.ClaimedAt, *updated.LastCheckedAt)
	require.NotNil(t, updated.LastFingerprint)
	assert.Equal(t, "content:changed", *updated.LastFingerprint)
	assert.NotNil(t, updated.LastChangeDetectedAt)
	assert.NotNil(t, updated.ChangeSeenAt)
	require.NotNil(t, updated.LastCheckError)
	assert.Equal(t, "timeout", *updated.LastCheckError)
}

func TestRepository_FindDueForCheck_RespectsInterval(t *testing.T) {
	// Need direct pool access to backdate last_checked_at — testdb.New spins
	// a fresh container per call, so we share one pool between repo + raw SQL.
	ctx := context.Background()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	lrepo := links.NewRepository(pool)

	// Hourly link checked 30 minutes ago → NOT due.
	l, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/h", Title: "h"})
	hi := "hourly"
	_, err := lrepo.Update(ctx, uid, l.ID, links.UpdateInput{CheckInterval: &hi, CheckIntervalSet: true})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE link SET last_checked_at = now() - interval '30 minutes' WHERE id = $1`, l.ID)
	require.NoError(t, err)

	due, err := lrepo.SystemFindDueForCheck(ctx, 100)
	require.NoError(t, err)
	assert.NotContains(t, dueIDs(due), l.ID, "30 minutes < 1 hour: must NOT be due")

	// Backdate further → past 1h → now due.
	_, err = pool.Exec(ctx, `UPDATE link SET last_checked_at = now() - interval '2 hours' WHERE id = $1`, l.ID)
	require.NoError(t, err)
	due, err = lrepo.SystemFindDueForCheck(ctx, 100)
	require.NoError(t, err)
	assert.Contains(t, dueIDs(due), l.ID, "2h > 1h: hourly link is due")
	assert.Equal(t, uid, due[0].UserID, "the due row must carry its owner")
}

func TestRepository_RecordCheckResult_FirstObservationDoesNotMarkChange(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	l, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/f", Title: "f"})
	di := "daily"
	_, err := lrepo.Update(ctx, uid, l.ID, links.UpdateInput{CheckInterval: &di, CheckIntervalSet: true})
	require.NoError(t, err)
	claim := claimChecks(t, ctx, lrepo, l.ID)[l.ID]

	// Worker passes Changed=false on the first observation (no previous fp).
	recordCheckResult(t, ctx, lrepo, l.ID, claim.ClaimedAt, links.CheckResult{
		Fingerprint: "content:abc",
		Changed:     false,
	})

	got, err := lrepo.Get(ctx, uid, l.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastFingerprint)
	assert.Equal(t, "content:abc", *got.LastFingerprint)
	assert.NotNil(t, got.LastCheckedAt, "last_checked_at must always bump")
	assert.Nil(t, got.LastChangeDetectedAt, "first observation must NOT bump last_change_detected_at")
}

func TestRepository_RecordCheckResult_BumpsDetectionAndNullsSeen(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	l, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/c", Title: "c"})
	di := "daily"
	_, err := lrepo.Update(ctx, uid, l.ID, links.UpdateInput{CheckInterval: &di, CheckIntervalSet: true})
	require.NoError(t, err)
	claim := claimChecks(t, ctx, lrepo, l.ID)[l.ID]

	// Seed a first observation and pretend the user already saw an OLD change.
	recordCheckResult(t, ctx, lrepo, l.ID, claim.ClaimedAt, links.CheckResult{
		Fingerprint: "content:abc",
	})
	recordCheckResult(t, ctx, lrepo, l.ID, claim.ClaimedAt, links.CheckResult{
		Fingerprint: "content:def",
		Changed:     true,
	})
	require.NoError(t, lrepo.MarkChangeSeen(ctx, uid, l.ID))

	got, err := lrepo.Get(ctx, uid, l.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ChangeSeenAt, "MarkChangeSeen must stamp change_seen_at")

	// NEW change → must null out change_seen_at again so the badge re-shows.
	recordCheckResult(t, ctx, lrepo, l.ID, claim.ClaimedAt, links.CheckResult{
		Fingerprint: "content:ghi",
		Changed:     true,
	})
	got2, err := lrepo.Get(ctx, uid, l.ID)
	require.NoError(t, err)
	assert.Nil(t, got2.ChangeSeenAt, "new detection must reset change_seen_at to NULL")
	require.NotNil(t, got2.LastChangeDetectedAt)
	if got.LastChangeDetectedAt != nil {
		assert.True(t, got2.LastChangeDetectedAt.After(*got.LastChangeDetectedAt),
			"new detection must move last_change_detected_at forward")
	}
}

func TestRepository_RecordCheckResult_StoresErrorInLastCheckErrorNotPreviewError(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	l, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/e", Title: "e"})
	di := "daily"
	_, err := lrepo.Update(ctx, uid, l.ID, links.UpdateInput{CheckInterval: &di, CheckIntervalSet: true})
	require.NoError(t, err)
	claim := claimChecks(t, ctx, lrepo, l.ID)[l.ID]

	beforePreviewErr := ""
	{
		got, _ := lrepo.Get(ctx, uid, l.ID)
		if got.PreviewError != nil {
			beforePreviewErr = *got.PreviewError
		}
	}

	recordCheckResult(t, ctx, lrepo, l.ID, claim.ClaimedAt, links.CheckResult{
		FetchErr: "timeout",
	})

	got, err := lrepo.Get(ctx, uid, l.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastCheckError, "FetchErr must land in last_check_error")
	assert.Equal(t, "timeout", *got.LastCheckError)

	// preview_error MUST NOT change (CLAUDE.md §4: preview worker is the
	// only writer of preview_error).
	afterPreviewErr := ""
	if got.PreviewError != nil {
		afterPreviewErr = *got.PreviewError
	}
	assert.Equal(t, beforePreviewErr, afterPreviewErr, "RecordCheckResult must NOT touch preview_error")
}

func TestRepository_MarkChangeSeen_404WhenNeverDetected(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	l, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/n", Title: "n"})

	err := lrepo.MarkChangeSeen(ctx, uid, l.ID)
	require.Error(t, err, "MarkChangeSeen must 404 when no change has been detected")
	assert.ErrorIs(t, err, domainerr.ErrNotFound)
}

func TestRepository_ListRecentChanges_FiltersAndSorts(t *testing.T) {
	ctx, uid, lrepo, _ := setup(t)
	older, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/old", Title: "older"})
	newer, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/new", Title: "newer"})
	skipped, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/skip", Title: "no change"})

	di := "daily"
	_, err := lrepo.Update(ctx, uid, older.ID, links.UpdateInput{CheckInterval: &di, CheckIntervalSet: true})
	require.NoError(t, err)
	_, err = lrepo.Update(ctx, uid, newer.ID, links.UpdateInput{CheckInterval: &di, CheckIntervalSet: true})
	require.NoError(t, err)
	claims := claimChecks(t, ctx, lrepo, older.ID, newer.ID)

	// Seed older detection, then newer.
	recordCheckResult(t, ctx, lrepo, older.ID, claims[older.ID].ClaimedAt, links.CheckResult{Fingerprint: "content:1"})
	recordCheckResult(t, ctx, lrepo, older.ID, claims[older.ID].ClaimedAt, links.CheckResult{Fingerprint: "content:2", Changed: true})
	recordCheckResult(t, ctx, lrepo, newer.ID, claims[newer.ID].ClaimedAt, links.CheckResult{Fingerprint: "content:1"})
	recordCheckResult(t, ctx, lrepo, newer.ID, claims[newer.ID].ClaimedAt, links.CheckResult{Fingerprint: "content:9", Changed: true})

	out, err := lrepo.ListRecentChanges(ctx, uid, 7*24*60*60, 50)
	require.NoError(t, err)
	require.Len(t, out, 2, "only links with last_change_detected_at != NULL are returned")
	assert.Equal(t, "newer", out[0].Title, "DESC by last_change_detected_at: newer first")
	assert.NotContains(t, []int64{out[0].ID, out[1].ID}, skipped.ID,
		"links without a detected change must not appear")
}

func TestRepository_ListRecentChanges_WindowFiltersOut(t *testing.T) {
	// Same pool-sharing rationale as FindDueForCheck_RespectsInterval.
	ctx := context.Background()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	lrepo := links.NewRepository(pool)

	l, _ := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/w", Title: "w"})
	di := "daily"
	_, err := lrepo.Update(ctx, uid, l.ID, links.UpdateInput{CheckInterval: &di, CheckIntervalSet: true})
	require.NoError(t, err)
	claim := claimChecks(t, ctx, lrepo, l.ID)[l.ID]
	recordCheckResult(t, ctx, lrepo, l.ID, claim.ClaimedAt, links.CheckResult{Fingerprint: "content:1"})
	recordCheckResult(t, ctx, lrepo, l.ID, claim.ClaimedAt, links.CheckResult{Fingerprint: "content:2", Changed: true})

	_, err = pool.Exec(ctx, `UPDATE link SET last_change_detected_at = now() - interval '8 days' WHERE id = $1`, l.ID)
	require.NoError(t, err)

	out, err := lrepo.ListRecentChanges(ctx, uid, 7*24*60*60, 50)
	require.NoError(t, err)
	assert.Empty(t, out, "8-day-old change must fall outside the 7-day window")
}

// AssertOwned is the ownership gate ProxyFile uses instead of Get, so it needs
// its own test: the handler suite exercises the branch through a fake
// repository, which proves the handler reacts to the answer but nothing about
// how the answer is reached.
//
// The foreign case is the one that matters. Object keys are FLAT
// (`images/{link_id}.jpg` — no tenant segment), so the id embedded in the key
// is attacker-supplied and this query is the only thing standing between a
// guessed id and another tenant's image. It must answer the byte-identical 404
// an absent id gets: a distinguishable error would turn the proxy into an
// enumeration oracle over a dense BIGSERIAL.
func TestRepository_AssertOwned(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	alice := testdb.SeedUser(t, pool, "alice@test.local", "editor")
	bob := testdb.SeedUser(t, pool, "bob@test.local", "editor")
	repo := links.NewRepository(pool)

	mine, err := repo.Create(ctx, alice, links.CreateInput{URL: "https://a.test/own", Title: "own"})
	require.NoError(t, err)

	require.NoError(t, repo.AssertOwned(ctx, alice, mine.ID), "the owner must pass")

	foreign := repo.AssertOwned(ctx, bob, mine.ID)
	absent := repo.AssertOwned(ctx, bob, mine.ID+10_000)
	require.ErrorIs(t, foreign, domainerr.ErrNotFound)
	require.ErrorIs(t, absent, domainerr.ErrNotFound)
	assert.Equal(t, absent, foreign,
		"a foreign id and an absent id must be indistinguishable, or the file proxy leaks which ids exist")
}

// The sweep spans every tenant by design; it must not span every account
// STATE. Disabling an account revokes its sessions and kills its API tokens,
// but nothing in that path touches the change-check opt-in — so a disabled
// owner's links kept being fetched on schedule and kept producing Web Push
// notifications to a browser that could no longer sign in.
//
// Filtered at the CLAIM rather than only at delivery because this is where the
// cost lives: a disabled account's links stop consuming fetch budget too, not
// merely stop notifying. Delivery re-checks separately (push.Repository.List),
// for the account disabled between the claim and the send.
func TestRepository_FindDueForCheck_SkipsDisabledOwners(t *testing.T) {
	// Deliberately NOT setup(t): this test needs the pool for raw SQL, and
	// testdb.Shared RESETS the database on every call — a second one here
	// would silently wipe whatever setup(t) had just seeded.
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	other := testdb.SeedUser(t, pool, "still-active@test.local", "editor")
	lrepo := links.NewRepository(pool)
	di := "daily"

	mine, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/mine", Title: "mine"})
	require.NoError(t, err)
	_, err = lrepo.Update(ctx, uid, mine.ID, links.UpdateInput{CheckInterval: &di, CheckIntervalSet: true})
	require.NoError(t, err)
	theirs, err := lrepo.Create(ctx, other, links.CreateInput{URL: "https://x.test/theirs", Title: "theirs"})
	require.NoError(t, err)
	_, err = lrepo.Update(ctx, other, theirs.ID, links.UpdateInput{CheckInterval: &di, CheckIntervalSet: true})
	require.NoError(t, err)

	due, err := lrepo.SystemFindDueForCheck(ctx, 100)
	require.NoError(t, err)
	require.Contains(t, dueIDs(due), mine.ID)
	require.Contains(t, dueIDs(due), theirs.ID)

	_, err = pool.Exec(ctx, `UPDATE app_user SET status = 'disabled' WHERE id = $1`, int64(uid))
	require.NoError(t, err)
	// Backdated rather than nulled: the interval arm is the one production
	// spends its life in, and NULL would only exercise the freshly-opted-in
	// branch. Needed at all because the first sweep bumped last_checked_at.
	_, err = pool.Exec(ctx,
		`UPDATE link SET last_checked_at = now() - interval '2 days' WHERE id = ANY($1::bigint[])`,
		[]int64{mine.ID, theirs.ID})
	require.NoError(t, err)

	after, err := lrepo.SystemFindDueForCheck(ctx, 100)
	require.NoError(t, err)
	assert.NotContains(t, dueIDs(after), mine.ID, "a disabled owner's links must not be claimed")
	assert.Contains(t, dueIDs(after), theirs.ID, "an active owner in the same sweep is unaffected")

	// Not merely absent from the RESULT — left unclaimed. Filtering after the
	// UPDATE, in Go, would satisfy every assertion above while the row kept
	// churning last_checked_at and kept eating the LIMIT-256 sweep budget every
	// tick, which is the whole reason this half filters at the claim instead of
	// only at delivery. The claim is what writes the column, so the column is
	// what proves it did not happen.
	var claimed *time.Time
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT last_checked_at FROM link WHERE id = $1`, mine.ID).Scan(&claimed))
	require.NotNil(t, claimed)
	assert.True(t, claimed.Before(time.Now().Add(-time.Hour)),
		"a skipped link must not have been claimed, so its last_checked_at stays backdated")

	// Reversible: the opt-in survives, so re-enabling resumes monitoring
	// without the user having to set the interval again.
	_, err = pool.Exec(ctx, `UPDATE app_user SET status = 'active' WHERE id = $1`, int64(uid))
	require.NoError(t, err)
	back, err := lrepo.SystemFindDueForCheck(ctx, 100)
	require.NoError(t, err)
	assert.Contains(t, dueIDs(back), mine.ID, "re-enabling the account resumes the sweep")

	// `pending` is excluded too, and the predicate says so by being `= active`
	// rather than `<> disabled`. Indistinguishable today — a pending account
	// cannot sign in, so it never accumulates links — but they part ways the
	// moment a "suspend to pending" transition exists, and the answer belongs
	// in a test rather than in whichever operator happens to read the SQL.
	_, err = pool.Exec(ctx, `UPDATE app_user SET status = 'pending' WHERE id = $1`, int64(uid))
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`UPDATE link SET last_checked_at = now() - interval '2 days' WHERE id = $1`, mine.ID)
	require.NoError(t, err)
	pending, err := lrepo.SystemFindDueForCheck(ctx, 100)
	require.NoError(t, err)
	assert.NotContains(t, dueIDs(pending), mine.ID, "a pending owner is not swept either")
}

// The JOIN put app_user in the FROM, and `FOR UPDATE` without `OF` marks every
// table it finds there. Two unintended consequences follow, and this pins the
// first: SKIP LOCKED starts skipping a due link because its OWNER row is
// locked. Every auth path holds `app_user FOR NO KEY UPDATE` for the length of
// a transaction — login, a 2FA step, minting an API token, subscribing to push
// — so an owner merely signing in would suspend their own monitoring for that
// tick. The second consequence is the mirror of it: the sweep would hold
// FOR UPDATE on app_user, which conflicts with the FOR KEY SHARE every foreign
// key to it takes, blocking INSERTs into link, note, session, audit_log,
// mail_outbox and click_log — click_log being the public /go/ redirect.
func TestRepository_FindDueForCheck_DoesNotLockTheOwnerRow(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, pool, "busy-owner@test.local", "editor")
	lrepo := links.NewRepository(pool)
	di := "daily"

	l, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://x.test/busy", Title: "busy"})
	require.NoError(t, err)
	_, err = lrepo.Update(ctx, uid, l.ID, links.UpdateInput{CheckInterval: &di, CheckIntervalSet: true})
	require.NoError(t, err)

	// Exactly what an in-flight login holds while it works.
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	var locked int64
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(uid)).Scan(&locked))

	due, err := lrepo.SystemFindDueForCheck(ctx, 100)
	require.NoError(t, err)
	assert.Contains(t, dueIDs(due), l.ID,
		"a due link must be claimed even while its owner row is locked by another transaction")
}

// The conditional predicate is what makes a screenful of broken cards produce
// one regeneration instead of thirty-three, and what stops a newer image from
// being discarded. Both halves are asserted against a real Postgres.
func TestInvalidateMissingPreview_OnlyFiresForTheURLThat404d(t *testing.T) {
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))
	uid := testdb.SeedUser(t, pool, "owner@test.local", "owner")
	repo := links.NewRepository(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, uid, links.CreateInput{
		URL: "https://example.com/a", Title: "A",
	})
	require.NoError(t, err)

	gone := "/api/files/screenshots/" + strconv.FormatInt(created.ID, 10) + ".jpg"
	_, err = pool.Exec(ctx,
		`UPDATE link SET og_image_url = $2, preview_status = 'ok' WHERE id = $1`, created.ID, gone)
	require.NoError(t, err)

	changed, err := repo.InvalidateMissingPreview(ctx, uid, created.ID, gone)
	require.NoError(t, err)
	assert.True(t, changed, "the first request re-arms the preview")

	var status string
	var url *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT preview_status, og_image_url FROM link WHERE id = $1`, created.ID).Scan(&status, &url))
	assert.Equal(t, "pending", status, "nothing failed — the bytes are gone, and pending is what the worker picks up")
	assert.Nil(t, url)

	// The second concurrent request for the same broken card. It must change
	// nothing, which is what keeps the enqueue to one.
	changed, err = repo.InvalidateMissingPreview(ctx, uid, created.ID, gone)
	require.NoError(t, err)
	assert.False(t, changed)

	// A manual upload that landed between the browser's request and this write
	// no longer matches the key that 404'd, so it survives.
	fresh := "/api/files/images/" + strconv.FormatInt(created.ID, 10) + ".abc.jpg"
	_, err = pool.Exec(ctx, `UPDATE link SET og_image_url = $2 WHERE id = $1`, created.ID, fresh)
	require.NoError(t, err)
	changed, err = repo.InvalidateMissingPreview(ctx, uid, created.ID, gone)
	require.NoError(t, err)
	assert.False(t, changed, "a newer image must not be discarded by a stale 404")

	require.NoError(t, pool.QueryRow(ctx,
		`SELECT og_image_url FROM link WHERE id = $1`, created.ID).Scan(&url))
	require.NotNil(t, url)
	assert.Equal(t, fresh, *url)
}

// §4: every repository method is owner-scoped, and this one writes.
func TestInvalidateMissingPreview_IsOwnerScoped(t *testing.T) {
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))
	owner := testdb.SeedUser(t, pool, "owner@test.local", "owner")
	other := testdb.SeedUser(t, pool, "other@test.local", "editor")
	repo := links.NewRepository(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, owner, links.CreateInput{URL: "https://example.com/a", Title: "A"})
	require.NoError(t, err)
	gone := "/api/files/screenshots/" + strconv.FormatInt(created.ID, 10) + ".jpg"
	_, err = pool.Exec(ctx, `UPDATE link SET og_image_url = $2 WHERE id = $1`, created.ID, gone)
	require.NoError(t, err)

	changed, err := repo.InvalidateMissingPreview(ctx, other, created.ID, gone)
	require.NoError(t, err)
	assert.False(t, changed, "another account must not be able to reset this link's preview")
}
