package auth

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"foldex/internal/pkg/authctx"
)

// Audit actions. Closed vocabulary: the administration screen groups and
// translates by these, so a free-form string invented at a call site would
// render as an untranslated key nobody planned for.
const (
	AuditLoginSucceeded   = "login.succeeded"
	AuditLoginFailed      = "login.failed"
	AuditRoleChanged      = "user.role_changed"
	AuditStatusChanged    = "user.status_changed"
	AuditUserCreated      = "user.created"
	AuditUserDeleted      = "user.deleted"
	AuditOwnershipMoved   = "instance.ownership_transferred"
	AuditInviteCreated    = "invite.created"
	AuditInviteRevoked    = "invite.revoked"
	AuditSessionsRevoked  = "user.sessions_revoked"
	AuditPasswordRecovery = "user.password_recovery_sent"
	AuditPolicyChanged    = "policy.changed"
	// AuditEmailChanged records the address MOVING, at the moment the
	// confirmation link is consumed — never when it is requested. A request
	// that nobody confirms changed nothing, and an entry for it would make the
	// trail claim an account moved when it did not.
	AuditEmailChanged = "user.email_changed"
)

// AuditEntry is one row of the trail as the API returns it.
type AuditEntry struct {
	ID          int64     `json:"id"`
	Action      string    `json:"action"`
	ActorEmail  *string   `json:"actor_email"`
	TargetEmail *string   `json:"target_email"`
	Detail      *string   `json:"detail"`
	CreatedAt   time.Time `json:"created_at"`
}

// AuditRecord is what a call site hands to Audit.
//
// E-mails are captured at write time, not joined at read time: the whole point
// of ON DELETE SET NULL on the ids is that the trail outlives the accounts, and
// a join would then render every historical row as blank.
type AuditRecord struct {
	Action      string
	ActorID     *authctx.UserID
	ActorEmail  string
	TargetID    *authctx.UserID
	TargetEmail string
	Detail      string
}

// Audit appends one entry.
//
// It returns an error the caller is expected to LOG rather than propagate. An
// audit write must never turn a successful administrative action into a failure
// response: the action already committed, and answering 500 would invite a
// retry that performs it twice. The trail losing a row is the lesser failure,
// and it is a visible one — the caller logs it.
//
// Detail must never carry a credential. The root log handler redacts by
// attribute key, which cannot help a value already concatenated into a string,
// so this is a call-site obligation — hence the closed action vocabulary above,
// which keeps the number of call sites small enough to audit by reading.
func (r *Repository) Audit(ctx context.Context, rec AuditRecord) error {
	var actor, target *int64
	if rec.ActorID != nil {
		id := int64(*rec.ActorID)
		actor = &id
	}
	if rec.TargetID != nil {
		id := int64(*rec.TargetID)
		target = &id
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO audit_log (action, actor_id, actor_email, target_id, target_email, detail)
		VALUES ($1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), NULLIF($6, ''))`,
		rec.Action, actor, rec.ActorEmail, target, rec.TargetEmail, truncateDetail(rec.Detail))
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}

// truncateDetail keeps a long value from tripping audit_log's CHECK on `detail`
// and failing the insert. Losing the tail of a detail beats losing the entry.
func truncateDetail(s string) string { return truncateTo(s, maxAuditDetail) }

// maxAuditDetail and maxAuditEmail mirror the CHECK constraints in migration
// 000033. Named here so the pair moves together: exceeding either would abort
// the INSERT, and an audit failure is logged rather than propagated — so the
// entry would simply vanish.
const (
	maxAuditDetail = 512
	maxAuditEmail  = MaxEmailLen
	// maxAuditPageSize bounds one page of the trail. Shared with the handler so
	// the range it validates and the range this clamps cannot drift apart.
	maxAuditPageSize = 200
)

func truncateTo(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Cut on a rune boundary: a detail split mid-sequence would render as a
	// replacement character in the UI and, worse, is not valid UTF-8 for
	// Postgres's text type.
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// ListAudit returns the newest entries, optionally narrowed to one action.
//
// Keyset pagination rather than OFFSET: the trail grows at its head, so an
// offset-paged second page would re-show rows the first page already displayed
// as soon as anything was written between the two requests.
//
// The predicates are BRANCHED in Go rather than expressed as an OR against an
// empty-string parameter. pgx caches statements, so after a few executions Postgres
// builds a GENERIC plan — and an OR against the same parameter is not sargable
// there, so it cannot become an index condition. The filter would degrade to a
// heap filter over a backward scan of the whole table, which on a trail
// dominated by login.failed means reading hundreds of thousands of rows to fill
// one 50-row page of a rare action. Four static strings keep every path on an
// index; none of them interpolates user input.
//
// ORDER BY is id alone, matching the cursor. id is monotonic with created_at
// (both come from the same INSERT) and was already the tiebreaker, so ordering
// by it costs nothing in correctness and lets `id < $n` actually start the scan
// instead of filtering after it.
func (r *Repository) ListAudit(ctx context.Context, action string, beforeID int64, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > maxAuditPageSize {
		limit = 50
	}
	const projection = `SELECT id, action, actor_email, target_email, detail, created_at FROM audit_log `

	var (
		query string
		args  []any
	)
	switch {
	case action == "" && beforeID == 0:
		query = projection + `ORDER BY id DESC LIMIT $1`
		args = []any{limit}
	case action == "":
		query = projection + `WHERE id < $1 ORDER BY id DESC LIMIT $2`
		args = []any{beforeID, limit}
	case beforeID == 0:
		query = projection + `WHERE action = $1 ORDER BY id DESC LIMIT $2`
		args = []any{action, limit}
	default:
		query = projection + `WHERE action = $1 AND id < $2 ORDER BY id DESC LIMIT $3`
		args = []any{action, beforeID, limit}
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Action, &e.ActorEmail, &e.TargetEmail, &e.Detail, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AuditRetention is how long the trail is kept.
//
// Ninety days rather than the sweeper's ordinary retention: sessions and
// challenges are operational state that stops mattering the moment it expires,
// while an audit entry is evidence, and an investigation routinely reaches back
// past a quarter. It is still finite, because the failed-login writer accepts a
// row from any unauthenticated caller who can reach the port.
const AuditRetention = 90 * 24 * time.Hour

// SweepAuditLog deletes entries older than retain, in bounded batches.
//
// Batched because the first sweep after this ships may face a table nothing has
// ever pruned: one unbounded DELETE would hold locks and write WAL for as long
// as it takes, on a path that runs while the instance is serving. Each pass
// removes at most one batch and the next tick continues — the table shrinks
// over several sweeps instead of one long stall.
func (r *Repository) SweepAuditLog(ctx context.Context, retain time.Duration) (int64, error) {
	const batch = 5000
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM audit_log
		WHERE id IN (
			SELECT id FROM audit_log
			WHERE created_at < now() - $1::interval
			ORDER BY id
			LIMIT $2
		)`, intervalArg(retain), batch)
	if err != nil {
		return 0, fmt.Errorf("sweep audit log: %w", err)
	}
	return tag.RowsAffected(), nil
}
