package importer

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

// fakeImportRows is a scripted pgx.Rows: each entry is one (id,url,slug)
// triple and scanErr/rowErr let a test fail the two distinct places
// loadExistingImportLinks can fail while reading.
type fakeImportRows struct {
	pgx.Rows
	rows    [][3]any
	i       int
	scanErr error
	rowErr  error
}

func (r *fakeImportRows) Close() {}
func (r *fakeImportRows) Err() error {
	return r.rowErr
}
func (r *fakeImportRows) Next() bool {
	if r.i >= len(r.rows) {
		return false
	}
	r.i++
	return true
}
func (r *fakeImportRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	row := r.rows[r.i-1]
	*dest[0].(*int64) = row[0].(int64)
	*dest[1].(*string) = row[1].(string)
	if len(dest) > 2 {
		*dest[2].(*string) = row[2].(string)
	}
	return nil
}

// fakeImportTx embeds pgx.Tx (nil) and overrides only the verbs staging.go
// calls, scripting which one fails.
type fakeImportTx struct {
	pgx.Tx
	execErrs  []error // per Exec call, in order; a missing entry succeeds
	execCalls int
	rows      pgx.Rows
	queryErr  error
	copyErr   error
	copies    []string
}

func (tx *fakeImportTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	idx := tx.execCalls
	tx.execCalls++
	if idx < len(tx.execErrs) && tx.execErrs[idx] != nil {
		return pgconn.CommandTag{}, tx.execErrs[idx]
	}
	return pgconn.NewCommandTag("DELETE 1"), nil
}

func (tx *fakeImportTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	if tx.queryErr != nil {
		return nil, tx.queryErr
	}
	return tx.rows, nil
}

func (tx *fakeImportTx) CopyFrom(_ context.Context, table pgx.Identifier, _ []string, src pgx.CopyFromSource) (int64, error) {
	for src.Next() {
		if _, err := src.Values(); err != nil {
			return 0, err
		}
	}
	tx.copies = append(tx.copies, table.Sanitize())
	if tx.copyErr != nil {
		return 0, tx.copyErr
	}
	return 0, nil
}

func TestResolveSeeds_NilBlanksAndSanitizedColors(t *testing.T) {
	assert.Equal(t, resolvedSeed{}, resolveSeeds(nil))

	icon := "star"
	out := resolveSeeds(&jsonSeed{
		tags: []JSONTag{
			{Name: "  Go ", Color: "#abc", Icon: &icon},
			{Name: "   ", Color: "#aabbcc"},
			{Name: "Evil", Color: `red url("https://evil.test/x")`},
		},
		folders: []JSONFolder{
			{Name: " Work ", Color: "#00ff00"},
			{Name: ""},
		},
	})
	require.Len(t, out.tags, 2)
	assert.Equal(t, "Go", out.tags[0].name)
	assert.Equal(t, "#abc", out.tags[0].color)
	require.NotNil(t, out.tags[0].icon)
	assert.Equal(t, "star", *out.tags[0].icon)
	assert.True(t, out.tags[0].seeded)
	assert.Nil(t, out.tags[1].icon)
	assert.Equal(t, defaultImportColor, out.tags[1].color, "a tracking-pixel colour must be replaced, not staged")
	require.Len(t, out.folders, 1)
	assert.Equal(t, stagedImportFolder{name: "Work", color: "#00ff00"}, out.folders[0])
}

