package importer

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"foldex/internal/entityrefs"
	"foldex/internal/pkg/authctx"
	slugpkg "foldex/internal/pkg/slug"
)

const maxImportSlugMatches = 1_000_000

type existingImportLink struct {
	id   int64
	slug string
}

type importItemGroup struct {
	first      int
	last       int
	lastTagged int
}

type stagedImportTag struct {
	name   string
	color  string
	icon   *string
	seeded bool
}

type stagedImportFolder struct {
	name  string
	color string
}

type importStagingPlan struct {
	groups          map[string]*importItemGroup
	urls            []string
	normalizedTags  [][]string
	folderNames     []string
	tagCatalog      map[string]stagedImportTag
	tagOrder        []string
	folderCatalog   map[string]stagedImportFolder
	folderOrder     []string
	selected        []bool
	applyTags       []bool
	slugs           []string
	selectedIndexes []int
}

func validateImportClickBudget(items []Item) error {
	var total int64
	for i, item := range items {
		if item.ClickCount < 0 || item.ClickCount > maxImportClickCount {
			return fmt.Errorf("items[%d]: click_count out of range (0..%d)", i, maxImportClickCount)
		}
		if item.ClickCount > maxImportTotalClicks-total {
			return fmt.Errorf("items[%d]: cumulative click_count exceeds %d", i, maxImportTotalClicks)
		}
		total += item.ClickCount
	}
	return nil
}

func newImportStagingPlan(items []Item, seed *jsonSeed) *importStagingPlan {
	plan := &importStagingPlan{
		groups:         make(map[string]*importItemGroup, len(items)),
		urls:           make([]string, 0, len(items)),
		normalizedTags: make([][]string, len(items)),
		folderNames:    make([]string, len(items)),
		tagCatalog:     make(map[string]stagedImportTag),
		tagOrder:       make([]string, 0),
		folderCatalog:  make(map[string]stagedImportFolder),
		folderOrder:    make([]string, 0),
		selected:       make([]bool, len(items)),
		applyTags:      make([]bool, len(items)),
		slugs:          make([]string, len(items)),
	}
	plan.addSeedCatalogs(seed)
	for i, item := range items {
		plan.addItem(i, item)
	}
	return plan
}

func (plan *importStagingPlan) addSeedCatalogs(seed *jsonSeed) {
	if seed == nil {
		return
	}
	for _, tag := range seed.tags {
		name := strings.TrimSpace(tag.Name)
		if name == "" {
			continue
		}
		staged, exists := plan.tagCatalog[name]
		if !exists {
			plan.tagOrder = append(plan.tagOrder, name)
		}
		staged.name = name
		staged.color = sanitizeImportColor(tag.Color)
		staged.seeded = true
		if tag.Icon != nil {
			icon := *tag.Icon
			staged.icon = &icon
		}
		plan.tagCatalog[name] = staged
	}
	for _, folder := range seed.folders {
		name := strings.TrimSpace(folder.Name)
		if name == "" {
			continue
		}
		plan.addFolder(name, sanitizeImportColor(folder.Color))
	}
}

func (plan *importStagingPlan) addItem(index int, item Item) {
	plan.normalizedTags[index] = normalizeImportTags(item.Tags)
	for _, name := range plan.normalizedTags[index] {
		if _, exists := plan.tagCatalog[name]; exists {
			continue
		}
		plan.tagOrder = append(plan.tagOrder, name)
		plan.tagCatalog[name] = stagedImportTag{name: name, color: defaultImportColor}
	}
	if item.Folder != nil {
		plan.folderNames[index] = strings.TrimSpace(*item.Folder)
		plan.addFolder(plan.folderNames[index], defaultImportColor)
	}

	group, exists := plan.groups[item.URL]
	if !exists {
		group = &importItemGroup{first: index, lastTagged: -1}
		plan.groups[item.URL] = group
		plan.urls = append(plan.urls, item.URL)
	}
	group.last = index
	if len(plan.normalizedTags[index]) > 0 {
		group.lastTagged = index
	}
}

