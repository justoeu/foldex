package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"foldex/internal/pkg/auditctx"
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
	AuditRolePermissions  = "role.permissions_changed"
	// AuditBackupRunRequested records a manual "run now" on the operational
	// backup surface (ADR-43). What executed — or failed — is backup_run's
	// story; the trail only answers WHO asked.
	AuditBackupRunRequested = "backup.run_requested"
	// AuditBackupScheduleChanged records an edit (or reset to the env
	// baseline) of the backup agenda (ADR-44). WHO moved the schedule is the
	// trail's story; what the agent then does is backup_run's.
	AuditBackupScheduleChanged = "backup.schedule_changed"
	// AuditEmailChanged records the address MOVING, at the moment the
	// confirmation link is consumed — never when it is requested. A request
	// that nobody confirms changed nothing, and an entry for it would make the
	// trail claim an account moved when it did not.
	AuditEmailChanged = "user.email_changed"
)

// AuditEntry is one row of the trail as the API returns it.
//
// Category and Severity are derived (see audit_vocab.go), never stored. Subject
// is populated by exactly one query — the owner's own-activity feed; the
// administrative projection does not select the column at all, which is what
// keeps INV-045 true for a table both readers share.
type AuditEntry struct {
	ID       int64  `json:"id"`
	Action   string `json:"action"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	// ActorEmail is withheld for content rows on the administrative
	// projection: who edited a bookmark is answered by ActorRef, an opaque id.
	ActorEmail  *string   `json:"actor_email"`
	ActorRef    *int64    `json:"actor_ref"`
	TargetEmail *string   `json:"target_email"`
	Detail      *string   `json:"detail"`
	IP          *string   `json:"ip"`
	IPTrusted   bool      `json:"ip_trusted"`
	UserAgent   *string   `json:"user_agent"`
	EntityKind  *string   `json:"entity_kind,omitempty"`
	EntityID    *int64    `json:"entity_id,omitempty"`
	Subject     *string   `json:"subject,omitempty"`
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
	// IP is the address the server OBSERVED — RemoteAddr after
	// server.trustedProxyRealIP ran — and IPTrusted says whether a configured
	// proxy vouched for it. The pair travels together on purpose: an address
	// with no provenance is the thing migration 000033 refused to store.
	IP        string
	IPTrusted bool
	UserAgent string
	// EntityKind, EntityID and Subject describe the row a CONTENT event
	// touched. Subject is user content and is projected only to its owner.
	EntityKind string
	EntityID   *int64
	Subject    string
}

// WithRequest fills the context columns from the live request.
//
// One helper at every write site rather than three lines repeated: the address,
// its provenance and the device are a SET — a row carrying an address without
// the flag that says whether anyone vouched for it is the shape migration
// 000033 refused, and the easiest way to recreate it is to copy two of the
// three lines.
func (rec AuditRecord) WithRequest(r *http.Request) AuditRecord {
	if r == nil {
		return rec
	}
	rec.IP = normalizeAuditIP(r.RemoteAddr)
	rec.IPTrusted = auditctx.IPTrusted(r.Context())
	rec.UserAgent = r.UserAgent()
	return rec
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
	// The address goes in through NULLIF + a cast rather than as a typed
	// value: an unparseable string must land as NULL, not abort the INSERT.
	// An audit failure is logged and swallowed, so a malformed address would
	// not surface as an error — the ENTRY would simply vanish, which is the one
	// outcome a trail must not have.
	_, err := r.pool.Exec(ctx, `
		INSERT INTO audit_log (
			action, actor_id, actor_email, target_id, target_email, detail,
			ip, ip_trusted, user_agent, entity_kind, entity_id, subject)
		VALUES ($1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), NULLIF($6, ''),
			NULLIF($7, '')::inet, $8, NULLIF($9, ''), NULLIF($10, ''), $11, NULLIF($12, ''))`,
		rec.Action, actor, rec.ActorEmail, target, rec.TargetEmail, truncateDetail(rec.Detail),
		normalizeAuditIP(rec.IP), rec.IPTrusted, truncateTo(rec.UserAgent, maxAuditUserAgent),
		truncateTo(rec.EntityKind, maxAuditEntityKind), rec.EntityID,
		truncateTo(rec.Subject, maxAuditSubject))
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
	// Mirror migration 000044's CHECKs, for the reason above.
	maxAuditUserAgent  = 256
	maxAuditEntityKind = 32
	maxAuditSubject    = 256
	// maxAuditPageSize bounds one page of the trail. Shared with the handler so
	// the range it validates and the range this clamps cannot drift apart.
	maxAuditPageSize = 200
)

// normalizeAuditIP returns the address in Postgres's own spelling, or "" for
// anything inet would reject.
//
// Parsing here rather than letting the cast fail: RemoteAddr is
// "host:port" on a direct bind and a bare host once trustedProxyRealIP has
// rewritten it, and an IPv4-mapped IPv6 address ("::ffff:1.2.3.4") is a second
// spelling of a row that already exists. Both would make the blocklist's
// equality test and the origins aggregate disagree with themselves.
// NormalizeIP is normalizeAuditIP for callers outside this package — the
// enforcement middleware and the blocklist share it so a blocked address and a
// recorded one cannot be two spellings that never compare equal.
func NormalizeIP(raw string) string { return normalizeAuditIP(raw) }

func normalizeAuditIP(raw string) string {
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	addr, err := netip.ParseAddr(strings.Trim(raw, "[]"))
	if err != nil {
		return ""
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	return addr.String()
}

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
