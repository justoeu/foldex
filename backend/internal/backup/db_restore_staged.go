package backup

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/htmlsanitize"
	slugpkg "foldex/internal/pkg/slug"
)

const maxRestoreSlugMatches = 1_000_000

type existingRestoreLink struct {
	slug string
}

func restoreSkipStaged(ctx context.Context, tx pgx.Tx, uid authctx.UserID, snap *Snapshot) (Counts, Counts, idMapping, error) {
	var inserted, skipped Counts
	mapping := newIDMapping()

	existingLinks, err := loadExistingRestoreLinks(ctx, tx, uid, snap.Links)
	if err != nil {
		return inserted, skipped, mapping, err
	}
	linkSlugs, err := allocateRestoreLinkSlugs(ctx, tx, snap.Links, existingLinks)
	if err != nil {
		return inserted, skipped, mapping, err
	}
	noteSlugs, err := allocateRestoreNoteSlugs(ctx, tx, snap.Notes)
	if err != nil {
		return inserted, skipped, mapping, err
	}

	if err := createRestoreStagingTables(ctx, tx); err != nil {
		return inserted, skipped, mapping, err
	}
	if err := copyRestoreStaging(ctx, tx, snap, nil, linkSlugs, noteSlugs); err != nil {
		return inserted, skipped, mapping, err
	}

	ct, err := tx.Exec(ctx, `
		INSERT INTO tag (id, user_id, name, color, icon, created_at)
		SELECT new_id, $1, name, color, icon, created_at
		FROM _backup_restore_tag
		ORDER BY ordinal
		ON CONFLICT (user_id, name) DO NOTHING`, int64(uid))
	if err != nil {
		return inserted, skipped, mapping, fmt.Errorf("insert staged restore tags: %w", err)
	}
	inserted.Tags = ct.RowsAffected()
	skipped.Tags = int64(len(snap.Tags)) - inserted.Tags
	if err := loadStagedIDMapping(ctx, tx, `
		SELECT staged.old_id, live.id
		FROM _backup_restore_tag staged
		JOIN tag live ON live.user_id = $1 AND live.name = staged.name`, mapping.tagMap, int64(uid)); err != nil {
		return inserted, skipped, mapping, fmt.Errorf("map staged restore tags: %w", err)
	}

	inserted.Folders, err = insertStagedFolders(ctx, tx, uid, mapping)
	if err != nil {
		return inserted, skipped, mapping, err
	}

	links, err := restoreConflictLinks(ctx, tx, uid, snap.Links, existingLinks, mapping, ModeSkip)
	if err != nil {
		return inserted, skipped, mapping, err
	}
	inserted.Links, skipped.Links = links.inserted, links.skipped

	inserted.Notes, err = insertStagedNotes(ctx, tx, uid, mapping)
	if err != nil {
		return inserted, skipped, mapping, err
	}

	if err := attachPolymorphicTags(ctx, tx, mapping, snap, &inserted, &skipped, true); err != nil {
		return inserted, skipped, mapping, err
	}
	if err := copyPolymorphicClicks(ctx, tx, uid, mapping, snap, &inserted, &skipped, true); err != nil {
		return inserted, skipped, mapping, err
	}
	return inserted, skipped, mapping, nil
}

