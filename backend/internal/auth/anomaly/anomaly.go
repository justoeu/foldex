package anomaly

import (
	"fmt"
	"log/slog"
	"sort"
	"time"
)

const (
	KindSpray    = "spray"
	KindHammer   = "hammer"
	KindThrottle = "throttle"
)

const (
	// Same values as auth.SeverityCritical / SeverityWarning — the panel and
	// the trail sit on the same screen, so a badge colour must come from one
	// vocabulary.
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
)

const MaxRows = 100
const QueryLimit = 500

type Anomaly struct {
	Kind             string    `json:"kind"`
	IP               string    `json:"ip"`
	IPTrusted        bool      `json:"ip_trusted"`
	DistinctAccounts int       `json:"distinct_accounts"`
	Failures         int       `json:"failures"`
	Throttles        int       `json:"throttles"`
	FirstSeen        time.Time `json:"first_seen"`
	LastSeen         time.Time `json:"last_seen"`
	Blocked          bool      `json:"blocked"`
	Severity         string    `json:"severity"`
}

type Thresholds struct {
	SprayAccounts  int `json:"spray_accounts"`
	HammerFailures int `json:"hammer_failures"`
	WindowMinutes  int `json:"window_minutes"`
}

var windows = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

func Window(raw string, policyMinutes int) (time.Duration, string, bool) {
	if raw == "" {
		return time.Duration(policyMinutes) * time.Minute, fmt.Sprintf("%dm", policyMinutes), true
	}
	d, ok := windows[raw]
	return d, raw, ok
}

func SeverityFor(value, threshold int) string {
	if threshold > 0 && value >= 2*threshold {
		return SeverityCritical
	}
	return SeverityWarning
}

func Rank(in []Anomaly, limit int, log *slog.Logger) []Anomaly {
	sort.SliceStable(in, func(i, j int) bool {
		li, lj := severityRank(in[i].Severity), severityRank(in[j].Severity)
		if li != lj {
			return li < lj
		}
		return in[i].LastSeen.After(in[j].LastSeen)
	})
	if len(in) > limit {
		if log != nil {
			log.Warn("anomaly list truncated to the page cap",
				"kept", limit, "dropped", len(in)-limit)
		}
		in = in[:limit]
	}
	return in
}

func severityRank(s string) int {
	if s == SeverityCritical {
		return 0
	}
	return 1
}