func TestValidateImportClickBudget_SingleItemRange(t *testing.T) {
	for _, n := range []int64{-1, maxImportClickCount + 1} {
		err := validateImportClickBudget([]Item{{ClickCount: n}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "click_count out of range")
	}
	require.NoError(t, validateImportClickBudget(nil))
	require.NoError(t, validateImportClickBudget([]Item{{ClickCount: maxImportClickCount}}))
}

func TestPlanImport_GroupsCatalogsAndLastTagged(t *testing.T) {
	seed := &jsonSeed{
		tags:    []JSONTag{{Name: "go", Color: "#111111"}, {Name: "seeded-only", Color: "#222222"}},
		folders: []JSONFolder{{Name: "Docs", Color: "#333333"}},
	}
	items := []Item{
		{URL: "https://a.test", Title: "A", Tags: []string{" go ", "go", "", " web "}, Folder: strPtr(" Docs ")},
		{URL: "https://a.test", Title: "A again"},                  // duplicate, untagged
		{URL: "https://b.test", Title: "B", Folder: strPtr("   ")}, // blank folder is no folder
		{URL: "https://b.test", Title: "B again", Tags: []string{"web"}, Folder: strPtr("New")},
	}
	plan := planImport(items, seed)

	assert.Equal(t, []string{"https://a.test", "https://b.test"}, plan.urls)
	assert.Equal(t, []string{"go", "web"}, plan.normalizedTags[0], "tags are trimmed and de-duplicated")
	assert.Equal(t, &importItemGroup{first: 0, last: 1, lastTagged: 0}, plan.groups["https://a.test"])
	assert.Equal(t, &importItemGroup{first: 2, last: 3, lastTagged: 3}, plan.groups["https://b.test"])

	// The seed colour wins over the default a same-named item tag would get.
	assert.Equal(t, "#111111", plan.tagCatalog["go"].color)
	assert.True(t, plan.tagCatalog["go"].seeded)
	assert.Equal(t, stagedImportTag{name: "web", color: defaultImportColor}, plan.tagCatalog["web"])
	assert.Equal(t, []string{"go", "seeded-only", "web"}, plan.tagOrder)

	assert.Equal(t, []string{"Docs", "New"}, plan.folderOrder)
	assert.Equal(t, "#333333", plan.folderCatalog["Docs"].color)
	assert.Equal(t, "Docs", plan.folderNames[0])
	assert.Equal(t, "", plan.folderNames[2])

	empty := planImport(nil, nil)
	assert.Empty(t, empty.urls)
	assert.Empty(t, empty.tagOrder)
}

func TestNormalizeImportTags_AndAddFolderEdges(t *testing.T) {
	assert.Empty(t, normalizeImportTags(nil))
	assert.Empty(t, normalizeImportTags([]string{" ", ""}))
	assert.Equal(t, []string{"b", "a"}, normalizeImportTags([]string{" b ", "a", "b"}),
		"first occurrence wins and order is preserved")

	plan := planImport(nil, nil)
	plan.addFolder("", "#fff")
	plan.addFolder("Docs", "#111")
	plan.addFolder("Docs", "#222")
	assert.Equal(t, []string{"Docs"}, plan.folderOrder, "duplicates and blanks stage nothing")
	assert.Equal(t, "#111", plan.folderCatalog["Docs"].color, "the first colour wins")
}

func TestPlanChooseRows_SkipExistingNewAndWipe(t *testing.T) {
	items := []Item{
		{URL: "https://old.test", Title: "Old", Tags: []string{"x"}},
		{URL: "https://old.test", Title: "Old 2"},
		{URL: "https://new.test", Title: "New"},
		{URL: "https://new.test", Title: "New 2", Tags: []string{"y"}},
		{URL: "https://solo.test", Title: "Solo"},
	}
	existing := map[string]existingImportLink{"https://old.test": {id: 9, slug: "old"}}

	skip := planImport(items, nil)
	skip.chooseRows(modeSkip, existing)
	assert.Equal(t, []int{2, 4}, skip.selectedIndexes, "skip selects the first row of each NEW url only")
	assert.True(t, skip.applyTags[3], "the last TAGGED duplicate supplies the new link's tags")
	assert.False(t, skip.applyTags[0], "a pre-existing link never gets its curated tags replaced")

	wipe := planImport(items, nil)
	wipe.chooseRows(modeWipe, existing)
	assert.Equal(t, []int{1, 3, 4}, wipe.selectedIndexes, "wipe selects the last row of every url")
	for _, i := range []int{1, 3, 4} {
		assert.True(t, wipe.applyTags[i])
	}
}

func TestPlanSlugBasesAndAllocateSlugs(t *testing.T) {
	items := []Item{
		{URL: "https://a.test", Title: "Hello World"},
		{URL: "https://b.test", Title: "Hello World"},
		{URL: "https://c.test", Title: "!!!"}, // slugifies to nothing
	}
	plan := planImport(items, nil)
	plan.chooseRows(modeSkip, nil)
	bases := plan.slugBases(items)
	assert.Equal(t, []string{"hello-world", "hello-world", "link-imported"}, bases)

	require.NoError(t, plan.allocateSlugs(items, modeSkip, nil, bases, []string{"hello-world"}))
	assert.NotEqual(t, plan.slugs[0], plan.slugs[1], "duplicate titles must not collide")
	assert.NotEqual(t, "hello-world", plan.slugs[0], "an already-taken slug is skipped")
	assert.Equal(t, "link-imported", plan.slugs[2])
	assert.Equal(t, []bool{true, true, true}, plan.selected)

	// An empty base is the allocator's error, and it must propagate.
	bad := planImport(items, nil)
	bad.chooseRows(modeSkip, nil)
	assert.Error(t, bad.allocateSlugs(items, modeSkip, nil, []string{"", "x", "y"}, nil))
}

func TestPlanAllocateSlugs_WipeReleasesTheOldSlug(t *testing.T) {
	items := []Item{{URL: "https://a.test", Title: "Hello World"}}
	plan := planImport(items, nil)
	existing := map[string]existingImportLink{"https://a.test": {id: 1, slug: "hello-world"}}
	plan.chooseRows(modeWipe, existing)
	bases := plan.slugBases(items)
	require.NoError(t, plan.allocateSlugs(items, modeWipe, existing, bases, []string{"hello-world"}))
	assert.Equal(t, "hello-world", plan.slugs[0],
		"wipe deletes the old row, so its slug is free for the replacement")
}

func TestLoadExistingImportLinks_EmptyScanAndRowsErrors(t *testing.T) {
	ctx := context.Background()
	uid := authctx.UserID(7)

	got, err := loadExistingImportLinks(ctx, nil, uid, nil)
	require.NoError(t, err)
	assert.Empty(t, got, "no URLs means no query (the nil tx would panic)")

	boom := errors.New("boom")
	_, err = loadExistingImportLinks(ctx, &fakeImportTx{queryErr: boom}, uid, []string{"u"})
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "load existing import URLs")

	_, err = loadExistingImportLinks(ctx, &fakeImportTx{rows: &fakeImportRows{rows: [][3]any{{int64(1), "u", "s"}}, scanErr: boom}}, uid, []string{"u"})
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "scan existing import URL")

	_, err = loadExistingImportLinks(ctx, &fakeImportTx{rows: &fakeImportRows{rowErr: boom}}, uid, []string{"u"})
	assert.ErrorIs(t, err, boom)

	rows := &fakeImportRows{rows: [][3]any{{int64(3), "https://a.test", "a-slug"}}}
	got, err = loadExistingImportLinks(ctx, &fakeImportTx{rows: rows}, uid, []string{"https://a.test"})
	require.NoError(t, err)
	assert.Equal(t, map[string]existingImportLink{"https://a.test": {id: 3, slug: "a-slug"}}, got)
}