func restoreDuplicateStaged(ctx context.Context, tx pgx.Tx, uid authctx.UserID, snap *Snapshot) (Counts, []string, idMapping, error) {
	var inserted Counts
	mapping := newIDMapping()
	warnings := make([]string, 0)

	tagNames, err := allocateDuplicateTagNames(ctx, tx, uid, snap.Tags)
	if err != nil {
		return inserted, warnings, mapping, err
	}
	existingLinks, err := loadExistingRestoreLinks(ctx, tx, uid, snap.Links)
	if err != nil {
		return inserted, warnings, mapping, err
	}
	linkSlugs, err := allocateRestoreLinkSlugs(ctx, tx, snap.Links, existingLinks)
	if err != nil {
		return inserted, warnings, mapping, err
	}
	noteSlugs, err := allocateRestoreNoteSlugs(ctx, tx, snap.Notes)
	if err != nil {
		return inserted, warnings, mapping, err
	}
	if err := createRestoreStagingTables(ctx, tx); err != nil {
		return inserted, warnings, mapping, err
	}
	if err := copyRestoreStaging(ctx, tx, snap, tagNames, linkSlugs, noteSlugs); err != nil {
		return inserted, warnings, mapping, err
	}

	ct, err := tx.Exec(ctx, `
		INSERT INTO tag (id, user_id, name, color, icon, created_at)
		SELECT new_id, $1, name, color, icon, created_at
		FROM _backup_restore_tag
		ORDER BY ordinal`, int64(uid))
	if err != nil {
		return inserted, warnings, mapping, fmt.Errorf("insert staged duplicate tags: %w", err)
	}
	inserted.Tags = ct.RowsAffected()
	if err := loadStagedIDMapping(ctx, tx,
		`SELECT old_id, new_id FROM _backup_restore_tag`, mapping.tagMap); err != nil {
		return inserted, warnings, mapping, fmt.Errorf("map staged duplicate tags: %w", err)
	}
	for i, tag := range snap.Tags {
		if tagNames[i] != tag.Name {
			warnings = append(warnings, fmt.Sprintf("tag %q renomeada para %q", tag.Name, tagNames[i]))
		}
	}

	inserted.Folders, err = insertStagedFolders(ctx, tx, uid, mapping)
	if err != nil {
		return inserted, warnings, mapping, err
	}

	links, err := restoreConflictLinks(ctx, tx, uid, snap.Links, existingLinks, mapping, ModeDuplicate)
	if err != nil {
		return inserted, warnings, mapping, err
	}
	inserted.Links = links.inserted
	warnings = append(warnings, links.warnings...)

	inserted.Notes, err = insertStagedNotes(ctx, tx, uid, mapping)
	if err != nil {
		return inserted, warnings, mapping, err
	}

	if err := attachPolymorphicTags(ctx, tx, mapping, snap, &inserted, nil, false); err != nil {
		return inserted, warnings, mapping, err
	}
	if err := copyPolymorphicClicks(ctx, tx, uid, mapping, snap, &inserted, nil, false); err != nil {
		return inserted, warnings, mapping, err
	}
	return inserted, warnings, mapping, nil
}

func insertStagedFolders(ctx context.Context, tx pgx.Tx, uid authctx.UserID, mapping idMapping) (int64, error) {
	ct, err := tx.Exec(ctx, `
		INSERT INTO folder
		    (id, user_id, name, color, parent_id, password_hash, password_hint, created_at)
		SELECT child.new_id, $1, child.name, child.color, parent.new_id,
		       child.password_hash, child.password_hint, child.created_at
		FROM _backup_restore_folder child
		LEFT JOIN _backup_restore_folder parent ON parent.old_id = child.parent_old_id
		ORDER BY child.ordinal`, int64(uid))
	if err != nil {
		return 0, fmt.Errorf("insert staged folders: %w", err)
	}
	if err := loadStagedIDMapping(ctx, tx,
		`SELECT old_id, new_id FROM _backup_restore_folder`, mapping.folderMap); err != nil {
		return 0, fmt.Errorf("map staged folders: %w", err)
	}
	return ct.RowsAffected(), nil
}

func insertStagedNotes(ctx context.Context, tx pgx.Tx, uid authctx.UserID, mapping idMapping) (int64, error) {
	ct, err := tx.Exec(ctx, `
		INSERT INTO note
		    (id, user_id, title, slug, body_html, body_text, pinned, folder_id,
		     cover_url, is_public, created_at, updated_at)
		SELECT staged.new_id, $1, staged.title, staged.slug, staged.body_html,
		       staged.body_text, staged.pinned, folder.new_id, staged.cover_url,
		       staged.is_public, staged.created_at, staged.updated_at
		FROM _backup_restore_note staged
		LEFT JOIN _backup_restore_folder folder ON folder.old_id = staged.folder_old_id
		ORDER BY staged.ordinal`, int64(uid))
	if err != nil {
		return 0, fmt.Errorf("insert staged notes: %w", err)
	}
	if err := loadStagedIDMapping(ctx, tx,
		`SELECT old_id, new_id FROM _backup_restore_note`, mapping.noteMap); err != nil {
		return 0, fmt.Errorf("map staged notes: %w", err)
	}
	return ct.RowsAffected(), nil
}

type conflictLinkRestore struct {
	inserted int64
	skipped  int64
	warnings []string
}

