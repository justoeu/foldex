package importer

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ValidationReport is what /api/import/validate returns. It mirrors what the
// frontend needs to render the preview dialog (mode picker + folder selection
// tree) without any DB mutation having happened yet.
type ValidationReport struct {
	Format    string             `json:"format"`
	Counts    ValidationCounts   `json:"counts"`
	Conflicts ValidationCounts   `json:"conflicts"`
	Folders   []ValidationFolder `json:"folders"`
	Links     []ValidationLink   `json:"links"`
	Warnings  []string           `json:"warnings"`
}

type ValidationCounts struct {
	Links   int `json:"links"`
	Folders int `json:"folders"`
	Tags    int `json:"tags"`
}

type ValidationFolder struct {
	Path  string `json:"path"`  // full path "Bookmarks Bar > Work > Issues"
	Name  string `json:"name"`  // last segment, used as the foldex folder name
	Count int    `json:"count"` // links inside this folder (not recursive)
}

type ValidationLink struct {
	URL      string   `json:"url"`
	Title    string   `json:"title"`
	Folder   string   `json:"folder,omitempty"` // matches a Folders[].Path
	Tags     []string `json:"tags,omitempty"`
	Conflict bool     `json:"conflict"` // URL already exists in the DB
}

// Validate parses the upload and computes conflict counts against the live
// DB without writing anything. The frontend uses the resulting report to
// drive the preview dialog (mode picker + folder selection).
func Validate(ctx context.Context, pool *pgxpool.Pool, items []Item) (ValidationReport, error) {
	rep := ValidationReport{
		Counts:   ValidationCounts{},
		Warnings: []string{},
	}

	// Group links by folder so the frontend can render a tree-like preview.
	folderCounts := map[string]int{} // folder path → link count
	tagSet := map[string]struct{}{}
	links := make([]ValidationLink, 0, len(items))

	for _, it := range items {
		var folderPath string
		if it.Folder != nil {
			folderPath = strings.TrimSpace(*it.Folder)
		}
		links = append(links, ValidationLink{
			URL:    it.URL,
			Title:  it.Title,
			Folder: folderPath,
			Tags:   it.Tags,
		})
		if folderPath != "" {
			folderCounts[folderPath]++
		}
		for _, t := range it.Tags {
			tagSet[t] = struct{}{}
		}
	}

	rep.Counts.Links = len(items)
	rep.Counts.Folders = len(folderCounts)
	rep.Counts.Tags = len(tagSet)

	folders := make([]ValidationFolder, 0, len(folderCounts))
	for path, count := range folderCounts {
		folders = append(folders, ValidationFolder{
			Path:  path,
			Name:  path, // current parser already gives the deepest name
			Count: count,
		})
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Path < folders[j].Path })
	rep.Folders = folders

	// Conflict detection against the live DB. URL is the unique key for
	// links; tag.name for tags; folder has no unique constraint so we don't
	// report conflicts for folders (they merge on name in skip mode anyway).
	if len(links) > 0 {
		urls := make([]string, 0, len(links))
		urlIdx := make(map[string]int, len(links))
		for i, l := range links {
			urls = append(urls, l.URL)
			urlIdx[l.URL] = i
		}
		rows, err := pool.Query(ctx,
			`SELECT url FROM link WHERE url = ANY($1::text[])`, urls)
		if err != nil {
			return rep, fmt.Errorf("conflict links: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var u string
			if err := rows.Scan(&u); err != nil {
				return rep, err
			}
			if i, ok := urlIdx[u]; ok {
				links[i].Conflict = true
				rep.Conflicts.Links++
			}
		}
		if err := rows.Err(); err != nil {
			return rep, fmt.Errorf("conflict links: %w", err)
		}
	}
	if len(tagSet) > 0 {
		names := make([]string, 0, len(tagSet))
		for n := range tagSet {
			names = append(names, n)
		}
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM tag WHERE name = ANY($1::text[])`, names).Scan(&rep.Conflicts.Tags); err != nil {
			return rep, fmt.Errorf("conflict tags: %w", err)
		}
	}

	rep.Links = links
	return rep, nil
}