func TestSelectRows_LoadTakenErrorPropagates(t *testing.T) {
	items := []Item{{URL: "https://a.test", Title: "A"}}
	plan := planImport(items, nil)
	boom := errors.New("taken exploded")
	err := plan.selectRows(context.Background(), &fakeImportTx{queryErr: boom}, items, modeSkip, nil)
	require.Error(t, err, "a taken-slug load failure must abort the import, not guess slugs")

	// Nothing selected => LoadTaken short-circuits and no tx is touched.
	none := planImport(nil, nil)
	require.NoError(t, none.selectRows(context.Background(), &fakeImportTx{queryErr: boom}, nil, modeSkip, nil))
}

func TestStagingCopyAndCreate_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("stage exploded")

	err := createImportStagingTables(ctx, &fakeImportTx{execErrs: []error{boom}})
	assert.ErrorIs(t, err, boom)
	require.NoError(t, createImportStagingTables(ctx, &fakeImportTx{}))

	// Empty stages never COPY — a nil tx proves it.
	empty := planImport(nil, nil)
	require.NoError(t, empty.copyToStaging(ctx, nil, nil))

	items := []Item{{URL: "https://a.test", Title: "A", Tags: []string{"x"}, Folder: strPtr("Docs")}}
	plan := planImport(items, nil)
	plan.chooseRows(modeSkip, nil)
	require.NoError(t, plan.allocateSlugs(items, modeSkip, nil, plan.slugBases(items), nil))

	tx := &fakeImportTx{}
	require.NoError(t, plan.copyToStaging(ctx, tx, items))
	assert.Len(t, tx.copies, 3, "items, tags and folders all stage")

	err = plan.copyItems(ctx, &fakeImportTx{copyErr: boom}, items)
	assert.Contains(t, err.Error(), "copy import item staging")
	assert.ErrorIs(t, err, boom)
	err = plan.copyTags(ctx, &fakeImportTx{copyErr: boom})
	assert.Contains(t, err.Error(), "copy import tag staging")
	err = plan.copyFolders(ctx, &fakeImportTx{copyErr: boom})
	assert.Contains(t, err.Error(), "copy import folder staging")
}

func TestUpsertAndRelations_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	uid := authctx.UserID(1)
	boom := errors.New("exec exploded")

	err := upsertImportCatalogs(ctx, &fakeImportTx{execErrs: []error{boom}}, uid)
	assert.Contains(t, err.Error(), "insert import folders")
	err = upsertImportCatalogs(ctx, &fakeImportTx{execErrs: []error{nil, boom}}, uid)
	assert.Contains(t, err.Error(), "insert import tags")
	require.NoError(t, upsertImportCatalogs(ctx, &fakeImportTx{}, uid))

	for i, want := range []string{"insert import clicks", "upsert import click stats", "replace import tags", "attach import tags"} {
		errs := make([]error, i+1)
		errs[i] = boom
		err := insertStagedImportRelations(ctx, &fakeImportTx{execErrs: errs}, uid)
		require.Error(t, err)
		assert.Contains(t, err.Error(), want)
	}
	require.NoError(t, insertStagedImportRelations(ctx, &fakeImportTx{}, uid))
}

