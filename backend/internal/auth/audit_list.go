package auth

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AuditFilter narrows one page of the trail.
//
// Every field is optional except the window, which Normalize supplies: an
// unbounded query is one backward scan of a table whose retention is ninety
// days and whose bulk is failed logins, and the search predicate below is not
// indexable. Bounding by time first is what keeps a typed filter from reading
// the whole trail.
type AuditFilter struct {
	Action   string
	Category string
	Search   string
	Since    time.Time
	BeforeID int64
	Limit    int
	// Ascending flips the page to oldest-first. The CURSOR flips with it —
	// `id > $n` instead of `id < $n` — because a keyset cursor means "continue
	// past this row", and which side that is depends on the direction. Reversing
	// a descending page in the client would reorder only the fifty rows already
	// loaded, so "oldest first" would show the oldest of the NEWEST fifty: a
	// control that looks like it works and answers a different question.
	Ascending bool
}

// AuditWindowCeiling bounds a filter that names no window at all.
//
// Every HTTP caller goes through parseAuditFilter, which resolves one of three
// named windows and always sets Since — so this is the floor under a DIRECT
// repository call (a test, a future job), not the screen's default. It is the
// widest of the three rather than the narrowest: a caller that named nothing
// asked for "the trail", and silently handing back one day of it would be a
// quieter wrong answer than a slower right one. Retention caps it at ninety
// days regardless.
const AuditWindowCeiling = 30 * 24 * time.Hour

// Normalize fills the defaults and clamps the bounds.
func (f AuditFilter) Normalize(now time.Time) AuditFilter {
	if f.Limit <= 0 || f.Limit > maxAuditPageSize {
		f.Limit = 50
	}
	if f.Since.IsZero() {
		f.Since = now.Add(-AuditWindowCeiling)
	}
	// Trimmed and bounded here; the wildcards are escaped at query time by
	// likeEscape, which is where the pattern is actually built.
	f.Search = strings.TrimSpace(f.Search)
	if len(f.Search) > maxAuditSearch {
		f.Search = f.Search[:maxAuditSearch]
	}
	return f
}

// maxAuditSearch bounds the typed filter. Long enough for an address or a full
// e-mail, short enough that the LIKE cannot be handed a pathological pattern.
const maxAuditSearch = 128

// likeEscape makes a user-typed term a literal substring for LIKE.
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// adminProjection is the administrative trail's columns.
//
// `subject` is ABSENT, and that absence is the enforcement of INV-045 — not a
// convention, not a filter applied in Go after the row arrived. A content
// event's human label is the caller's private content, and the only query that
// may read it is the owner's own feed below. actor_email is blanked in SQL for
// the same reason and by the same means: for a content row the administrator
// gets the opaque actor id and nothing that names the person.
//
// TestAdminProjectionNeverSelectsSubject reads this constant and fails if the
// column is ever added back.
const adminProjection = `
	SELECT id, action,
	       CASE WHEN action = ANY($1::text[]) THEN NULL ELSE actor_email END,
	       actor_id,
	       CASE WHEN action = ANY($1::text[]) THEN NULL ELSE target_email END,
	       CASE WHEN action = ANY($1::text[]) THEN NULL ELSE detail END,
	       host(ip), ip_trusted, user_agent, created_at
	FROM audit_log`

