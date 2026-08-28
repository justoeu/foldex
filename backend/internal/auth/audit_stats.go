package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// RiskWindow is how close together failures have to be to count as one burst.
//
// Fifteen minutes matches the lockout attemptlimit applies, so the card and the
// limiter describe the same event. A longer window would collect a week of
// unrelated typos into something the screen calls an attack.
const RiskWindow = 15 * time.Minute

// AuditStats is everything the audit screen's header needs, for one window.
type AuditStats struct {
	Totals       AuditTotals       `json:"totals"`
	Days         []AuditDayBucket  `json:"days"`
	Distribution []AuditActionStat `json:"distribution"`
	Actors       []AuditActorStat  `json:"actors"`
	Origins      []AuditOriginStat `json:"origins"`
	Risk         *AuditRisk        `json:"risk"`
}

// AuditTotals carries each headline number beside the same number for the
// PRECEDING window of equal length, so the screen can render a delta without a
// second round trip and without inventing the comparison client-side.
type AuditTotals struct {
	Events        int64 `json:"events"`
	EventsPrev    int64 `json:"events_prev"`
	Failures      int64 `json:"failures"`
	FailuresPrev  int64 `json:"failures_prev"`
	AccessChanges int64 `json:"access_changes"`
	AccessPrev    int64 `json:"access_changes_prev"`
	Actors        int64 `json:"actors"`
	ActiveUsers   int64 `json:"active_users"`
}

// AuditDayBucket is one column of the "events per day" chart. The four series
// are disjoint and together cover the vocabulary, so their sum is the day's
// total and a stacked column is honest.
type AuditDayBucket struct {
	Day     time.Time `json:"day"`
	Logins  int64     `json:"logins"`
	Failed  int64     `json:"failed"`
	Admin   int64     `json:"admin"`
	Content int64     `json:"content"`
}

type AuditActionStat struct {
	Action   string `json:"action"`
	Category string `json:"category"`
	Count    int64  `json:"count"`
}

// AuditActorStat is one row of "most active actors".
//
// It carries the e-mail, which the per-row content projection deliberately
// withholds — and the two are consistent because this row does NOT carry the
// actor id. An administrator learns that an account was active, and cannot join
// that back to the pseudonymous `usuário #N` on a content line.
//
// The pseudonym is a defence against INCIDENTAL exposure — names sitting beside
// content activity on a screen an administrator reads for other reasons — and
// not against a determined one. The administration surface hands out user ids
// elsewhere (it must, to address a user for a PATCH), and in a self-hosted
// instance an administrator with shell access holds the database outright. The
// ADR says so in those words; anything stronger here would be decoration.
type AuditActorStat struct {
	Email string `json:"email"`
	Role  string `json:"role"`
	Count int64  `json:"count"`
}

// AuditOriginStat is one row of "origins of access".
type AuditOriginStat struct {
	IP        string    `json:"ip"`
	Trusted   bool      `json:"trusted"`
	UserAgent *string   `json:"user_agent"`
	Count     int64     `json:"count"`
	Failures  int64     `json:"failures"`
	LastSeen  time.Time `json:"last_seen"`
	Blocked   bool      `json:"blocked"`
}

// AuditRisk is the burst the screen leads with, or nil when there is none.
type AuditRisk struct {
	IP       string    `json:"ip"`
	Failures int64     `json:"failures"`
	Targets  int64     `json:"targets"`
	FirstAt  time.Time `json:"first_at"`
	LastAt   time.Time `json:"last_at"`
	Blocked  bool      `json:"blocked"`
}

