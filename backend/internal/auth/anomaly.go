package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// Anomaly detection — ADR-47, docs/SDD-ABUSE-DEFENSE.md §8.
//
// This file READS the trail and ranks what it finds. It never blocks, never
// tightens a limit and never writes a row, and that is a decision rather than
// an omission: INV-178 says the failure mode of the blocklist is not "a block
// that does not work", it is an instance nobody can reach, installed by the
// person who most needed to reach it. A heuristic holding that button would
// produce exactly that, unattended, at three in the morning — and the input to
// the heuristic is written by the attacker. So the detector orders and
// reports; installing a block stays a human act behind an owner-only, locked
// permission with its own rails.
//
// The whole panel is built out of rows the trail already has. Nothing here
// stores a derived signal: a stored classification would freeze whatever the
// rules meant on the day it was written, which is the same reason
// audit_vocab.go derives category and severity instead of storing them.

// The three rules. One origin can trip more than one, and each is reported as
// its own row — a spray and a lockout from the same address are two facts
// about it, and collapsing them would hide the one the operator is deciding on.
const (
	// AnomalyKindSpray is breadth: one origin failed against many DISTINCT
	// accounts. This is the signal the panel exists for, and it is the same
	// question SDD §4.2 made the IP bucket count.
	AnomalyKindSpray = "spray"
	// AnomalyKindHammer is depth: one origin failed many times against a
	// SINGLE account. Deliberately separate from spray, because an office
	// behind a NAT produces depth all day and never produces breadth.
	AnomalyKindHammer = "hammer"
	// AnomalyKindThrottle is a limiter that actually entered lockout. One is
	// enough: it is not an inference about the traffic, it is the record that
	// the instance already stopped answering that origin.
	AnomalyKindThrottle = "throttle"
)

// Severity of an anomaly ROW, which is not the severity of an audit ENTRY.
//
// Two levels rather than the trail's three, because the panel answers one
// question — is this worth acting on right now — and "info" has no answer to
// it: a row that is not worth looking at should not be on the list at all.
const (
	AnomalySeverityCritical = "critical"
	AnomalySeverityWarn     = "warn"
)

// maxAnomalyRows bounds what one response carries.
//
// A hundred is far past the point a person reads, and past the point the
// screen is useful — but the cap exists so the response cannot grow with the
// attack, which is the shape of every amplifier this document is about.
const maxAnomalyRows = 100

// anomalyQueryLimit bounds how much each rule's aggregate returns, so the cap
// above is not enforced only after the database has already built an unbounded
// result set for a screen that will render a hundred rows.
const anomalyQueryLimit = 500

