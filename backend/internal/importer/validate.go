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

type itemAggregate struct {
	folderAggregates map[string]ValidationFolder
	tagSet           map[string]struct{}
	urls             []string
	urlFolders       map[string][]string
}

func Validate(ctx context.Context, pool *pgxpool.Pool, uid authctx.UserID, items []Item) (ValidationReport, error) {
	rep, agg := aggregateItems(items)

	existing, err := queryExistingURLs(ctx, pool, uid, agg.urls)
	if err != nil {
		return rep, err
	}
	applyConflicts(&rep, &agg, existing)

	if len(agg.tagSet) > 0 {
		names := make([]string, 0, len(agg.tagSet))
		for n := range agg.tagSet {
			names = append(names, n)
		}
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM tag WHERE user_id = $2 AND name = ANY($1::text[])`, names, int64(uid)).Scan(&rep.Conflicts.Tags); err != nil {
			return rep, fmt.Errorf("conflict tags: %w", err)
		}
	}

	sortFolders(&rep, agg.folderAggregates)
	return rep, nil
}

func aggregateItems(items []Item) (ValidationReport, itemAggregate) {
	rep := ValidationReport{
		Counts:   ValidationCounts{},
		Warnings: []string{},
	}
	agg := itemAggregate{
		folderAggregates: make(map[string]ValidationFolder),
		tagSet:           map[string]struct{}{},
		urls:             make([]string, 0, len(items)),
		urlFolders:       make(map[string][]string, len(items)),
	}
	for _, it := range items {
		var folderPath string
		if it.Folder != nil {
			folderPath = strings.TrimSpace(*it.Folder)
		}
		if folderPath == "" {
			rep.Ungrouped.Links++
		} else {
			aggregate := agg.folderAggregates[folderPath]
			aggregate.Path = folderPath
			aggregate.Name = folderPath
			aggregate.Count++
			agg.folderAggregates[folderPath] = aggregate
		}
		if _, seen := agg.urlFolders[it.URL]; !seen {
			agg.urls = append(agg.urls, it.URL)
		}
		agg.urlFolders[it.URL] = append(agg.urlFolders[it.URL], folderPath)
		for _, t := range it.Tags {
			agg.tagSet[t] = struct{}{}
		}
	}
	rep.Counts.Links = len(items)
	rep.Counts.Folders = len(agg.folderAggregates)
	rep.Counts.Tags = len(agg.tagSet)
	return rep, agg
}

func queryExistingURLs(ctx context.Context, pool *pgxpool.Pool, uid authctx.UserID, urls []string) ([]string, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx,
		`SELECT url FROM link WHERE user_id = $2 AND url = ANY($1::text[])`, urls, int64(uid))
	if err != nil {
		return nil, fmt.Errorf("conflict links: %w", err)
	}
	defer rows.Close()
	existing := make([]string, 0)
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		existing = append(existing, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conflict links: %w", err)
	}
	return existing, nil
}

func applyConflicts(rep *ValidationReport, agg *itemAggregate, existing []string) {
	for _, u := range existing {
		folders, ok := agg.urlFolders[u]
		if !ok || len(folders) == 0 {
			continue
		}
		rep.Conflicts.Links++
		seen := make(map[string]struct{}, len(folders))
		for _, folderPath := range folders {
			if _, dup := seen[folderPath]; dup {
				continue
			}
			seen[folderPath] = struct{}{}
			if folderPath == "" {
				rep.Ungrouped.Conflicts++
				continue
			}
			aggregate := agg.folderAggregates[folderPath]
			aggregate.Conflicts++
			agg.folderAggregates[folderPath] = aggregate
		}
	}
}

func sortFolders(rep *ValidationReport, folderAggregates map[string]ValidationFolder) {
	rep.Folders = make([]ValidationFolder, 0, len(folderAggregates))
	for _, aggregate := range folderAggregates {
		rep.Folders = append(rep.Folders, aggregate)
	}
	sort.Slice(rep.Folders, func(i, j int) bool { return rep.Folders[i].Path < rep.Folders[j].Path })
}