// AuditStatsSince computes the header for [since, now].
//
// Six queries rather than one: the shapes are genuinely different aggregations
// and a single statement would be a pile of FILTER clauses over a join nobody
// could read. It is a fixed six regardless of how much data comes back — the
// count does not grow with rows, so this is not the N+1 the performance rule is
// about — and it serves an administrator opening a screen, not a hot path.
func (r *Repository) AuditStatsSince(ctx context.Context, since time.Time) (AuditStats, error) {
	var out AuditStats
	content := ContentActions()
	// The preceding window of equal length, for the deltas.
	span := time.Since(since)
	prev := since.Add(-span)

	if err := r.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE created_at >= $1),
			count(*) FILTER (WHERE created_at >= $2 AND created_at < $1),
			count(*) FILTER (WHERE created_at >= $1 AND action = 'login.failed'),
			count(*) FILTER (WHERE created_at >= $2 AND created_at < $1 AND action = 'login.failed'),
			count(*) FILTER (WHERE created_at >= $1 AND action = ANY($3::text[])),
			count(*) FILTER (WHERE created_at >= $2 AND created_at < $1 AND action = ANY($3::text[])),
			count(DISTINCT actor_id) FILTER (WHERE created_at >= $1)
		FROM audit_log WHERE created_at >= $2`,
		since, prev, accessChangeActions).Scan(
		&out.Totals.Events, &out.Totals.EventsPrev,
		&out.Totals.Failures, &out.Totals.FailuresPrev,
		&out.Totals.AccessChanges, &out.Totals.AccessPrev,
		&out.Totals.Actors); err != nil {
		return out, fmt.Errorf("audit totals: %w", err)
	}
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM app_user WHERE status = 'active'`).Scan(&out.Totals.ActiveUsers); err != nil {
		return out, fmt.Errorf("audit active users: %w", err)
	}

	// generate_series, not a GROUP BY alone: a day on which nothing happened
	// has no rows to group, and a chart that silently drops empty days
	// compresses a quiet week into a busy-looking one.
	//
	// The series is built with date_trunc over timestamptz, and its upper bound
	// is the DATABASE's now() — not the client's, and not a ::date cast of
	// either. An earlier draft used `generate_series($1::date, $2::date, ...)`,
	// which made Postgres infer the parameters as `date`: the driver then
	// encoded the process's LOCAL calendar day, while created_at is compared in
	// the session's zone. On a server running at UTC-3 the last bucket ended
	// where the day began, so every event of the current day fell past the end
	// of the chart and the busiest column read zero. The failure is silent, it
	// only appears away from UTC, and it looks like "a quiet day" — so the
	// bound stays inside SQL, where the clock and the rows are the same clock.
	days, err := r.pool.Query(ctx, `
		SELECT d,
		       count(a.id) FILTER (WHERE a.action = 'login.succeeded'),
		       count(a.id) FILTER (WHERE a.action = 'login.failed'),
		       count(a.id) FILTER (WHERE a.action NOT IN ('login.succeeded','login.failed')
		                             AND NOT (a.action = ANY($2::text[]))),
		       count(a.id) FILTER (WHERE a.action = ANY($2::text[]))
		FROM generate_series(date_trunc('day', $1::timestamptz),
		                     date_trunc('day', now()), '1 day') AS d
		LEFT JOIN audit_log a ON a.created_at >= d AND a.created_at < d + interval '1 day'
		GROUP BY d ORDER BY d`, since, content)
	if err != nil {
		return out, fmt.Errorf("audit days: %w", err)
	}
	defer days.Close()
	out.Days = []AuditDayBucket{}
	for days.Next() {
		var b AuditDayBucket
		if err := days.Scan(&b.Day, &b.Logins, &b.Failed, &b.Admin, &b.Content); err != nil {
			return out, fmt.Errorf("scan audit day: %w", err)
		}
		out.Days = append(out.Days, b)
	}
	if err := days.Err(); err != nil {
		return out, fmt.Errorf("audit days: %w", err)
	}

	dist, err := r.pool.Query(ctx, `
		SELECT action, count(*) FROM audit_log
		WHERE created_at >= $1 GROUP BY action ORDER BY count(*) DESC, action`, since)
	if err != nil {
		return out, fmt.Errorf("audit distribution: %w", err)
	}
	defer dist.Close()
	out.Distribution = []AuditActionStat{}
	for dist.Next() {
		var s AuditActionStat
		if err := dist.Scan(&s.Action, &s.Count); err != nil {
			return out, fmt.Errorf("scan audit distribution: %w", err)
		}
		s.Category = AuditCategory(s.Action)
		out.Distribution = append(out.Distribution, s)
	}
	if err := dist.Err(); err != nil {
		return out, fmt.Errorf("audit distribution: %w", err)
	}

	// Joined to app_user rather than reading the denormalized actor_email: this
	// card names LIVE accounts and shows the role each holds now, and a deleted
	// account is not "most active", it is gone. The trail keeps its own copy of
	// the address for the rows themselves, which is what lets those outlive the
	// account (000033's rationale) — a different question from this one.
	actors, err := r.pool.Query(ctx, `
		SELECT u.email, u.role, count(*) FROM audit_log a
		JOIN app_user u ON u.id = a.actor_id
		WHERE a.created_at >= $1
		GROUP BY u.email, u.role ORDER BY count(*) DESC, u.email LIMIT 5`, since)
	if err != nil {
		return out, fmt.Errorf("audit actors: %w", err)
	}
	defer actors.Close()
	out.Actors = []AuditActorStat{}
	for actors.Next() {
		var s AuditActorStat
		if err := actors.Scan(&s.Email, &s.Role, &s.Count); err != nil {
			return out, fmt.Errorf("scan audit actor: %w", err)
		}
		out.Actors = append(out.Actors, s)
	}
	if err := actors.Err(); err != nil {
		return out, fmt.Errorf("audit actors: %w", err)
	}

	// mode() for the user agent: one address is usually one device, and the
	// alternative — the LATEST agent — would let a single odd request relabel a
	// row that a hundred ordinary ones defined.
	origins, err := r.pool.Query(ctx, `
		SELECT host(a.ip), bool_or(a.ip_trusted),
		       mode() WITHIN GROUP (ORDER BY a.user_agent),
		       count(*), count(*) FILTER (WHERE a.action = 'login.failed'),
		       max(a.created_at),
		       EXISTS (SELECT 1 FROM ip_block b WHERE b.ip = a.ip)
		FROM audit_log a
		WHERE a.created_at >= $1 AND a.ip IS NOT NULL
		GROUP BY a.ip ORDER BY count(*) DESC, a.ip LIMIT 6`, since)
	if err != nil {
		return out, fmt.Errorf("audit origins: %w", err)
	}
	defer origins.Close()
	out.Origins = []AuditOriginStat{}
	for origins.Next() {
		var s AuditOriginStat
		if err := origins.Scan(&s.IP, &s.Trusted, &s.UserAgent, &s.Count,
			&s.Failures, &s.LastSeen, &s.Blocked); err != nil {
			return out, fmt.Errorf("scan audit origin: %w", err)
		}
		out.Origins = append(out.Origins, s)
	}
	if err := origins.Err(); err != nil {
		return out, fmt.Errorf("audit origins: %w", err)
	}

	risk, err := r.worstBurst(ctx, since)
	if err != nil {
		return out, err
	}
	out.Risk = risk
	return out, nil
}