func TestInsertStagedImportLinks_ErrorsAndRows(t *testing.T) {
	ctx := context.Background()
	uid := authctx.UserID(1)
	boom := errors.New("insert exploded")

	_, _, err := insertStagedImportLinks(ctx, &fakeImportTx{queryErr: boom}, uid, 1)
	assert.Contains(t, err.Error(), "insert import links")

	// The inserted-links stream is (id int64, url string) — reuse a two-column script.
	_, _, err = insertStagedImportLinks(ctx, &fakeImportTx{rows: &fakeImportRows{rows: [][3]any{{int64(1), "u", "s"}}, scanErr: boom}}, uid, 1)
	assert.Contains(t, err.Error(), "scan inserted import link")

	_, _, err = insertStagedImportLinks(ctx, &fakeImportTx{rows: &fakeImportRows{rowErr: boom}}, uid, 1)
	assert.ErrorIs(t, err, boom)

	ids, urls, err := insertStagedImportLinks(ctx, &fakeImportTx{rows: &fakeImportRows{rows: [][3]any{{int64(5), "https://a.test", "s"}}}}, uid, 1)
	require.NoError(t, err)
	assert.Equal(t, []int64{5}, ids)
	_, ok := urls["https://a.test"]
	assert.True(t, ok)
}

func TestWipeExistingImportLinks_ShortCircuits(t *testing.T) {
	ctx := context.Background()
	uid := authctx.UserID(1)
	existing := map[string]existingImportLink{"https://a.test": {id: 1, slug: "a"}}

	n, err := wipeExistingImportLinks(ctx, nil, uid, existing, modeSkip)
	assert.Zero(t, n)
	require.NoError(t, err, "skip mode wipes nothing and must not touch the tx")

	n, err = wipeExistingImportLinks(ctx, nil, uid, nil, modeWipe)
	assert.Zero(t, n)
	require.NoError(t, err)
}

func TestWipeExistingImportLinks_ExecError(t *testing.T) {
	// PurgeOwnerSet is the FIRST statement, so its failure is reported as
	// the reference wipe, never as a half-done delete.
	boom := errors.New("purge exploded")
	_, err := wipeExistingImportLinks(context.Background(), &fakeImportTx{execErrs: []error{boom}},
		authctx.UserID(1), map[string]existingImportLink{"u": {id: 2}}, modeWipe)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wipe import")
}

func TestPlanImport_DuplicateSeedTagsMergeAndCopyToStagingPropagates(t *testing.T) {
	icon := "star"
	// The same seed tag twice: one catalogue row, later colour wins, the
	// earlier icon survives a later entry that omits it.
	plan := planImport(nil, &jsonSeed{tags: []JSONTag{
		{Name: "go", Color: "#111111", Icon: &icon},
		{Name: "go", Color: "#222222"},
	}})
	assert.Equal(t, []string{"go"}, plan.tagOrder)
	assert.Equal(t, "#222222", plan.tagCatalog["go"].color)
	require.NotNil(t, plan.tagCatalog["go"].icon)
	assert.Equal(t, "star", *plan.tagCatalog["go"].icon)

	items := []Item{{URL: "https://a.test", Title: "A"}}
	full := planImport(items, nil)
	boom := errors.New("copy exploded")
	err := full.copyToStaging(context.Background(), &fakeImportTx{copyErr: boom}, items)
	assert.ErrorIs(t, err, boom, "the first failing COPY aborts the staging sequence")
}

func TestImportDuplicateWarnings(t *testing.T) {
	items := []Item{
		{URL: "https://a.test"},
		{URL: "https://a.test"},
		{URL: "https://b.test"},
	}
	groups := map[string]*importItemGroup{
		"https://a.test": {first: 0, last: 1},
		"https://b.test": {first: 2, last: 2},
	}
	inserted := map[string]struct{}{"https://a.test": {}}

	assert.Nil(t, importDuplicateWarnings(items, groups, inserted, modeSkip),
		"warnings are a duplicate-mode behaviour only")

	got := importDuplicateWarnings(items, groups, inserted, modeDuplicate)
	require.Len(t, got, 2, "the second a.test occurrence and the un-inserted b.test both warn")
	assert.Contains(t, got[0], "https://a.test")
	assert.Contains(t, got[1], "https://b.test")
}
