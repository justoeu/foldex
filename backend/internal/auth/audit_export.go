package auth

import "foldex/internal/auth/auditpkg"

const (
	AuditLoginSucceeded        = auditpkg.AuditLoginSucceeded
	AuditLoginFailed           = auditpkg.AuditLoginFailed
	AuditRoleChanged           = auditpkg.AuditRoleChanged
	AuditStatusChanged         = auditpkg.AuditStatusChanged
	AuditUserCreated           = auditpkg.AuditUserCreated
	AuditUserDeleted           = auditpkg.AuditUserDeleted
	AuditOwnershipMoved        = auditpkg.AuditOwnershipMoved
	AuditInviteCreated         = auditpkg.AuditInviteCreated
	AuditInviteRevoked         = auditpkg.AuditInviteRevoked
	AuditSessionsRevoked       = auditpkg.AuditSessionsRevoked
	AuditPasswordRecovery      = auditpkg.AuditPasswordRecovery
	AuditPolicyChanged         = auditpkg.AuditPolicyChanged
	AuditRolePermissions       = auditpkg.AuditRolePermissions
	AuditBackupRunRequested    = auditpkg.AuditBackupRunRequested
	AuditBackupScheduleChanged = auditpkg.AuditBackupScheduleChanged
	AuditEmailChanged          = auditpkg.AuditEmailChanged
	AuditLinkCreated           = auditpkg.AuditLinkCreated
	AuditLinkUpdated           = auditpkg.AuditLinkUpdated
	AuditLinkDeleted           = auditpkg.AuditLinkDeleted
	AuditNoteCreated           = auditpkg.AuditNoteCreated
	AuditNoteUpdated           = auditpkg.AuditNoteUpdated
	AuditNoteDeleted           = auditpkg.AuditNoteDeleted
	AuditFolderCreated         = auditpkg.AuditFolderCreated
	AuditFolderUpdated         = auditpkg.AuditFolderUpdated
	AuditFolderDeleted         = auditpkg.AuditFolderDeleted
	AuditFolderUnlock          = auditpkg.AuditFolderUnlock
	AuditTagCreated            = auditpkg.AuditTagCreated
	AuditTagUpdated            = auditpkg.AuditTagUpdated
	AuditTagDeleted            = auditpkg.AuditTagDeleted
	AuditImportApplied         = auditpkg.AuditImportApplied
	AuditBackupRestore         = auditpkg.AuditBackupRestore
	AuditIPBlocked             = auditpkg.AuditIPBlocked
	AuditIPUnblocked           = auditpkg.AuditIPUnblocked
	AuditRateLimited           = auditpkg.AuditRateLimited
	CategoryIdentity           = auditpkg.CategoryIdentity
	CategoryContent            = auditpkg.CategoryContent
	SeverityInfo               = auditpkg.SeverityInfo
	SeverityWarning            = auditpkg.SeverityWarning
	SeverityCritical           = auditpkg.SeverityCritical
	RiskBurstThreshold         = auditpkg.RiskBurstThreshold
)

func AuditCategory(action string) string { return auditpkg.AuditCategory(action) }

func ContentActions() []string { return auditpkg.ContentActions() }

func AuditSeverity(action string, burst int) string { return auditpkg.AuditSeverity(action, burst) }

func AuditActions() []string { return auditpkg.AuditActions() }

func KnownAuditAction(action string) bool { return auditpkg.KnownAuditAction(action) }