// accessChangeActions is the "changes of access" metric: the identity events
// that move someone's authority, as opposed to merely reporting a sign-in.
var accessChangeActions = []string{
	AuditRoleChanged, AuditStatusChanged, AuditUserCreated, AuditUserDeleted,
	AuditOwnershipMoved, AuditInviteCreated, AuditInviteRevoked,
	AuditRolePermissions, AuditPolicyChanged, AuditIPBlocked, AuditIPUnblocked,
}

// worstBurst finds the address with the most failures inside one RiskWindow.
//
// The window is measured between the FIRST and LAST failure of the address
// inside the period, not by bucketing the clock: five failures at 09:58 and
// 10:02 are one burst, and a fixed quarter-hour grid would split them into two
// of two and three and report neither.
func (r *Repository) worstBurst(ctx context.Context, since time.Time) (*AuditRisk, error) {
	var risk AuditRisk
	err := r.pool.QueryRow(ctx, `
		SELECT host(ip), count(*), count(DISTINCT target_email),
		       min(created_at), max(created_at),
		       EXISTS (SELECT 1 FROM ip_block b WHERE b.ip = audit_log.ip)
		FROM audit_log
		WHERE created_at >= $1 AND action = 'login.failed' AND ip IS NOT NULL
		GROUP BY ip
		HAVING count(*) >= $2 AND max(created_at) - min(created_at) <= $3::interval
		ORDER BY count(*) DESC, max(created_at) DESC
		LIMIT 1`, since, RiskBurstThreshold, intervalArg(RiskWindow)).
		Scan(&risk.IP, &risk.Failures, &risk.Targets, &risk.FirstAt, &risk.LastAt, &risk.Blocked)
	if err != nil {
		// No burst is the ordinary case, not a failure: the card renders its
		// quiet state and the screen loads.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("audit risk: %w", err)
	}
	return &risk, nil
}

// FailureBursts returns, for the window, how many failures each address
// produced — the input AuditSeverity needs to tell a typo from an attack.
//
// One query for the whole page rather than one per row: severity is a property
// of the address's behaviour, not of the single row being classified.
func (r *Repository) FailureBursts(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT host(ip), count(*) FROM audit_log
		WHERE created_at >= $1 AND action = 'login.failed' AND ip IS NOT NULL
		GROUP BY ip`, since)
	if err != nil {
		return nil, fmt.Errorf("failure bursts: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var ip string
		var n int
		if err := rows.Scan(&ip, &n); err != nil {
			return nil, fmt.Errorf("scan failure burst: %w", err)
		}
		out[ip] = n
	}
	return out, rows.Err()
}
