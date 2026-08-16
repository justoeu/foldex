package importer

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
)

type ValidationReport struct {
	Format    string              `json:"format"`
	Counts    ValidationCounts    `json:"counts"`
	Conflicts ValidationCounts    `json:"conflicts"`
	Folders   []ValidationFolder  `json:"folders"`
	Ungrouped ValidationAggregate `json:"ungrouped"`
	Warnings  []string            `json:"warnings"`
}

type ValidationCounts struct {
	Links   int `json:"links"`
	Folders int `json:"folders"`
	Tags    int `json:"tags"`
}

type ValidationFolder struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Count     int    `json:"count"`
	Conflicts int    `json:"conflicts"`
}

type ValidationAggregate struct {
	Links     int `json:"links"`
	Conflicts int `json:"conflicts"`
}

func Validate(ctx context.Context, pool *pgxpool.Pool, uid authctx.UserID, items []Item) (ValidationReport, error) {
	rep := ValidationReport{
		Counts:   ValidationCounts{},
		Warnings: []string{},
	}

	folderAggregates := make(map[string]ValidationFolder)
	tagSet := map[string]struct{}{}
	urls := make([]string, 0, len(items))
	urlFolders := make(map[string]string, len(items))

	for _, it := range items {
		var folderPath string
		if it.Folder != nil {
			folderPath = strings.TrimSpace(*it.Folder)
		}
		if folderPath == "" {
			rep.Ungrouped.Links++
		} else {
			aggregate := folderAggregates[folderPath]
			aggregate.Path = folderPath
			aggregate.Name = folderPath
			aggregate.Count++
			folderAggregates[folderPath] = aggregate
		}
		if _, seen := urlFolders[it.URL]; !seen {
			urls = append(urls, it.URL)
		}
		urlFolders[it.URL] = folderPath
		for _, t := range it.Tags {
			tagSet[t] = struct{}{}
		}
	}

	rep.Counts.Links = len(items)
	rep.Counts.Folders = len(folderAggregates)
	rep.Counts.Tags = len(tagSet)

	if len(urls) > 0 {
		rows, err := pool.Query(ctx,
			`SELECT url FROM link WHERE user_id = $2 AND url = ANY($1::text[])`, urls, int64(uid))
		if err != nil {
			return rep, fmt.Errorf("conflict links: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var u string
			if err := rows.Scan(&u); err != nil {
				return rep, err
			}
			folderPath, ok := urlFolders[u]
			if !ok {
				continue
			}
			rep.Conflicts.Links++
			if folderPath == "" {
				rep.Ungrouped.Conflicts++
			} else {
				aggregate := folderAggregates[folderPath]
				aggregate.Conflicts++
				folderAggregates[folderPath] = aggregate
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
			`SELECT count(*) FROM tag WHERE user_id = $2 AND name = ANY($1::text[])`, names, int64(uid)).Scan(&rep.Conflicts.Tags); err != nil {
			return rep, fmt.Errorf("conflict tags: %w", err)
		}
	}

	rep.Folders = make([]ValidationFolder, 0, len(folderAggregates))
	for _, aggregate := range folderAggregates {
		rep.Folders = append(rep.Folders, aggregate)
	}
	sort.Slice(rep.Folders, func(i, j int) bool { return rep.Folders[i].Path < rep.Folders[j].Path })
	return rep, nil
}