func restoreConflictLinks(ctx context.Context, tx pgx.Tx, uid authctx.UserID, links []LinkRow, existing map[string]existingRestoreLink, mapping idMapping, mode ConflictMode) (conflictLinkRestore, error) {
	var restored conflictLinkRestore
	label := "restore"
	if mode == ModeDuplicate {
		label = "duplicate"
	}
	ct, err := tx.Exec(ctx, `
		INSERT INTO link
		    (id, user_id, url, title, slug, description, favicon_url, og_image_url,
		     pinned, preview_status, preview_error, folder_id, is_public, created_at, updated_at)
		SELECT staged.new_id, $1, staged.url, staged.title, staged.slug,
		       staged.description, staged.favicon_url, staged.og_image_url,
		       staged.pinned, staged.preview_status, staged.preview_error,
		       folder.new_id, staged.is_public, staged.created_at, staged.updated_at
		FROM _backup_restore_link staged
		LEFT JOIN _backup_restore_folder folder ON folder.old_id = staged.folder_old_id
		ORDER BY staged.ordinal
		ON CONFLICT (user_id, url) DO NOTHING`, int64(uid))
	if err != nil {
		return restored, fmt.Errorf("insert staged %s links: %w", label, err)
	}
	restored.inserted = ct.RowsAffected()
	if err := loadStagedIDMapping(ctx, tx, `
		SELECT staged.old_id, live.id
		FROM _backup_restore_link staged
		JOIN link live ON live.user_id = $1 AND live.url = staged.url`, mapping.linkMap, int64(uid)); err != nil {
		return restored, fmt.Errorf("map staged %s links: %w", label, err)
	}
	if mode == ModeSkip {
		restored.skipped = int64(len(links)) - restored.inserted
		return restored, nil
	}
	for _, link := range links {
		if _, found := existing[link.URL]; found {
			restored.warnings = append(restored.warnings, fmt.Sprintf("link %q já existia — não duplicado (URL é UNIQUE)", link.URL))
		}
	}
	return restored, nil
}

func allocateDuplicateTagNames(ctx context.Context, tx pgx.Tx, uid authctx.UserID, tags []TagRow) ([]string, error) {
	bases := make([]string, len(tags))
	for i := range tags {
		bases[i] = tags[i].Name
	}
	taken, err := loadTakenDuplicateTagNames(ctx, tx, uid, bases, maxRestoreSlugMatches)
	if err != nil {
		return nil, err
	}
	used := make(map[string]struct{}, len(taken)+len(tags))
	for _, name := range taken {
		used[name] = struct{}{}
	}
	allocated := make([]string, len(tags))
	for i, base := range bases {
		candidate := base
		for attempt := 1; attempt < slugpkg.MaxUniqueAttempts; attempt++ {
			if attempt > 1 {
				candidate = fmt.Sprintf("%s (%d)", base, attempt)
			}
			if _, exists := used[candidate]; exists {
				continue
			}
			used[candidate] = struct{}{}
			allocated[i] = candidate
			break
		}
		if allocated[i] == "" {
			return nil, fmt.Errorf("allocate duplicate tag name: exhausted attempts for %q", base)
		}
	}
	return allocated, nil
}

