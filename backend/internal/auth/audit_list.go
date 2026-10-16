package auth

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"foldex/internal/auth/auditpkg"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/pgerr"
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
// The feed now answers "what did I do" rather than only "what content did I
// change": logins are included (recognizing an unrecognized device is the one
// security action this screen can drive), and link clicks are merged from
// click_log so the trail reads like the user's session history. Administrative
// acts performed ON the account (role changes, disables) stay excluded — they
// are not activity BY the account.
func (r *Repository) ListOwnActivity(ctx context.Context, uid authctx.UserID, beforeID int64, beforeAt *time.Time, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > maxAuditPageSize {
		limit = 50
	}
	args := []any{int64(uid), auditpkg.OwnActivityActions()}
	cursor := ""
	// The feed merges two id spaces (audit ids positive, click ids negated),
	// so the keyset cursor is the composite (created_at, feed id) of the
	// previous page's last row — one total order both sources can compare
	// against, with no dereference of the cursor row (a deleted cursor row
	// or another tenant's id can neither break paging nor leak timing).
	// A bare beforeID (stale client) keeps the legacy id-only bound.
	if beforeAt != nil {
		args = append(args, *beforeAt, beforeID)
		cursor = fmt.Sprintf(" AND (a.created_at, a.id) < ($%d, $%d)", len(args)-1, len(args))
	} else if beforeID > 0 {
		args = append(args, beforeID)
		cursor = fmt.Sprintf(" AND a.id < $%d", len(args))
	} else if beforeID < 0 {
		args = append(args, -beforeID)
		cursor = fmt.Sprintf(` AND a.created_at < (
			SELECT clicked_at FROM click_log WHERE id = $%d AND user_id = $1)`, len(args))
	}
	args = append(args, limit)
	limitArg := len(args)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT a.id, a.action, a.detail, host(a.ip), a.ip_trusted, a.user_agent,
		       a.entity_kind, a.entity_id, a.subject, a.created_at
		FROM audit_log a
		WHERE a.actor_id = $1 AND a.action = ANY($2::text[])%s
		ORDER BY a.created_at DESC, a.id DESC LIMIT $%d`, cursor, limitArg), args...)
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
		e.Category = auditpkg.AuditCategory(e.Action)
		e.Severity = auditpkg.AuditSeverity(e.Action, 0)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list own activity: %w", err)
	}

	// Clicks live in their own table, not audit_log. Merged here rather than
	// in a SQL UNION so the audit query stays self-contained in test schemas
	// that don't carry the link table. Only that one schema gap (42P01)
	// degrades the feed; a real SQL failure must surface — swallowing every
	// error here is what hid a nonexistent-table join in production.
	clicks, clickErr := r.ownClicks(ctx, uid, beforeID, beforeAt, limit)
	if clickErr != nil && !pgerr.IsUndefinedTable(clickErr) {
		return nil, fmt.Errorf("list own activity clicks: %w", clickErr)
	} else if clickErr == nil {
		out = append(out, clicks...)
		sort.Slice(out, func(i, j int) bool {
			if out[i].CreatedAt.Equal(out[j].CreatedAt) {
				return out[i].ID > out[j].ID
			}
			return out[i].CreatedAt.After(out[j].CreatedAt)
		})
		if len(out) > limit {
			out = out[:limit]
		}
	}
	return out, nil
}

// ownClicks reads the caller's link clicks, mapped onto the AuditEntry shape
// so the feed is one homogeneous list downstream. Click rows carry their
// NEGATED click_log id: the merged feed's cursor is (created_at, feed id),
// and a bare click id would be indistinguishable from an audit_log id (and
// collide with it as a React key). With the composite cursor the bound is
// the same tuple compare in feed-id space, (clicked_at, -id) < (at, feedID);
// the legacy id-only cursor falls back to per-source bounds via
// owner-scoped lookups of the cursor row.
func (r *Repository) ownClicks(ctx context.Context, uid authctx.UserID, beforeID int64, beforeAt *time.Time, limit int) ([]AuditEntry, error) {
	args := []any{int64(uid)}
	window := ""
	if beforeAt != nil {
		args = append(args, *beforeAt, beforeID)
		window = fmt.Sprintf(" AND (c.clicked_at, -c.id) < ($%d, $%d)", len(args)-1, len(args))
	} else if beforeID > 0 {
		args = append(args, beforeID)
		window = fmt.Sprintf(` AND c.clicked_at < (
			SELECT created_at FROM audit_log WHERE id = $%d AND actor_id = $1)`, len(args))
	} else if beforeID < 0 {
		args = append(args, -beforeID)
		window = fmt.Sprintf(` AND (c.clicked_at, c.id) < (
			SELECT clicked_at, id FROM click_log WHERE id = $%d AND user_id = $1)`, len(args))
	}
	args = append(args, limit)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT c.id, c.entity_id, COALESCE(l.title, ''), c.clicked_at
		FROM click_log c
		LEFT JOIN link l ON l.id = c.entity_id AND l.user_id = $1
		WHERE c.user_id = $1 AND c.entity_kind = 'link'%s
		ORDER BY c.clicked_at DESC, c.id ASC LIMIT $%d`, window, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("own clicks: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		e.Action = auditpkg.AuditLinkClicked
		e.Category = auditpkg.CategoryContent
		e.Severity = auditpkg.SeverityInfo
		kind := "link"
		e.EntityKind = &kind
		var subject string
		var clickID int64
		if err := rows.Scan(&clickID, &e.EntityID, &subject, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan own clicks: %w", err)
		}
		e.ID = -clickID
		e.Subject = &subject
		out = append(out, e)
	}
	return out, rows.Err()
}
