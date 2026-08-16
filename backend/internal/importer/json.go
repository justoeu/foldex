package importer

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"foldex/internal/pkg/cssvalid"
)

const (
	// maxImportClickCount bounds one link's synthetic history.
	maxImportClickCount = 10_000
	// maxImportTotalClicks bounds the whole request, preventing a valid
	// 50,000-link file from multiplying the per-link cap into 500M rows.
	maxImportTotalClicks = 1_000_000
)

type JSONFile struct {
	Version    int          `json:"version"`
	ExportedAt string       `json:"exported_at,omitempty"`
	Tags       []JSONTag    `json:"tags"`
	Folders    []JSONFolder `json:"folders"`
	Links      []JSONLink   `json:"links"`
}

type JSONTag struct {
	Name  string  `json:"name"`
	Color string  `json:"color"`
	Icon  *string `json:"icon"`
}

type JSONFolder struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type JSONLink struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Description *string  `json:"description"`
	Tags        []string `json:"tags"`
	Folder      *string  `json:"folder"`
	ClickCount  int64    `json:"click_count"`
	CreatedAt   string   `json:"created_at,omitempty"`
}

func ParseJSON(r io.Reader) (JSONFile, error) {
	var f JSONFile
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&f); err != nil {
		return f, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return f, fmt.Errorf("multiple JSON documents are not allowed")
		}
		return f, fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return f, nil
}

func (f JSONFile) Validate() error {
	// Accept both v1 (pre-folders) and v2 backups for round-trip stability.
	if f.Version != 1 && f.Version != 2 {
		return fmt.Errorf("unsupported version %d (expected 1 or 2)", f.Version)
	}
	for i, fl := range f.Folders {
		name := strings.TrimSpace(fl.Name)
		if name == "" {
			return fmt.Errorf("folders[%d]: name is required", i)
		}
		if len(name) > 200 {
			return fmt.Errorf("folders[%d]: name too long (max 200)", i)
		}
		// Empty color is allowed (the apply layer defaults it); a non-empty
		// value MUST pass the cssvalid allowlist, otherwise an untrusted
		// JSON file could plant `red url("https://evil/exfil")` and turn
		// every folder chip render into a tracking pixel (CLAUDE.md §4).
		if fl.Color != "" && !cssvalid.IsValidColor(fl.Color) {
			return fmt.Errorf("folders[%d]: color must be a hex (#abc, #aabbcc) or linear-gradient(135deg, #hex, #hex)", i)
		}
	}
	for i, t := range f.Tags {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			return fmt.Errorf("tags[%d]: name is required", i)
		}
		if len(name) > 80 {
			return fmt.Errorf("tags[%d]: name too long (max 80)", i)
		}
		// Same tracking-pixel defense as folders above.
		if t.Color != "" && !cssvalid.IsValidColor(t.Color) {
			return fmt.Errorf("tags[%d]: color must be a hex (#abc, #aabbcc) or linear-gradient(135deg, #hex, #hex)", i)
		}
	}
	var totalClicks int64
	for i, l := range f.Links {
		rawURL := strings.TrimSpace(l.URL)
		if rawURL == "" {
			return fmt.Errorf("links[%d]: url is required", i)
		}
		u, err := url.Parse(rawURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("links[%d]: url must be an absolute http(s) URL", i)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("links[%d]: url scheme must be http or https", i)
		}
		if len(strings.TrimSpace(l.Title)) > 500 {
			return fmt.Errorf("links[%d]: title too long (max 500)", i)
		}
		if l.ClickCount < 0 || l.ClickCount > maxImportClickCount {
			return fmt.Errorf("links[%d]: click_count out of range (0..%d)", i, maxImportClickCount)
		}
		if l.ClickCount > maxImportTotalClicks-totalClicks {
			return fmt.Errorf("links[%d]: cumulative click_count exceeds %d", i, maxImportTotalClicks)
		}
		totalClicks += l.ClickCount
		for j, tagName := range l.Tags {
			tname := strings.TrimSpace(tagName)
			if tname == "" {
				return fmt.Errorf("links[%d].tags[%d]: name is required", i, j)
			}
			if len(tname) > 80 {
				return fmt.Errorf("links[%d].tags[%d]: name too long (max 80)", i, j)
			}
		}
		if l.CreatedAt != "" {
			if _, err := time.Parse(time.RFC3339, l.CreatedAt); err != nil {
				return fmt.Errorf("links[%d]: invalid created_at %q (must be RFC3339)", i, l.CreatedAt)
			}
		}
	}
	return nil
}