func loadTakenDuplicateTagNames(ctx context.Context, tx pgx.Tx, uid authctx.UserID, bases []string, limit int) ([]string, error) {
	if len(bases) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		WITH requested(base) AS (
			SELECT DISTINCT unnest($2::text[])
		)
		SELECT DISTINCT tags.name
		FROM tag tags
		JOIN requested r
		  ON tags.name = r.base
		  OR r.base = regexp_replace(tags.name, ' \([0-9]+\)$', '')
		WHERE tags.user_id = $1
		LIMIT $3`, int64(uid), bases, limit+1)
	if err != nil {
		return nil, fmt.Errorf("load duplicate tag names: %w", err)
	}
	defer rows.Close()
	taken := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan duplicate tag name: %w", err)
		}
		taken = append(taken, name)
		if len(taken) > limit {
			return nil, fmt.Errorf("load duplicate tag names: more than %d relevant collisions", limit)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load duplicate tag names: %w", err)
	}
	sort.Strings(taken)
	return taken, nil
}

func loadExistingRestoreLinks(ctx context.Context, tx pgx.Tx, uid authctx.UserID, links []LinkRow) (map[string]existingRestoreLink, error) {
	existing := make(map[string]existingRestoreLink)
	if len(links) == 0 {
		return existing, nil
	}
	urls := make([]string, len(links))
	for i := range links {
		urls[i] = links[i].URL
	}
	rows, err := tx.Query(ctx, `
		SELECT url, slug
		FROM link
		WHERE user_id = $1 AND url = ANY($2::text[])`, int64(uid), urls)
	if err != nil {
		return nil, fmt.Errorf("load existing restore links: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var url string
		var link existingRestoreLink
		if err := rows.Scan(&url, &link.slug); err != nil {
			return nil, fmt.Errorf("scan existing restore link: %w", err)
		}
		existing[url] = link
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load existing restore links: %w", err)
	}
	return existing, nil
}

func allocateRestoreLinkSlugs(ctx context.Context, tx pgx.Tx, links []LinkRow, existing map[string]existingRestoreLink) ([]string, error) {
	bases := make([]string, 0, len(links))
	for _, link := range links {
		if _, found := existing[link.URL]; found {
			continue
		}
		bases = append(bases, restoreSlugBase(link.Slug, link.Title, "link-restored"))
	}
	taken, err := slugpkg.LoadTaken(ctx, tx, bases, maxRestoreSlugMatches)
	if err != nil {
		return nil, err
	}
	allocator := slugpkg.NewAllocator(taken)
	allocatedByURL := make(map[string]string, len(links))
	out := make([]string, len(links))
	for i, link := range links {
		if current, found := existing[link.URL]; found {
			out[i] = current.slug
			continue
		}
		if allocated, found := allocatedByURL[link.URL]; found {
			out[i] = allocated
			continue
		}
		allocated, err := allocator.Allocate(restoreSlugBase(link.Slug, link.Title, "link-restored"))
		if err != nil {
			return nil, err
		}
		allocatedByURL[link.URL] = allocated
		out[i] = allocated
	}
	return out, nil
}

func allocateRestoreNoteSlugs(ctx context.Context, tx pgx.Tx, notes []NoteRow) ([]string, error) {
	bases := make([]string, len(notes))
	for i := range notes {
		bases[i] = restoreSlugBase(notes[i].Slug, notes[i].Title, "note-restored")
	}
	taken, err := loadTakenNoteSlugs(ctx, tx, bases, maxRestoreSlugMatches)
	if err != nil {
		return nil, err
	}
	allocator := slugpkg.NewAllocator(taken)
	out := make([]string, len(notes))
	for i, base := range bases {
		out[i], err = allocator.Allocate(base)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func restoreSlugBase(current, title, fallback string) string {
	if current != "" {
		return current
	}
	if generated := slugpkg.Slugify(title); generated != "" {
		return generated
	}
	return fallback
}

func createRestoreStagingTables(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		CREATE TEMP TABLE _backup_restore_tag (
			ordinal integer NOT NULL, old_id bigint NOT NULL, new_id bigint NOT NULL,
			name text NOT NULL, color text NOT NULL, icon text, created_at timestamptz NOT NULL
		) ON COMMIT DROP;
		CREATE TEMP TABLE _backup_restore_folder (
			ordinal integer NOT NULL, old_id bigint NOT NULL, new_id bigint NOT NULL,
			name text NOT NULL, color text NOT NULL, parent_old_id bigint,
			password_hash text, password_hint text, created_at timestamptz NOT NULL
		) ON COMMIT DROP;
		CREATE TEMP TABLE _backup_restore_link (
			ordinal integer NOT NULL, old_id bigint NOT NULL, new_id bigint NOT NULL,
			url text NOT NULL, title text NOT NULL, slug text NOT NULL, description text,
			favicon_url text, og_image_url text, pinned boolean NOT NULL,
			preview_status text NOT NULL, preview_error text, folder_old_id bigint,
			is_public boolean NOT NULL, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
		) ON COMMIT DROP;
		CREATE TEMP TABLE _backup_restore_note (
			ordinal integer NOT NULL, old_id bigint NOT NULL, new_id bigint NOT NULL,
			title text NOT NULL, slug text NOT NULL, body_html text NOT NULL,
			body_text text NOT NULL, pinned boolean NOT NULL, folder_old_id bigint,
			cover_url text, is_public boolean NOT NULL, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
		) ON COMMIT DROP`)
	if err != nil {
		return fmt.Errorf("create backup restore staging tables: %w", err)
	}
	return nil
}

