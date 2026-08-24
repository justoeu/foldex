package authctx

// Permission is one capability a role either holds or does not — ADR-33.
//
// The set is deliberately coarse. A permission exists here only when some route
// actually gates on it; a matrix with entries nothing enforces is worse than no
// matrix at all, because it reads as a promise the server does not keep.
type Permission string

const (
	// Content permissions govern the caller's OWN library and nothing else.
	// They decide whether a write is allowed at all — never whose rows are
	// visible. Ownership scoping stays exactly where ADR-30 put it: an explicit
	// uid parameter on every repository method, with user_id as the first
	// predicate. A viewer and an editor see precisely the same rows (their own);
	// they differ only in whether the server accepts a mutation.
	PermContentRead  Permission = "content.read"
	PermContentWrite Permission = "content.write"

	// Moving data in and out of the instance.
	PermBackupExport  Permission = "backup.export"
	PermBackupRestore Permission = "backup.restore"
	PermImportRun     Permission = "import.run"

	// Administration of people.
	PermUsersRead    Permission = "users.read"
	PermUsersWrite   Permission = "users.write"
	PermRolesAssign  Permission = "roles.assign"
	PermInvitesRead  Permission = "invites.read"
	PermInvitesWrite Permission = "invites.write"

	// Administration of the instance itself.
	PermAuditRead   Permission = "audit.read"
	PermPolicyRead  Permission = "policy.read"
	PermPolicyWrite Permission = "policy.write"

	// Handing the instance to someone else. Owner-only by construction: it is
	// the one action whose whole purpose is to change who holds every other one.
	PermInstanceTransfer Permission = "instance.transfer"
)

// AllPermissions lists every permission in display order.
//
// A slice rather than a range over the matrix map: Go randomizes map iteration,
// and the administration screen renders this as a stable matrix whose rows must
// not reshuffle between two loads of the same page.
var AllPermissions = []Permission{
	PermContentRead,
	PermContentWrite,
	PermBackupExport,
	PermBackupRestore,
	PermImportRun,
	PermUsersRead,
	PermUsersWrite,
	PermRolesAssign,
	PermInvitesRead,
	PermInvitesWrite,
	PermAuditRead,
	PermPolicyRead,
	PermPolicyWrite,
	PermInstanceTransfer,
}

// AllRoles lists every role from most to least privileged, in display order.
var AllRoles = []Role{RoleOwner, RoleAdmin, RoleEditor, RoleViewer}

// rolePermissions is the matrix. A role absent from this map — or a role string
// that arrived from somewhere that skipped the CHECK constraint — resolves to a
// nil set, so Can reports false for everything. Authorization fails CLOSED, and
// an unknown role is powerless rather than unconstrained.
var rolePermissions = map[Role]map[Permission]bool{
	RoleOwner: {
		PermContentRead:      true,
		PermContentWrite:     true,
		PermBackupExport:     true,
		PermBackupRestore:    true,
		PermImportRun:        true,
		PermUsersRead:        true,
		PermUsersWrite:       true,
		PermRolesAssign:      true,
		PermInvitesRead:      true,
		PermInvitesWrite:     true,
		PermAuditRead:        true,
		PermPolicyRead:       true,
		PermPolicyWrite:      true,
		PermInstanceTransfer: true,
	},
	RoleAdmin: {
		PermContentRead:   true,
		PermContentWrite:  true,
		PermBackupExport:  true,
		PermBackupRestore: true,
		PermImportRun:     true,
		PermUsersRead:     true,
		PermUsersWrite:    true,
		PermRolesAssign:   true,
		PermInvitesRead:   true,
		PermInvitesWrite:  true,
		PermAuditRead:     true,
		PermPolicyRead:    true,
		// No PermPolicyWrite and no PermInstanceTransfer: an admin manages
		// people, the owner sets the rules those people are managed under. An
		// admin who could rewrite the password policy or the OAuth allowlist
		// could lower the instance's floor and then walk in through it.
	},
	RoleEditor: {
		PermContentRead:   true,
		PermContentWrite:  true,
		PermBackupExport:  true,
		PermBackupRestore: true,
		PermImportRun:     true,
	},
	RoleViewer: {
		PermContentRead: true,
		// Export but not restore: reading your own library into a file is still
		// reading. Restore writes rows, and import creates them.
		PermBackupExport: true,
	},
}

// Can reports whether the role holds the permission.
func (r Role) Can(p Permission) bool { return rolePermissions[r][p] }

// Permissions returns the role's permissions in AllPermissions order.
func (r Role) Permissions() []Permission {
	held := make([]Permission, 0, len(AllPermissions))
	for _, p := range AllPermissions {
		if r.Can(p) {
			held = append(held, p)
		}
	}
	return held
}

// Valid reports whether the role is one the matrix knows, mirroring the
// database CHECK constraint so a bad value is refused before it reaches SQL.
func (r Role) Valid() bool { _, ok := rolePermissions[r]; return ok }

// DefaultGrants is the compiled matrix, as a value a caller can copy.
//
// It exists because the stored grants (ADR-42) are seeded from it and fall back
// to it, and neither is possible while the only access is the unexported map.
func DefaultGrants() map[Role]map[Permission]bool {
	out := make(map[Role]map[Permission]bool, len(rolePermissions))
	for role, held := range rolePermissions {
		copied := make(map[Permission]bool, len(held))
		for p, ok := range held {
			copied[p] = ok
		}
		out[role] = copied
	}
	return out
}

// lockedPermissions are the entries no configuration may ever add or remove.
//
// Each is here because making it configurable would make the configurability
// itself unsound, not because it happens to be sensitive:
//
//   - PermRolesAssign is the META-permission. A role that can be GRANTED the
//     power to grant would, in one further step, grant itself everything else,
//     which makes locking the rest decorative.
//   - PermPolicyWrite and PermInstanceTransfer are the two owner-level entries.
//     An admin who could take policy.write would lower the password floor and
//     then walk in through it; instance.transfer changes who holds every other
//     permission there is.
//   - PermContentRead is locked in the other direction — it can never be
//     REMOVED. An account that cannot read its own library is not a restricted
//     account, it is a broken one, and its owner has no way to tell which.
var lockedPermissions = map[Permission]bool{
	PermRolesAssign:      true,
	PermPolicyWrite:      true,
	PermInstanceTransfer: true,
	PermContentRead:      true,
}

// IsPermissionLocked reports whether a permission is outside configuration.
func IsPermissionLocked(p Permission) bool { return lockedPermissions[p] }

// IsRoleEditable reports whether a role's grants may be configured at all.
//
// The owner is not. It is the role that exists to be able to fix everything
// else, so a configuration that could strip it is a configuration that can
// leave the instance unrecoverable except by direct SQL — the same reasoning
// that keeps at least one active administrator alive.
func IsRoleEditable(r Role) bool { return r != RoleOwner && r.Valid() }