// ListAudit returns one page of the administrative trail, newest first.
//
// Keyset pagination rather than OFFSET: the trail grows at its head, so an
// offset-paged second page would re-show rows the first page already displayed
// as soon as anything was written between the two requests.
//
// The predicates are ASSEMBLED from fixed fragments rather than written out as
// one static string per combination. The original had four hand-written
// queries because an OR against an empty-string parameter is not sargable
// under the generic plan pgx ends up with, and that reasoning still holds — so
// each fragment below is still a plain indexable comparison, and each is either
// present or absent rather than disabled by a sentinel value. What changed is
// the arithmetic: five optional filters is thirty-two static strings, and
// thirty-two hand-maintained near-copies is where a typo lives forever. No
// caller input reaches the SQL text; only $n placeholders do.
//
// ORDER BY is id alone, matching the cursor. id is monotonic with created_at
// (both come from the same INSERT), so ordering by it lets `id < $n` start the
// scan instead of filtering after it.
func (r *Repository) ListAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	f = f.Normalize(time.Now())
	content := ContentActions()
	args := []any{content}
	var where []string

	add := func(fragment string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(fragment, len(args)))
	}
	add("created_at >= $%d", f.Since)
	if f.Action != "" {
		add("action = $%d", f.Action)
	}
	switch f.Category {
	case CategoryContent:
		add("action = ANY($%d::text[])", content)
	case CategoryIdentity:
		add("NOT (action = ANY($%d::text[]))", content)
	}
	if f.BeforeID > 0 {
		if f.Ascending {
			add("id > $%d", f.BeforeID)
		} else {
			add("id < $%d", f.BeforeID)
		}
	}
	if f.Search != "" {
		// The e-mail arms are gated on the row NOT being a content row, and
		// that gate is the whole pseudonym.
		//
		// Without it the filter is a de-anonymisation oracle: an administrator
		// asks for `?category=content&q=alice@example.com`, the WHERE matches
		// `actor_email` on rows whose projection blanks it, and every
		// `actor_ref` that comes back is provably Alice's. The column would be
		// withheld from the OUTPUT while the INPUT still selected on it — the
		// projection would read as the enforcement of INV-045 and enforce
		// nothing.
		//
		// host(ip) rather than ip::text so a search for "189.42" matches the
		// address as it is DISPLAYED. ip::text on an inet carries the mask for
		// anything that is not a single host, and the screen never shows one.
		args = append(args, likeEscape(f.Search))
		n := len(args)
		args = append(args, content)
		c := len(args)
		where = append(where, fmt.Sprintf(
			`((NOT (action = ANY($%d::text[])) AND (actor_email ILIKE $%d ESCAPE '\'`+
				` OR target_email ILIKE $%d ESCAPE '\'))`+
				` OR host(ip) LIKE $%d ESCAPE '\' OR action ILIKE $%d ESCAPE '\')`,
			c, n, n, n, n))
	}
	args = append(args, f.Limit)

	direction := "DESC"
	if f.Ascending {
		direction = "ASC"
	}
	query := adminProjection + " WHERE " + strings.Join(where, " AND ") +
		fmt.Sprintf(" ORDER BY id %s LIMIT $%d", direction, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Action, &e.ActorEmail, &e.ActorRef, &e.TargetEmail,
			&e.Detail, &e.IP, &e.IPTrusted, &e.UserAgent, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		e.Category = AuditCategory(e.Action)
		// A floor, not the final word: the handler raises login.failed to
		// critical for addresses inside a burst, which needs per-address counts
		// this loop does not have. Setting it here means a caller that skips
		// that step still gets a classified row rather than an empty string the
		// UI would render as an unstyled badge.
		e.Severity = AuditSeverity(e.Action, 0)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	return out, nil
}

// ListOwnActivity returns the caller's own content events, WITH their subjects.
//
// This is the other half of the read split. The administrative trail above
// withholds the subject from everyone; here the caller is the row's actor, so
// the label is their own content and there is nothing to withhold. The scope is
// the WHERE clause — actor_id = the caller, and only content actions — which is
// INV-001's explicit-uid rule applied to a table that is otherwise instance-wide.
//
// Identity events are deliberately excluded even though some of them name this
// same account. "Your role was changed to admin" is an administrative act
// performed ON the account, not activity BY it, and mixing the two would make a
// feed titled "my activity" report things its owner did not do.
func (r *Repository) ListOwnActivity(ctx context.Context, uid int64, beforeID int64, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > maxAuditPageSize {
		limit = 50
	}
	args := []any{uid, ContentActions()}
	cursor := ""
	if beforeID > 0 {
		args = append(args, beforeID)
		cursor = fmt.Sprintf(" AND id < $%d", len(args))
	}
	args = append(args, limit)
	limitArg := len(args)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, action, detail, host(ip), ip_trusted, user_agent,
		       entity_kind, entity_id, subject, created_at
		FROM audit_log
		WHERE actor_id = $1 AND action = ANY($2::text[])%s
		ORDER BY id DESC LIMIT $%d`, cursor, limitArg), args...)
	if err != nil {
		return nil, fmt.Errorf("list own activity: %w", err)
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Action, &e.Detail, &e.IP, &e.IPTrusted,
			&e.UserAgent, &e.EntityKind, &e.EntityID, &e.Subject, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan own activity: %w", err)
		}
		e.Category = CategoryContent
		e.Severity = SeverityInfo
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list own activity: %w", err)
	}
	return out, nil
}