func copyRestoreStaging(ctx context.Context, tx pgx.Tx, snap *Snapshot, tagNames, linkSlugs, noteSlugs []string) error {
	tagIDs, err := reserveRestoreIDs(ctx, tx, "tag_id_seq", len(snap.Tags))
	if err != nil {
		return err
	}
	folderIDs, err := reserveRestoreIDs(ctx, tx, "folder_id_seq", len(snap.Folders))
	if err != nil {
		return err
	}
	linkIDs, err := reserveRestoreIDs(ctx, tx, "link_id_seq", len(snap.Links))
	if err != nil {
		return err
	}
	noteIDs, err := reserveRestoreIDs(ctx, tx, "note_id_seq", len(snap.Notes))
	if err != nil {
		return err
	}
	folderParents, _ := normalizeRestoreFolderParents(snap.Folders)

	if len(snap.Tags) > 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"_backup_restore_tag"},
			[]string{"ordinal", "old_id", "new_id", "name", "color", "icon", "created_at"},
			pgx.CopyFromSlice(len(snap.Tags), func(i int) ([]any, error) {
				row := snap.Tags[i]
				name := row.Name
				if tagNames != nil {
					name = tagNames[i]
				}
				return []any{i, row.ID, tagIDs[i], name, row.Color, row.Icon, row.CreatedAt}, nil
			}))
		if err != nil {
			return fmt.Errorf("copy restore tags: %w", err)
		}
	}
	if len(snap.Folders) > 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"_backup_restore_folder"},
			[]string{"ordinal", "old_id", "new_id", "name", "color", "parent_old_id", "password_hash", "password_hint", "created_at"},
			pgx.CopyFromSlice(len(snap.Folders), func(i int) ([]any, error) {
				row := snap.Folders[i]
				return []any{i, row.ID, folderIDs[i], row.Name, row.Color, folderParents[i], row.PasswordHash, row.PasswordHint, row.CreatedAt}, nil
			}))
		if err != nil {
			return fmt.Errorf("copy restore folders: %w", err)
		}
	}
	if len(snap.Links) > 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"_backup_restore_link"},
			[]string{"ordinal", "old_id", "new_id", "url", "title", "slug", "description", "favicon_url", "og_image_url", "pinned", "preview_status", "preview_error", "folder_old_id", "is_public", "created_at", "updated_at"},
			pgx.CopyFromSlice(len(snap.Links), func(i int) ([]any, error) {
				row := snap.Links[i]
				return []any{i, row.ID, linkIDs[i], row.URL, row.Title, linkSlugs[i], row.Description,
					row.FaviconURL, row.OGImageURL, row.Pinned, row.PreviewStatus, row.PreviewError,
					row.FolderID, row.IsPublic, row.CreatedAt, row.UpdatedAt}, nil
			}))
		if err != nil {
			return fmt.Errorf("copy restore links: %w", err)
		}
	}
	if len(snap.Notes) > 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"_backup_restore_note"},
			[]string{"ordinal", "old_id", "new_id", "title", "slug", "body_html", "body_text", "pinned", "folder_old_id", "cover_url", "is_public", "created_at", "updated_at"},
			pgx.CopyFromSlice(len(snap.Notes), func(i int) ([]any, error) {
				row := snap.Notes[i]
				bodyHTML, bodyText := htmlsanitize.SanitizeAndPlain(row.BodyHTML)
				return []any{i, row.ID, noteIDs[i], row.Title, noteSlugs[i], bodyHTML, bodyText,
					row.Pinned, row.FolderID, row.CoverURL, row.IsPublic, row.CreatedAt, row.UpdatedAt}, nil
			}))
		if err != nil {
			return fmt.Errorf("copy restore notes: %w", err)
		}
	}
	return nil
}

func normalizeRestoreFolderParents(folders []FolderRow) ([]*int64, []string) {
	indexByID, warnings := indexRestoreFolders(folders)
	children, hasPendingParent := buildRestoreAdjacency(folders, indexByID)
	parents := make([]*int64, len(folders))
	processed := make([]bool, len(folders))
	drainRestoreParents(folders, children, hasPendingParent, parents, processed)
	nilRestoreCycleMembers(folders, indexByID, hasPendingParent, parents)
	drainRestoreParents(folders, children, hasPendingParent, parents, processed)
	return parents, warnings
}

