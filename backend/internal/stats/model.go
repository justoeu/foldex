package stats

import "time"

type Summary struct {
	TotalLinks     int64 `json:"total_links"`
	TotalTags      int64 `json:"total_tags"`
	TotalClicks    int64 `json:"total_clicks"`
	ClicksLast30d  int64 `json:"clicks_last_30d"`
	ClicksPrev30d  int64 `json:"clicks_prev_30d"`
	NewLinksLast30 int64 `json:"new_links_last_30d"`
	TopHost        string `json:"top_host"`
	TopHostClicks  int64  `json:"top_host_clicks"`
}

type DailyPoint struct {
	Date   time.Time `json:"date"`
	Clicks int64     `json:"clicks"`
}

type TopLink struct {
	ID         int64  `json:"id"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Slug       string `json:"slug"`
	Host       string `json:"host"`
	Clicks     int64  `json:"clicks"`
	Clicks30d  int64  `json:"clicks_30d"`
	ClicksPrev int64  `json:"clicks_prev_30d"`
}

type TagBucket struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
	Clicks  int64  `json:"clicks"`
	Links   int64  `json:"links"`
}