func normalizeImportTags(names []string) []string {
	normalized := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, rawName := range names {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	return normalized
}

func (plan *importStagingPlan) addFolder(name, color string) {
	if name == "" {
		return
	}
	if _, exists := plan.folderCatalog[name]; exists {
		return
	}
	plan.folderOrder = append(plan.folderOrder, name)
	plan.folderCatalog[name] = stagedImportFolder{name: name, color: color}
}

func loadExistingImportLinks(ctx context.Context, tx pgx.Tx, uid authctx.UserID, urls []string) (map[string]existingImportLink, error) {
	existing := make(map[string]existingImportLink, len(urls))
	if len(urls) == 0 {
		return existing, nil
	}
	rows, err := tx.Query(ctx, `
        SELECT id, url, slug
        FROM link
        WHERE user_id = $1 AND url = ANY($2::text[])
    `, int64(uid), urls)
	if err != nil {
		return nil, fmt.Errorf("load existing import URLs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var url, slug string
		if err := rows.Scan(&id, &url, &slug); err != nil {
			return nil, fmt.Errorf("scan existing import URL: %w", err)
		}
		existing[url] = existingImportLink{id: id, slug: slug}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load existing import URLs: %w", err)
	}
	return existing, nil
}

func (plan *importStagingPlan) selectRows(ctx context.Context, tx pgx.Tx, items []Item, mode importMode, existing map[string]existingImportLink) error {
	plan.chooseRows(mode, existing)
	bases := plan.slugBases(items)
	taken, err := slugpkg.LoadTaken(ctx, tx, bases, maxImportSlugMatches)
	if err != nil {
		return err
	}
	return plan.allocateSlugs(items, mode, existing, bases, taken)
}

func (plan *importStagingPlan) chooseRows(mode importMode, existing map[string]existingImportLink) {
	for _, url := range plan.urls {
		group := plan.groups[url]
		if mode == modeWipe {
			plan.applyTags[group.last] = true
			plan.selectedIndexes = append(plan.selectedIndexes, group.last)
			continue
		}
		if group.lastTagged >= 0 {
			plan.applyTags[group.lastTagged] = true
		}
		if _, found := existing[url]; !found {
			plan.selectedIndexes = append(plan.selectedIndexes, group.first)
		}
	}
	sort.Ints(plan.selectedIndexes)
}

func (plan *importStagingPlan) slugBases(items []Item) []string {
	bases := make([]string, len(plan.selectedIndexes))
	for i, index := range plan.selectedIndexes {
		bases[i] = slugpkg.Slugify(items[index].Title)
		if bases[i] == "" {
			bases[i] = "link-imported"
		}
	}
	return bases
}

func (plan *importStagingPlan) allocateSlugs(items []Item, mode importMode, existing map[string]existingImportLink, bases, taken []string) error {
	allocator := slugpkg.NewAllocator(taken)
	for i, index := range plan.selectedIndexes {
		if mode == modeWipe {
			if old, found := existing[items[index].URL]; found {
				allocator.Release(old.slug)
			}
		}
		candidate, err := allocator.Allocate(bases[i])
		if err != nil {
			return err
		}
		plan.selected[index] = true
		plan.slugs[index] = candidate
	}
	return nil
}

func createImportStagingTables(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
        CREATE TEMP TABLE _import_item (
            ordinal integer PRIMARY KEY,
            url text NOT NULL,
            title text NOT NULL,
            description text,
            tag_names text[] NOT NULL,
            folder_name text,
            click_count bigint NOT NULL,
            created_at timestamptz,
            selected boolean NOT NULL,
            slug text,
            apply_tags boolean NOT NULL
        ) ON COMMIT DROP;
        CREATE TEMP TABLE _import_tag (
            name text PRIMARY KEY,
            color text NOT NULL,
            icon text,
            seeded boolean NOT NULL
        ) ON COMMIT DROP;
        CREATE TEMP TABLE _import_folder (
            name text PRIMARY KEY,
            color text NOT NULL
        ) ON COMMIT DROP;
        CREATE TEMP TABLE _import_inserted (
            id bigint PRIMARY KEY,
            url text UNIQUE NOT NULL
        ) ON COMMIT DROP;
    `)
	if err != nil {
		return fmt.Errorf("create import staging tables: %w", err)
	}
	return nil
}

func (plan *importStagingPlan) copyToStaging(ctx context.Context, tx pgx.Tx, items []Item) error {
	if err := plan.copyItems(ctx, tx, items); err != nil {
		return err
	}
	if err := plan.copyTags(ctx, tx); err != nil {
		return err
	}
	return plan.copyFolders(ctx, tx)
}

func (plan *importStagingPlan) copyItems(ctx context.Context, tx pgx.Tx, items []Item) error {
	if len(items) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(items))
	for i, item := range items {
		var folderName, candidate any
		if plan.folderNames[i] != "" {
			folderName = plan.folderNames[i]
		}
		if plan.selected[i] {
			candidate = plan.slugs[i]
		}
		rows = append(rows, []any{
			i, item.URL, item.Title, item.Description, plan.normalizedTags[i], folderName,
			item.ClickCount, item.CreatedAt, plan.selected[i], candidate, plan.applyTags[i],
		})
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"_import_item"},
		[]string{"ordinal", "url", "title", "description", "tag_names", "folder_name", "click_count", "created_at", "selected", "slug", "apply_tags"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return fmt.Errorf("copy import item staging: %w", err)
	}
	return nil
}

func (plan *importStagingPlan) copyTags(ctx context.Context, tx pgx.Tx) error {
	if len(plan.tagOrder) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(plan.tagOrder))
	for _, name := range plan.tagOrder {
		tag := plan.tagCatalog[name]
		rows = append(rows, []any{tag.name, tag.color, tag.icon, tag.seeded})
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"_import_tag"},
		[]string{"name", "color", "icon", "seeded"}, pgx.CopyFromRows(rows))
	if err != nil {
		return fmt.Errorf("copy import tag staging: %w", err)
	}
	return nil
}

func (plan *importStagingPlan) copyFolders(ctx context.Context, tx pgx.Tx) error {
	if len(plan.folderOrder) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(plan.folderOrder))
	for _, name := range plan.folderOrder {
		folder := plan.folderCatalog[name]
		rows = append(rows, []any{folder.name, folder.color})
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"_import_folder"},
		[]string{"name", "color"}, pgx.CopyFromRows(rows))
	if err != nil {
		return fmt.Errorf("copy import folder staging: %w", err)
	}
	return nil
}

func upsertImportCatalogs(ctx context.Context, tx pgx.Tx, uid authctx.UserID) error {
	if _, err := tx.Exec(ctx, `
        INSERT INTO folder (user_id, name, color)
        SELECT $1, staged.name, staged.color
        FROM _import_folder staged
        WHERE NOT EXISTS (
            SELECT 1 FROM folder existing
            WHERE existing.user_id = $1 AND existing.name = staged.name
        )
    `, int64(uid)); err != nil {
		return fmt.Errorf("insert import folders: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO tag AS current (user_id, name, color, icon)
        SELECT $1, name, color, icon FROM _import_tag
        ON CONFLICT (user_id, name) DO UPDATE SET
            color = CASE
                WHEN (SELECT staged.seeded FROM _import_tag staged WHERE staged.name = EXCLUDED.name)
                THEN EXCLUDED.color ELSE current.color
            END,
            icon = CASE
                WHEN (SELECT staged.seeded FROM _import_tag staged WHERE staged.name = EXCLUDED.name)
                THEN COALESCE(EXCLUDED.icon, current.icon) ELSE current.icon
            END
    `, int64(uid)); err != nil {
		return fmt.Errorf("insert import tags: %w", err)
	}
	return nil
}

func wipeExistingImportLinks(ctx context.Context, tx pgx.Tx, uid authctx.UserID, existing map[string]existingImportLink, mode importMode) (int, error) {
	if mode != modeWipe || len(existing) == 0 {
		return 0, nil
	}
	ids := make([]int64, 0, len(existing))
	for _, link := range existing {
		ids = append(ids, link.id)
	}
	if err := entityrefs.PurgeOwnerSet(ctx, tx, uid, "link", ids); err != nil {
		return 0, fmt.Errorf("wipe import link references: %w", err)
	}
	ct, err := tx.Exec(ctx, `
        DELETE FROM link
        WHERE user_id = $1 AND id = ANY($2::bigint[])
    `, int64(uid), ids)
	if err != nil {
		return 0, fmt.Errorf("wipe import links: %w", err)
	}
	return int(ct.RowsAffected()), nil
}

func insertStagedImportLinks(ctx context.Context, tx pgx.Tx, uid authctx.UserID, capacity int) ([]int64, map[string]struct{}, error) {
	rows, err := tx.Query(ctx, `
        WITH inserted AS (
            INSERT INTO link (user_id, url, title, slug, description, folder_id, created_at)
            SELECT $1, item.url, item.title, item.slug, item.description, folder.id,
                   COALESCE(item.created_at, now())
            FROM _import_item item
            LEFT JOIN LATERAL (
                SELECT id FROM folder
                WHERE user_id = $1 AND name = item.folder_name
                LIMIT 1
            ) folder ON item.folder_name IS NOT NULL
            WHERE item.selected
            ORDER BY item.ordinal
            ON CONFLICT (user_id, url) DO NOTHING
            RETURNING id, url
        )
        INSERT INTO _import_inserted (id, url)
        SELECT id, url FROM inserted
        RETURNING id, url
    `, int64(uid))
	if err != nil {
		return nil, nil, fmt.Errorf("insert import links: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0, capacity)
	urls := make(map[string]struct{}, capacity)
	for rows.Next() {
		var id int64
		var url string
		if err := rows.Scan(&id, &url); err != nil {
			return nil, nil, fmt.Errorf("scan inserted import link: %w", err)
		}
		ids = append(ids, id)
		urls[url] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("insert import links: %w", err)
	}
	return ids, urls, nil
}

func insertStagedImportRelations(ctx context.Context, tx pgx.Tx, uid authctx.UserID) error {
	if _, err := tx.Exec(ctx, `
        INSERT INTO click_log (entity_kind, entity_id, clicked_at, user_id)
        SELECT 'link', inserted.id, COALESCE(item.created_at, now()), $1
        FROM _import_item item
        JOIN _import_inserted inserted ON inserted.url = item.url
        CROSS JOIN LATERAL generate_series(1::bigint, item.click_count)
        WHERE item.selected AND item.click_count > 0
    `, int64(uid)); err != nil {
		return fmt.Errorf("insert import clicks: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        DELETE FROM link_tag refs
        USING link rows, _import_item item
        WHERE rows.user_id = $1
          AND rows.url = item.url
          AND item.apply_tags
          AND refs.entity_kind = 'link'
          AND refs.entity_id = rows.id
    `, int64(uid)); err != nil {
		return fmt.Errorf("replace import tags: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO link_tag (entity_kind, entity_id, tag_id)
        SELECT DISTINCT 'link', rows.id, tags.id
        FROM _import_item item
        JOIN link rows ON rows.user_id = $1 AND rows.url = item.url
        CROSS JOIN LATERAL unnest(item.tag_names) names(name)
        JOIN tag tags ON tags.user_id = $1 AND tags.name = names.name
        WHERE item.apply_tags
        ON CONFLICT DO NOTHING
    `, int64(uid)); err != nil {
		return fmt.Errorf("attach import tags: %w", err)
	}
	return nil
}

func importDuplicateWarnings(items []Item, groups map[string]*importItemGroup, insertedURLs map[string]struct{}, mode importMode) []string {
	if mode != modeDuplicate {
		return nil
	}
	warnings := make([]string, 0)
	for i, item := range items {
		_, inserted := insertedURLs[item.URL]
		if inserted && groups[item.URL].first == i {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("URL já existia, mantido o atual: %s", item.URL))
	}
	return warnings
}

func applyStagedImport(ctx context.Context, tx pgx.Tx, uid authctx.UserID, items []Item, mode importMode, seed *jsonSeed) (int, int, int, []string, []int64, error) {
	plan := newImportStagingPlan(items, seed)
	existing, err := loadExistingImportLinks(ctx, tx, uid, plan.urls)
	if err != nil {
		return 0, 0, 0, nil, nil, err
	}
	if err := plan.selectRows(ctx, tx, items, mode, existing); err != nil {
		return 0, 0, 0, nil, nil, err
	}
	if err := createImportStagingTables(ctx, tx); err != nil {
		return 0, 0, 0, nil, nil, err
	}
	if err := plan.copyToStaging(ctx, tx, items); err != nil {
		return 0, 0, 0, nil, nil, err
	}
	if err := upsertImportCatalogs(ctx, tx, uid); err != nil {
		return 0, 0, 0, nil, nil, err
	}
	wiped, err := wipeExistingImportLinks(ctx, tx, uid, existing, mode)
	if err != nil {
		return 0, 0, 0, nil, nil, err
	}
	freshIDs, insertedURLs, err := insertStagedImportLinks(ctx, tx, uid, len(plan.selectedIndexes))
	if err != nil {
		return 0, 0, 0, nil, nil, err
	}
	if err := insertStagedImportRelations(ctx, tx, uid); err != nil {
		return 0, 0, 0, nil, nil, err
	}

	imported := len(freshIDs)
	warnings := importDuplicateWarnings(items, plan.groups, insertedURLs, mode)
	return imported, len(items) - imported, wiped, warnings, freshIDs, nil
}
