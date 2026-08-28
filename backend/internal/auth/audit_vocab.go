package auth

// The audit vocabulary — ADR-46.
//
// Actions are grouped along two axes the screen needs and the database does
// not store: a CATEGORY, which decides who may read the row's content, and a
// SEVERITY, which decides how it is surfaced. Both are derived here rather
// than stored in columns, for the reason migration 000037's note gives about
// frozen migrations: a stored classification taken at write time would keep
// whatever the vocabulary meant on the day the row was written, and every
// later refinement would apply only to rows written after it. Derivation means
// the whole trail re-classifies the moment this file changes.

// Content actions. Recorded for every accepted mutation of the caller's own
// library, by the middleware in internal/server rather than by each handler —
// so a route added later is covered without anyone having to remember, the
// same reasoning that puts credential redaction at the root log handler.
const (
	AuditLinkCreated   = "link.created"
	AuditLinkUpdated   = "link.updated"
	AuditLinkDeleted   = "link.deleted"
	AuditNoteCreated   = "note.created"
	AuditNoteUpdated   = "note.updated"
	AuditNoteDeleted   = "note.deleted"
	AuditFolderCreated = "folder.created"
	AuditFolderUpdated = "folder.updated"
	AuditFolderDeleted = "folder.deleted"
	AuditFolderUnlock  = "folder.unlocked"
	AuditTagCreated    = "tag.created"
	AuditTagUpdated    = "tag.updated"
	AuditTagDeleted    = "tag.deleted"
	AuditImportApplied = "import.applied"
	AuditBackupRestore = "backup.restored"
)

// AuditIPBlocked and AuditIPUnblocked record the permanent blocklist (ADR-46).
// Identity events, not content: they change who can reach the instance at all.
const (
	AuditIPBlocked   = "instance.ip_blocked"
	AuditIPUnblocked = "instance.ip_unblocked"
)

// AuditRateLimited records a limiter ENTERING LOCKOUT — not a refused request.
//
// The distinction is the whole design of the entry. A limiter under attack
// refuses thousands of requests; one row per refusal would make the trail
// itself the amplifier the control exists to remove, and the table is already
// the most written one in the instance. A lockout is a state CHANGE and there
// is one of them per budget, so the row count is bounded by the number of
// buckets rather than by the attacker's request rate.
//
// It is an IDENTITY event: what changed is whether an origin or an account may
// authenticate at all. Classified as content it would be withheld from the
// administrative projection, and the anomaly panel's throttle rule — which
// reads exactly these rows — would be the one screen unable to see the
// strongest signal the instance produces.
const AuditRateLimited = "auth.rate_limited"

// Category decides the READ SCOPE of a row, which is the whole reason it
// exists. A content row carries a subject — the link's title, the folder's
// name — and that is the caller's private content, which INV-045 keeps out of
// every other account's reach, an administrator's included. The administrative
// trail therefore projects content rows WITHOUT their subject and identifies
// their actor by opaque id; the owner's own-activity feed projects the subject
// and is scoped to actor_id = the caller.
const (
	CategoryIdentity = "identity"
	CategoryContent  = "content"
)

// contentActions is the closed set that Category calls content. A membership
// map rather than a prefix test on the action string: "backup.restored" is
// content and "backup.run_requested" is not, and a rule based on the dotted
// prefix would put them on the same side while reading as if it had decided.
var contentActions = map[string]bool{
	AuditLinkCreated:   true,
	AuditLinkUpdated:   true,
	AuditLinkDeleted:   true,
	AuditNoteCreated:   true,
	AuditNoteUpdated:   true,
	AuditNoteDeleted:   true,
	AuditFolderCreated: true,
	AuditFolderUpdated: true,
	AuditFolderDeleted: true,
	AuditFolderUnlock:  true,
	AuditTagCreated:    true,
	AuditTagUpdated:    true,
	AuditTagDeleted:    true,
	AuditImportApplied: true,
	AuditBackupRestore: true,
}

// AuditCategory reports which half of the vocabulary an action belongs to.
//
// It fails toward CONTENT: an action nobody classified is treated as if it
// carried private data, so a vocabulary entry added without a thought about
// read scope is withheld from administrators rather than exposed to them. The
// cost of being wrong that way is a missing line on one screen; the cost of the
// other default is a content leak across accounts.
func AuditCategory(action string) string {
	if contentActions[action] {
		return CategoryContent
	}
	if _, known := identitySeverity[action]; known {
		return CategoryIdentity
	}
	return CategoryContent
}

// ContentActions returns the content vocabulary, for queries that filter on it.
func ContentActions() []string {
	out := make([]string, 0, len(contentActions))
	for _, a := range auditActionOrder {
		if contentActions[a] {
			out = append(out, a)
		}
	}
	return out
}