// Anomaly is one (origin, rule) finding.
//
// It carries the COUNT of accounts and never their addresses. The attacked
// mailbox is identity data that already lives on the trail's own timeline,
// behind the read split INV-175 built; repeating the list here would create a
// second surface returning it — one no projection guards — which is exactly
// the multiplication that invariant went to the trouble of reducing to one.
type Anomaly struct {
	Kind      string `json:"kind"`
	IP        string `json:"ip"`
	IPTrusted bool   `json:"ip_trusted"`
	// DistinctAccounts is how many accounts this origin touched: for a spray
	// the number it swept, for a hammer the number it crossed the threshold
	// against.
	DistinctAccounts int `json:"distinct_accounts"`
	// Failures is the failure count the rule measured — the origin's total for
	// a spray, and the worst single account's for a hammer.
	Failures  int       `json:"failures"`
	Throttles int       `json:"throttles"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Blocked   bool      `json:"blocked"`
	Severity  string    `json:"severity"`
}

// AnomalyThresholds is what the panel applied, echoed with the answer so the
// screen can say WHY a row is on it rather than restating numbers it copied.
type AnomalyThresholds struct {
	SprayAccounts  int `json:"spray_accounts"`
	HammerFailures int `json:"hammer_failures"`
	WindowMinutes  int `json:"window_minutes"`
}

// anomalyWindows are the periods the screen offers. A closed set for
// parseAuditFilter's reason: an arbitrary window is an arbitrary amount of
// scanning an administrator can ask the database for.
var anomalyWindows = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// anomalyWindow resolves the requested period. An absent one is the CONFIGURED
// window rather than a hard-coded default: the owner already answered "how far
// back does an anomaly count" when they saved the policy, and offering a
// different answer on first load would make the screen disagree with the
// document it renders. Anything outside the vocabulary is refused.
func anomalyWindow(raw string, policyMinutes int) (time.Duration, string, bool) {
	if raw == "" {
		return time.Duration(policyMinutes) * time.Minute, fmt.Sprintf("%dm", policyMinutes), true
	}
	d, ok := anomalyWindows[raw]
	return d, raw, ok
}

// anomalySeverity escalates a count against the threshold it crossed.
//
// Twice the threshold, because crossing it is the definition of the row being
// present at all: if that already painted the row critical, the level would
// carry no information and the operator would have to find the real one by
// reading rather than by looking. A non-positive threshold cannot escalate —
// the policy bounds forbid one, and this keeps a later widening of those
// bounds from silently repainting the whole panel.
func anomalySeverity(value, threshold int) string {
	if threshold > 0 && value >= 2*threshold {
		return AnomalySeverityCritical
	}
	return AnomalySeverityWarn
}

// rankAnomalies orders the merged findings and applies the row cap.
//
// Severity first and recency second: the panel is scanned rather than read, so
// the worst line has to be at the top, and among equally bad lines the most
// recent is the one still happening. The cut is LOGGED — a list silently
// truncated turns "there are nine anomalies" into a claim the screen makes and
// the data does not support, during the one incident where that matters.
func rankAnomalies(in []Anomaly, limit int, log *slog.Logger) []Anomaly {
	sort.SliceStable(in, func(i, j int) bool {
		li, lj := anomalySeverityRank(in[i].Severity), anomalySeverityRank(in[j].Severity)
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

func anomalySeverityRank(s string) int {
	if s == AnomalySeverityCritical {
		return 0
	}
	return 1
}

// Anomalies runs the three rules over one window.
//
// Three statements rather than one: they are genuinely different aggregations
// — breadth per origin, depth per (origin, account), and a count of lockouts —
// and a single statement would be a pile of FILTER clauses over a union nobody
// could read. The count is fixed regardless of how much data comes back.
//
// Every window bound is `now() - $1::interval`, computed by the DATABASE.
// INV-180 records what a client-side bound cost the daily chart: a `::date`
// parameter made the driver encode the PROCESS's local calendar day while the
// column was compared in the session's zone, so away from UTC the busiest
// bucket read zero and it looked like a quiet day. Here the same mistake would
// silently narrow or widen every rule's window.
func (r *Repository) Anomalies(ctx context.Context, window time.Duration,
	th AnomalyThresholds) ([]Anomaly, error) {
	var out []Anomaly
	iv := intervalArg(window)

	spray, err := r.pool.Query(ctx, `
		SELECT host(ip), bool_or(ip_trusted),
		       count(DISTINCT target_email), count(*),
		       min(created_at), max(created_at),
		       EXISTS (SELECT 1 FROM ip_block b WHERE b.ip = audit_log.ip)
		FROM audit_log
		WHERE created_at >= now() - $1::interval
		  AND action = $2 AND ip IS NOT NULL AND target_email IS NOT NULL
		GROUP BY ip
		HAVING count(DISTINCT target_email) >= $3
		ORDER BY count(DISTINCT target_email) DESC, max(created_at) DESC
		LIMIT $4`, iv, AuditLoginFailed, th.SprayAccounts, anomalyQueryLimit)
	if err != nil {
		return nil, fmt.Errorf("anomaly spray: %w", err)
	}
	defer spray.Close()
	for spray.Next() {
		a := Anomaly{Kind: AnomalyKindSpray}
		if err := spray.Scan(&a.IP, &a.IPTrusted, &a.DistinctAccounts, &a.Failures,
			&a.FirstSeen, &a.LastSeen, &a.Blocked); err != nil {
			return nil, fmt.Errorf("scan anomaly spray: %w", err)
		}
		a.Severity = anomalySeverity(a.DistinctAccounts, th.SprayAccounts)
		out = append(out, a)
	}
	if err := spray.Err(); err != nil {
		return nil, fmt.Errorf("anomaly spray: %w", err)
	}

	// Grouped per (origin, account) first and collapsed per origin after: the
	// threshold is about ONE account, so an origin that failed twice against
	// each of ten mailboxes is a spray and must not add up to a hammer.
	hammer, err := r.pool.Query(ctx, `
		WITH per_account AS (
			SELECT ip, bool_or(ip_trusted) AS trusted, count(*) AS failures,
			       min(created_at) AS first_at, max(created_at) AS last_at
			FROM audit_log
			WHERE created_at >= now() - $1::interval
			  AND action = $2 AND ip IS NOT NULL AND target_email IS NOT NULL
			GROUP BY ip, target_email
			HAVING count(*) >= $3
		)
		SELECT host(ip), bool_or(trusted), count(*), max(failures),
		       min(first_at), max(last_at),
		       EXISTS (SELECT 1 FROM ip_block b WHERE b.ip = per_account.ip)
		FROM per_account
		GROUP BY ip
		ORDER BY max(failures) DESC, max(last_at) DESC
		LIMIT $4`, iv, AuditLoginFailed, th.HammerFailures, anomalyQueryLimit)
	if err != nil {
		return nil, fmt.Errorf("anomaly hammer: %w", err)
	}
	defer hammer.Close()
	for hammer.Next() {
		a := Anomaly{Kind: AnomalyKindHammer}
		if err := hammer.Scan(&a.IP, &a.IPTrusted, &a.DistinctAccounts, &a.Failures,
			&a.FirstSeen, &a.LastSeen, &a.Blocked); err != nil {
			return nil, fmt.Errorf("scan anomaly hammer: %w", err)
		}
		a.Severity = anomalySeverity(a.Failures, th.HammerFailures)
		out = append(out, a)
	}
	if err := hammer.Err(); err != nil {
		return nil, fmt.Errorf("anomaly hammer: %w", err)
	}

	// One is enough, and it is always critical. The other two rules infer an
	// attack from a pattern; this one reports that a limiter already stopped
	// answering the origin — there is nothing left to be unsure about.
	throttle, err := r.pool.Query(ctx, `
		SELECT host(ip), bool_or(ip_trusted), count(*),
		       min(created_at), max(created_at),
		       EXISTS (SELECT 1 FROM ip_block b WHERE b.ip = audit_log.ip)
		FROM audit_log
		WHERE created_at >= now() - $1::interval AND action = $2 AND ip IS NOT NULL
		GROUP BY ip
		ORDER BY count(*) DESC, max(created_at) DESC
		LIMIT $3`, iv, AuditRateLimited, anomalyQueryLimit)
	if err != nil {
		return nil, fmt.Errorf("anomaly throttle: %w", err)
	}
	defer throttle.Close()
	for throttle.Next() {
		a := Anomaly{Kind: AnomalyKindThrottle, Severity: AnomalySeverityCritical}
		if err := throttle.Scan(&a.IP, &a.IPTrusted, &a.Throttles,
			&a.FirstSeen, &a.LastSeen, &a.Blocked); err != nil {
			return nil, fmt.Errorf("scan anomaly throttle: %w", err)
		}
		out = append(out, a)
	}
	if err := throttle.Err(); err != nil {
		return nil, fmt.Errorf("anomaly throttle: %w", err)
	}
	return out, nil
}

// AbuseObservedDays is the period the recommendation is measured over. A month
// is long enough to contain the instance's real peak — a monthly import, an
// end-of-quarter burst — and short enough that a limit set today is not being
// justified by traffic from a different deployment.
const AbuseObservedDays = 30

// AbuseObserved is what the instance ACTUALLY did, so tuning a limit is an
// informed act rather than an automatic one.
//
// This is the deliberate middle ground SDD §6 asks for: the screen shows the
// owner the numbers their own traffic produced, and the owner decides. Nothing
// here changes a limit. A field reads 0 when the trail holds no such row, and
// the screen renders "no data" — an absent measurement must not be presented
// as a measured zero, which would read as "nobody has ever written anything"
// and justify a floor.
type AbuseObserved struct {
	MaxDistinctAccountsPerIP int `json:"max_distinct_accounts_per_ip"`
	MaxFailuresPerAccount    int `json:"max_failures_per_account"`
	PeakWritesPerMinute      int `json:"peak_writes_per_minute"`
	Days                     int `json:"days"`
}

// AbuseObservedSince measures the three numbers the enforcement knobs are set
// against, over AbuseObservedDays.
func (r *Repository) AbuseObservedSince(ctx context.Context) (AbuseObserved, error) {
	out := AbuseObserved{Days: AbuseObservedDays}
	iv := intervalArg(AbuseObservedDays * 24 * time.Hour)

	if err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(max(n), 0) FROM (
			SELECT count(DISTINCT target_email) AS n FROM audit_log
			WHERE created_at >= now() - $1::interval
			  AND action = $2 AND ip IS NOT NULL AND target_email IS NOT NULL
			GROUP BY ip) s`, iv, AuditLoginFailed).
		Scan(&out.MaxDistinctAccountsPerIP); err != nil {
		return out, fmt.Errorf("observed distinct accounts: %w", err)
	}
	if err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(max(n), 0) FROM (
			SELECT count(*) AS n FROM audit_log
			WHERE created_at >= now() - $1::interval
			  AND action = $2 AND target_email IS NOT NULL
			GROUP BY target_email) s`, iv, AuditLoginFailed).
		Scan(&out.MaxFailuresPerAccount); err != nil {
		return out, fmt.Errorf("observed failures per account: %w", err)
	}
	// Per PRINCIPAL and per minute, because that is the shape of the quota the
	// number informs: a peak measured across the whole instance would let one
	// busy afternoon of five people justify a ceiling any one of them could
	// then hold the pool with.
	if err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(max(n), 0) FROM (
			SELECT count(*) AS n FROM audit_log
			WHERE created_at >= now() - $1::interval
			  AND action = ANY($2::text[]) AND actor_id IS NOT NULL
			GROUP BY actor_id, date_trunc('minute', created_at)) s`,
		iv, ContentActions()).Scan(&out.PeakWritesPerMinute); err != nil {
		return out, fmt.Errorf("observed peak writes: %w", err)
	}
	return out, nil
}
