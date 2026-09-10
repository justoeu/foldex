package auditpkg

const (
	AuditLoginSucceeded        = "login.succeeded"
	AuditLoginFailed           = "login.failed"
	AuditRoleChanged           = "user.role_changed"
	AuditStatusChanged         = "user.status_changed"
	AuditUserCreated           = "user.created"
	AuditUserDeleted           = "user.deleted"
	AuditOwnershipMoved        = "instance.ownership_transferred"
	AuditInviteCreated         = "invite.created"
	AuditInviteRevoked         = "invite.revoked"
	AuditSessionsRevoked       = "user.sessions_revoked"
	AuditPasswordRecovery      = "user.password_recovery_sent"
	AuditPolicyChanged         = "policy.changed"
	AuditRolePermissions       = "role.permissions_changed"
	AuditBackupRunRequested    = "backup.run_requested"
	AuditBackupScheduleChanged = "backup.schedule_changed"
	// An artifact left the instance (ADR-48). Its own event, never folded into
	// the run trigger: "someone pressed Run now" and "someone walked out with
	// every user's content and every bcrypt hash" are not the same sentence.
	AuditBackupDownloaded = "backup.downloaded"
	AuditEmailChanged     = "user.email_changed"
)

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

const (
	AuditIPBlocked   = "instance.ip_blocked"
	AuditIPUnblocked = "instance.ip_unblocked"
)

const AuditRateLimited = "auth.rate_limited"

const (
	CategoryIdentity = "identity"
	CategoryContent  = "content"
)

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

func AuditCategory(action string) string {
	if contentActions[action] {
		return CategoryContent
	}
	if _, known := identitySeverity[action]; known {
		return CategoryIdentity
	}
	return CategoryContent
}

func ContentActions() []string {
	out := make([]string, 0, len(contentActions))
	for _, a := range auditActionOrder {
		if contentActions[a] {
			out = append(out, a)
		}
	}
	return out
}

const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

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
	// Warning, not info: routine in a healthy instance, and the first line an
	// investigator reads in an unhealthy one.
	AuditBackupDownloaded: SeverityWarning,
	AuditEmailChanged:     SeverityWarning,
	AuditIPBlocked:        SeverityWarning,
	AuditIPUnblocked:      SeverityWarning,
	AuditRateLimited:      SeverityWarning,
}

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
	AuditBackupDownloaded,
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

func AuditActions() []string {
	out := make([]string, len(auditActionOrder))
	copy(out, auditActionOrder)
	return out
}

func KnownAuditAction(action string) bool {
	_, identity := identitySeverity[action]
	return identity || contentActions[action]
}

const RiskBurstThreshold = 5