func indexRestoreFolders(folders []FolderRow) (map[int64]int, []string) {
	indexByID := make(map[int64]int, len(folders))
	var warnings []string
	for i := range folders {
		if _, exists := indexByID[folders[i].ID]; exists {
			warnings = append(warnings, fmt.Sprintf("duplicate folder id %d; later row ignored", folders[i].ID))
			continue
		}
		indexByID[folders[i].ID] = i
	}
	return indexByID, warnings
}

func buildRestoreAdjacency(folders []FolderRow, indexByID map[int64]int) ([][]int, []bool) {
	children := make([][]int, len(folders))
	hasPendingParent := make([]bool, len(folders))
	seenID := make(map[int64]struct{}, len(indexByID))
	for i := range folders {
		if _, dup := seenID[folders[i].ID]; dup {
			continue
		}
		seenID[folders[i].ID] = struct{}{}
		if folders[i].ParentID == nil {
			continue
		}
		parentIndex, exists := indexByID[*folders[i].ParentID]
		if !exists {
			continue
		}
		hasPendingParent[i] = true
		children[parentIndex] = append(children[parentIndex], i)
	}
	return children, hasPendingParent
}

func drainRestoreParents(folders []FolderRow, children [][]int, hasPendingParent []bool, parents []*int64, processed []bool) {
	queue := make([]int, 0, len(folders))
	for i := range folders {
		if !processed[i] && !hasPendingParent[i] {
			queue = append(queue, i)
		}
	}
	head := 0
	for head < len(queue) {
		index := queue[head]
		head++
		if processed[index] {
			continue
		}
		processed[index] = true
		for _, child := range children[index] {
			if processed[child] || !hasPendingParent[child] {
				continue
			}
			hasPendingParent[child] = false
			parentID := folders[index].ID
			parents[child] = &parentID
			queue = append(queue, child)
		}
	}
}

func nilRestoreCycleMembers(folders []FolderRow, indexByID map[int64]int, hasPendingParent []bool, parents []*int64) {
	inCycle := make([]bool, len(folders))
	for i := range folders {
		if !hasPendingParent[i] {
			continue
		}
		for _, idx := range restoreCycleSlice(i, folders, indexByID, hasPendingParent) {
			inCycle[idx] = true
		}
	}
	for i := range inCycle {
		if !inCycle[i] {
			continue
		}
		parents[i] = nil
		hasPendingParent[i] = false
	}
}

func restoreCycleSlice(start int, folders []FolderRow, indexByID map[int64]int, pending []bool) []int {
	path := make([]int, 0, 8)
	at := map[int]int{}
	cur := start
	for pending[cur] {
		if j, ok := at[cur]; ok {
			return path[j:]
		}
		at[cur] = len(path)
		path = append(path, cur)
		if folders[cur].ParentID == nil {
			return nil
		}
		next, ok := indexByID[*folders[cur].ParentID]
		if !ok {
			return nil
		}
		cur = next
	}
	return nil
}

func reserveRestoreIDs(ctx context.Context, tx pgx.Tx, sequence string, count int) ([]int64, error) {
	if count == 0 {
		return nil, nil
	}
	var query string
	switch sequence {
	case "tag_id_seq":
		query = `SELECT nextval('tag_id_seq') FROM generate_series(1, $1)`
	case "folder_id_seq":
		query = `SELECT nextval('folder_id_seq') FROM generate_series(1, $1)`
	case "link_id_seq":
		query = `SELECT nextval('link_id_seq') FROM generate_series(1, $1)`
	case "note_id_seq":
		query = `SELECT nextval('note_id_seq') FROM generate_series(1, $1)`
	default:
		return nil, fmt.Errorf("reserve restore ids: unknown sequence %q", sequence)
	}
	rows, err := tx.Query(ctx, query, count)
	if err != nil {
		return nil, fmt.Errorf("reserve restore ids from %s: %w", sequence, err)
	}
	defer rows.Close()
	ids := make([]int64, 0, count)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("reserve restore ids from %s: %w", sequence, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reserve restore ids from %s: %w", sequence, err)
	}
	if len(ids) != count {
		return nil, fmt.Errorf("reserve restore ids from %s: got %d, want %d", sequence, len(ids), count)
	}
	return ids, nil
}

func loadStagedIDMapping(ctx context.Context, tx pgx.Tx, query string, mapping map[int64]int64, args ...any) error {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var oldID, newID int64
		if err := rows.Scan(&oldID, &newID); err != nil {
			return err
		}
		mapping[oldID] = newID
	}
	return rows.Err()
}
