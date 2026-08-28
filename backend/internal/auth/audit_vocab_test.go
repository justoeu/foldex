package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The category is the READ SCOPE, so getting it wrong in the content direction
// leaks another account's private label into the administrative trail. It is
// asserted action by action rather than by ranging over the map under test — a
// test that derived its expectations from contentActions would pass for any
// contents, including one where "policy.changed" had been typed into it.
func TestAuditCategory_IsExactlyTheDocumentedSplit(t *testing.T) {
	content := []string{
		AuditLinkCreated, AuditLinkUpdated, AuditLinkDeleted,
		AuditNoteCreated, AuditNoteUpdated, AuditNoteDeleted,
		AuditFolderCreated, AuditFolderUpdated, AuditFolderDeleted, AuditFolderUnlock,
		AuditTagCreated, AuditTagUpdated, AuditTagDeleted,
		AuditImportApplied, AuditBackupRestore,
	}
	identity := []string{
		AuditLoginSucceeded, AuditLoginFailed, AuditRoleChanged, AuditStatusChanged,
		AuditUserCreated, AuditUserDeleted, AuditOwnershipMoved,
		AuditInviteCreated, AuditInviteRevoked, AuditSessionsRevoked,
		AuditPasswordRecovery, AuditPolicyChanged, AuditRolePermissions,
		AuditBackupRunRequested, AuditBackupScheduleChanged, AuditEmailChanged,
		AuditIPBlocked, AuditIPUnblocked,
	}
	for _, a := range content {
		assert.Equal(t, CategoryContent, AuditCategory(a), "action %q", a)
	}
	for _, a := range identity {
		assert.Equal(t, CategoryIdentity, AuditCategory(a), "action %q", a)
	}
	assert.Len(t, AuditActions(), len(content)+len(identity),
		"an action was added to the vocabulary without being classified here")
}

// backup.restored and backup.run_requested share a dotted prefix and sit on
// OPPOSITE sides of the split. This is the case a prefix rule would get wrong
// while reading as though it had decided.
func TestAuditCategory_PrefixIsNotTheRule(t *testing.T) {
	assert.Equal(t, CategoryContent, AuditCategory(AuditBackupRestore))
	assert.Equal(t, CategoryIdentity, AuditCategory(AuditBackupRunRequested))
}

// An action nobody classified must be withheld from administrators, not
// exposed to them: the cost of erring toward content is a missing line on one
// screen, and the cost of the other default is another account's data.
func TestAuditCategory_UnknownActionFailsTowardContent(t *testing.T) {
	for _, a := range []string{"", "link", "something.invented", "policy"} {
		assert.Equal(t, CategoryContent, AuditCategory(a), "action %q", a)
		assert.False(t, KnownAuditAction(a), "action %q must not be known", a)
	}
}

// One wrong password is a typo and five in a quarter of an hour is an attack,
// and the screen has to show the difference. The threshold is the same one
// attemptlimit locks out on, so the card and the limiter cannot disagree.
func TestAuditSeverity_LoginFailureEscalatesWithTheBurst(t *testing.T) {
	assert.Equal(t, SeverityWarning, AuditSeverity(AuditLoginFailed, 0))
	assert.Equal(t, SeverityWarning, AuditSeverity(AuditLoginFailed, RiskBurstThreshold-1))
	assert.Equal(t, SeverityCritical, AuditSeverity(AuditLoginFailed, RiskBurstThreshold))
	assert.Equal(t, SeverityCritical, AuditSeverity(AuditLoginFailed, RiskBurstThreshold+40))
}

// The burst count is meaningful for exactly one action. Letting it raise any
// other would make a busy day look like an incident.
func TestAuditSeverity_BurstAppliesOnlyToLoginFailures(t *testing.T) {
	for _, a := range []string{AuditLoginSucceeded, AuditLinkCreated, AuditRoleChanged} {
		assert.Equal(t, AuditSeverity(a, 0), AuditSeverity(a, 99),
			"the burst count must not move %q", a)
	}
}

// The three levels have to mean something distinct, or the badge is decoration.
// Creating a bookmark is not a security event however many times it happens;
// deleting an account and rewriting the permission matrix are.
func TestAuditSeverity_ClassifiesTheVocabulary(t *testing.T) {
	assert.Equal(t, SeverityInfo, AuditSeverity(AuditLoginSucceeded, 0))
	assert.Equal(t, SeverityInfo, AuditSeverity(AuditLinkCreated, 0))
	assert.Equal(t, SeverityWarning, AuditSeverity(AuditRoleChanged, 0))
	assert.Equal(t, SeverityWarning, AuditSeverity(AuditIPBlocked, 0))
	assert.Equal(t, SeverityCritical, AuditSeverity(AuditUserDeleted, 0))
	assert.Equal(t, SeverityCritical, AuditSeverity(AuditOwnershipMoved, 0))
	assert.Equal(t, SeverityCritical, AuditSeverity(AuditRolePermissions, 0))
}

// ContentActions feeds the SQL that decides which rows are pseudonymised and
// which the owner's feed returns at all. A drift between it and the category
// function would split the two halves of the read scope apart.
func TestContentActions_MatchesTheCategoryFunction(t *testing.T) {
	list := ContentActions()
	require.NotEmpty(t, list)
	for _, a := range list {
		assert.Equal(t, CategoryContent, AuditCategory(a), "action %q", a)
	}
	for _, a := range AuditActions() {
		if AuditCategory(a) == CategoryContent {
			assert.Contains(t, list, a, "content action %q missing from ContentActions", a)
		}
	}
}

// The filter row must not reshuffle between two loads of the same page, which
// is why the order is a slice and not a map range.
func TestAuditActions_IsStableAndCopied(t *testing.T) {
	first, second := AuditActions(), AuditActions()
	assert.Equal(t, first, second)
	first[0] = "mutated"
	assert.NotEqual(t, "mutated", AuditActions()[0],
		"AuditActions must hand out a copy, or one caller can corrupt the vocabulary")
}