// Severity levels, in the order the screen escalates them.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// identitySeverity classifies the identity half. Everything absent from this
// map is content, and content is informational: creating a bookmark is not a
// security event however many times it happens.
//
// The line between warning and info is whether the entry describes a change to
// WHO CAN DO WHAT. A successful sign-in is expected traffic; a role change, a
// suspension, a policy edit or a blocklist entry is someone's authority moving,
// and that is what an administrator scanning this screen is looking for.
//
// login.failed is the one action whose severity is not fixed here: a single
// wrong password is noise, and a burst against one mailbox is the signal the
// screen exists to surface. AuditSeverity takes that from the row's context.
var identitySeverity = map[string]string{
	AuditLoginSucceeded:        SeverityInfo,
	AuditLoginFailed:           SeverityWarning,
	AuditRoleChanged:           SeverityWarning,
	AuditStatusChanged:         SeverityWarning,
	AuditUserCreated:           SeverityWarning,
	AuditUserDeleted:           SeverityCritical,
	AuditOwnershipMoved:        SeverityCritical,
	AuditInviteCreated:         SeverityInfo,
	AuditInviteRevoked:         SeverityInfo,
	AuditSessionsRevoked:       SeverityWarning,
	AuditPasswordRecovery:      SeverityWarning,
	AuditPolicyChanged:         SeverityWarning,
	AuditRolePermissions:       SeverityCritical,
	AuditBackupRunRequested:    SeverityInfo,
	AuditBackupScheduleChanged: SeverityWarning,
	AuditEmailChanged:          SeverityWarning,
	AuditIPBlocked:             SeverityWarning,
	AuditIPUnblocked:           SeverityWarning,
	// A lockout is the control WORKING, so it is a warning rather than the
	// critical reserved for authority moving. It still has to be above info:
	// a run of these is what the anomaly panel calls a throttle, and a level
	// nobody scans for would bury it among the sign-ins.
	AuditRateLimited: SeverityWarning,
}

// AuditSeverity classifies one entry.
//
// burst is how many failed logins the same address produced inside the risk
// window; it is meaningful only for login.failed and ignored otherwise. It is
// a parameter rather than a lookup because the caller has already computed the
// per-address counts for the risk card, and re-deriving them per row would be
// one query per line of the page.
func AuditSeverity(action string, burst int) string {
	if action == AuditLoginFailed {
		if burst >= RiskBurstThreshold {
			return SeverityCritical
		}
		return SeverityWarning
	}
	if s, ok := identitySeverity[action]; ok {
		return s
	}
	return SeverityInfo
}

// auditActionOrder is every action this binary writes, in the order the screen
// offers them as filters. A slice because Go randomizes map iteration and the
// filter row must not reshuffle between two loads of the same page — the same
// reason authctx.AllPermissions is a slice.
var auditActionOrder = []string{
	AuditLoginFailed,
	AuditLoginSucceeded,
	AuditRoleChanged,
	AuditStatusChanged,
	AuditUserCreated,
	AuditUserDeleted,
	AuditOwnershipMoved,
	AuditInviteCreated,
	AuditInviteRevoked,
	AuditSessionsRevoked,
	AuditPasswordRecovery,
	AuditEmailChanged,
	AuditPolicyChanged,
	AuditRolePermissions,
	AuditBackupRunRequested,
	AuditBackupScheduleChanged,
	AuditIPBlocked,
	AuditIPUnblocked,
	AuditRateLimited,
	AuditLinkCreated,
	AuditLinkUpdated,
	AuditLinkDeleted,
	AuditNoteCreated,
	AuditNoteUpdated,
	AuditNoteDeleted,
	AuditFolderCreated,
	AuditFolderUpdated,
	AuditFolderDeleted,
	AuditFolderUnlock,
	AuditTagCreated,
	AuditTagUpdated,
	AuditTagDeleted,
	AuditImportApplied,
	AuditBackupRestore,
}

// AuditActions returns the vocabulary in display order.
func AuditActions() []string {
	out := make([]string, len(auditActionOrder))
	copy(out, auditActionOrder)
	return out
}

// KnownAuditAction reports whether an action is one this binary writes.
//
// The list endpoint validates its filter against this: an unknown action would
// otherwise run a full backward scan of the trail to return zero rows, which is
// an unauthenticated-adjacent caller's cheapest way to make the database work.
func KnownAuditAction(action string) bool {
	_, identity := identitySeverity[action]
	return identity || contentActions[action]
}

// RiskBurstThreshold is how many failures from one address inside
// RiskWindow make the screen call it an attack rather than a typo.
//
// Five because that is what attemptlimit already parks an address for: the
// screen and the limiter should not disagree about what a burst is, or the
// card would announce a threat the server had silently already handled — or
// worse, stay quiet about one it had not.
const RiskBurstThreshold = 5
