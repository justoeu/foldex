package auth

import (
	"context"
	"fmt"
	"time"

	"foldex/internal/auth/anomaly"
)

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
		a.Severity = anomaly.SeverityFor(a.DistinctAccounts, th.SprayAccounts)
		out = append(out, a)
	}
	if err := spray.Err(); err != nil {
		return nil, fmt.Errorf("anomaly spray: %w", err)
	}

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
		a.Severity = anomaly.SeverityFor(a.Failures, th.HammerFailures)
		out = append(out, a)
	}
	if err := hammer.Err(); err != nil {
		return nil, fmt.Errorf("anomaly hammer: %w", err)
	}

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

const AbuseObservedDays = 30

type AbuseObserved struct {
	MaxDistinctAccountsPerIP int `json:"max_distinct_accounts_per_ip"`
	MaxFailuresPerAccount    int `json:"max_failures_per_account"`
	PeakWritesPerMinute      int `json:"peak_writes_per_minute"`
	Days                     int `json:"days"`
}

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
