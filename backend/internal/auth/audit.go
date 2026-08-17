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
	AuditUserDeleted      = "user.deleted"
	AuditOwnershipMoved   = "instance.ownership_transferred"
	AuditInviteCreated    = "invite.created"
	AuditInviteRevoked    = "invite.revoked"
	AuditSessionsRevoked  = "user.sessions_revoked"
	AuditPasswordRecovery = "user.password_recovery_sent"
	AuditPolicyChanged    = "policy.changed"
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

// truncateDetail keeps a long value from tripping the CHECK constraint and
// failing the insert. Losing the tail of a detail beats losing the entry.
func truncateDetail(s string) string {
	const max = 512
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
// Keyset pagination on (created_at, id) rather than OFFSET: the trail grows at
// its head, so an offset-paged second page would re-show rows that the first
// page already displayed as soon as anything was written between the two
// requests.
func (r *Repository) ListAudit(ctx context.Context, action string, beforeID int64, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, action, actor_email, target_email, detail, created_at
		FROM audit_log
		WHERE ($1 = '' OR action = $1)
		  AND ($2 = 0 OR id < $2)
		ORDER BY created_at DESC, id DESC
		LIMIT $3`, action, beforeID, limit)
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
