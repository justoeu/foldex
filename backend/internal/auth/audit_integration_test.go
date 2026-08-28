//go:build integration

package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/pkg/authctx"
	"foldex/internal/testdb"
)

// auditFixture gives each test its own empty trail and two accounts.
//
// The trail is truncated rather than filtered around, because every assertion
// here is about what a page CONTAINS — and a shared container carries whatever
// the previous test wrote.
func auditFixture(t *testing.T) (*auth.Repository, *pgxpool.Pool, auth.User, auth.User) {
	t.Helper()
	pool := testdb.Shared(t)
	repo := auth.NewRepository(pool)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `TRUNCATE audit_log, ip_block RESTART IDENTITY`)
	require.NoError(t, err)

	uniq := time.Now().UnixNano()
	owner, err := repo.AdminCreateUser(ctx,
		fmt.Sprintf("owner-%d@foldex.test", uniq), "Owner", "correct horse battery staple", authctx.RoleAdmin)
	require.NoError(t, err)
	member, err := repo.AdminCreateUser(ctx,
		fmt.Sprintf("member-%d@foldex.test", uniq), "Member", "correct horse battery staple", authctx.RoleEditor)
	require.NoError(t, err)
	return repo, pool, owner, member
}

func writeAudit(t *testing.T, repo *auth.Repository, rec auth.AuditRecord) {
	t.Helper()
	require.NoError(t, repo.Audit(context.Background(), rec))
}

// ─────────────────────────────────────────────────────────────────────
// The read split — INV-045 for a table two readers share
// ─────────────────────────────────────────────────────────────────────

// The single most important assertion in this file. A content event carries the
// caller's private label; the administrative projection must return it to
// nobody, and it must not name the person either. Asserted against the DATABASE
// rather than against a Go filter, because the guarantee is that the column
// never leaves it.
func TestAudit_AdminNeverSeesAContentSubjectOrItsActorEmail(t *testing.T) {
	repo, _, _, member := auditFixture(t)
	writeAudit(t, repo, auth.AuditRecord{
		Action:      auth.AuditLinkCreated,
		ActorID:     &member.ID,
		ActorEmail:  member.Email,
		TargetEmail: member.Email,
		Detail:      "a detail nobody else should read",
		Subject:     "My private bookmark title",
		EntityKind:  "link",
		EntityID:    ptr(int64(42)),
	})

	entries, err := repo.ListAudit(context.Background(), auth.AuditFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	e := entries[0]

	assert.Nil(t, e.Subject, "the content label must never reach the administrative trail")
	assert.Nil(t, e.ActorEmail, "a content row must not name its actor")
	assert.Nil(t, e.TargetEmail)
	assert.Nil(t, e.Detail, "the detail of a content event is content too")
	require.NotNil(t, e.ActorRef, "the opaque actor id is what replaces the name")
	assert.Equal(t, int64(member.ID), *e.ActorRef)
	assert.Equal(t, auth.CategoryContent, e.Category)
}

// The other half: an IDENTITY row keeps its e-mail, because that is what an
// administrator needs to answer "who promoted whom" and it names an account
// they already manage.
func TestAudit_AdminStillSeesIdentityEmails(t *testing.T) {
	repo, _, owner, member := auditFixture(t)
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditRoleChanged, ActorID: &owner.ID, ActorEmail: owner.Email,
		TargetID: &member.ID, TargetEmail: member.Email, Detail: "editor → admin",
	})

	entries, err := repo.ListAudit(context.Background(), auth.AuditFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].ActorEmail)
	assert.Equal(t, owner.Email, *entries[0].ActorEmail)
	require.NotNil(t, entries[0].TargetEmail)
	assert.Equal(t, member.Email, *entries[0].TargetEmail)
	require.NotNil(t, entries[0].Detail)
	assert.Equal(t, "editor → admin", *entries[0].Detail)
	assert.Equal(t, auth.CategoryIdentity, entries[0].Category)
}

// The owner IS the actor, so there is nothing to withhold from them.
func TestAudit_OwnFeedReturnsTheSubject(t *testing.T) {
	repo, _, _, member := auditFixture(t)
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditLinkCreated, ActorID: &member.ID,
		Subject: "My private bookmark title", EntityKind: "link", EntityID: ptr(int64(42)),
	})

	entries, err := repo.ListOwnActivity(context.Background(), int64(member.ID), 0, 50)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].Subject)
	assert.Equal(t, "My private bookmark title", *entries[0].Subject)
	require.NotNil(t, entries[0].EntityID)
	assert.Equal(t, int64(42), *entries[0].EntityID)
}

// INV-001's explicit-uid rule applied to a table that is otherwise
// instance-wide. Another account's rows are not "hidden" by the UI — they are
// not in the result set.
func TestCrossUser_OwnActivityNeverReturnsAnotherAccountsRows(t *testing.T) {
	repo, _, owner, member := auditFixture(t)
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditLinkCreated, ActorID: &owner.ID, Subject: "the owner's secret",
	})
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditLinkCreated, ActorID: &member.ID, Subject: "the member's own",
	})

	entries, err := repo.ListOwnActivity(context.Background(), int64(member.ID), 0, 50)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "the member's own", *entries[0].Subject)
}

// "Your role was changed to admin" is something done TO the account, not BY it.
// A feed titled "my activity" must not report it.
func TestAudit_OwnFeedExcludesIdentityEvents(t *testing.T) {
	repo, _, owner, member := auditFixture(t)
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditRoleChanged, ActorID: &owner.ID, TargetID: &member.ID,
	})
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditLoginSucceeded, ActorID: &member.ID, ActorEmail: member.Email,
	})
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditNoteUpdated, ActorID: &member.ID, Subject: "a note",
	})

	entries, err := repo.ListOwnActivity(context.Background(), int64(member.ID), 0, 50)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, auth.AuditNoteUpdated, entries[0].Action)
}

// ─────────────────────────────────────────────────────────────────────
// Context columns
// ─────────────────────────────────────────────────────────────────────

// The address and the flag that says whether anyone vouched for it are a SET.
// Storing one without the other is the shape migration 000033 refused.
func TestAudit_StoresTheAddressWithItsProvenance(t *testing.T) {
	repo, _, owner, _ := auditFixture(t)
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditLoginSucceeded, ActorID: &owner.ID, ActorEmail: owner.Email,
		IP: "203.0.113.9", IPTrusted: true, UserAgent: "Mozilla/5.0 (Macintosh)",
	})
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditLoginFailed, TargetEmail: "nobody@foldex.test",
		IP: "198.51.100.4", IPTrusted: false, UserAgent: "curl/8",
	})

	entries, err := repo.ListAudit(context.Background(), auth.AuditFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "198.51.100.4", *entries[0].IP)
	assert.False(t, entries[0].IPTrusted)
	assert.Equal(t, "203.0.113.9", *entries[1].IP)
	assert.True(t, entries[1].IPTrusted, "a proxy-vouched address must say so")
}

// An unparseable address must land as NULL, not abort the INSERT. An audit
// failure is logged and swallowed, so a rejected row does not surface as an
// error — the ENTRY vanishes, which is the one outcome a trail must not have.
func TestAudit_AMalformedAddressLosesTheColumnNotTheRow(t *testing.T) {
	repo, _, _, _ := auditFixture(t)
	for _, bad := range []string{"not-an-ip", "999.1.1.1", "", "10.0.0.0/8"} {
		writeAudit(t, repo, auth.AuditRecord{
			Action: auth.AuditLoginFailed, TargetEmail: "x@foldex.test", IP: bad,
		})
	}
	entries, err := repo.ListAudit(context.Background(), auth.AuditFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 4, "every row must survive its unusable address")
	for _, e := range entries {
		assert.Nil(t, e.IP)
	}
}

// The User-Agent header is attacker-controlled on the unauthenticated failed-
// login path. Without the cap one attempt writes an arbitrarily large permanent
// row, and the per-address rate bucket cannot help: it is keyed by the
// attempted address, so a fresh string buys a fresh budget.
func TestAudit_TruncatesTheDeviceStringRatherThanFailing(t *testing.T) {
	repo, _, _, _ := auditFixture(t)
	huge := ""
	for i := 0; i < 4000; i++ {
		huge += "A"
	}
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditLoginFailed, TargetEmail: "x@foldex.test", UserAgent: huge,
	})
	entries, err := repo.ListAudit(context.Background(), auth.AuditFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].UserAgent)
	assert.LessOrEqual(t, len(*entries[0].UserAgent), 256)
}

// ─────────────────────────────────────────────────────────────────────
// Filters and pagination
// ─────────────────────────────────────────────────────────────────────

func TestAudit_FiltersByActionCategoryAndSearch(t *testing.T) {
	repo, _, owner, member := auditFixture(t)
	ctx := context.Background()
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed,
		TargetEmail: "victim@foldex.test", IP: "189.42.11.7"})
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditRoleChanged,
		ActorID: &owner.ID, ActorEmail: owner.Email, TargetID: &member.ID, TargetEmail: member.Email})
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLinkCreated,
		ActorID: &member.ID, Subject: "secret", IP: "203.0.113.9"})

	byAction, err := repo.ListAudit(ctx, auth.AuditFilter{Action: auth.AuditLoginFailed})
	require.NoError(t, err)
	require.Len(t, byAction, 1)

	content, err := repo.ListAudit(ctx, auth.AuditFilter{Category: auth.CategoryContent})
	require.NoError(t, err)
	require.Len(t, content, 1)
	assert.Equal(t, auth.AuditLinkCreated, content[0].Action)

	identity, err := repo.ListAudit(ctx, auth.AuditFilter{Category: auth.CategoryIdentity})
	require.NoError(t, err)
	assert.Len(t, identity, 2)

	byIP, err := repo.ListAudit(ctx, auth.AuditFilter{Search: "189.42"})
	require.NoError(t, err)
	require.Len(t, byIP, 1, "the address must be searchable as it is DISPLAYED")

	byEmail, err := repo.ListAudit(ctx, auth.AuditFilter{Search: "victim@"})
	require.NoError(t, err)
	assert.Len(t, byEmail, 1)
}

// The de-anonymisation oracle the search predicate WAS.
//
// Without the category gate on the e-mail arms, an administrator asks for
// `?category=content&q=alice@example.com`: the WHERE matches `actor_email` on
// rows whose projection blanks it, and every `actor_ref` that comes back is
// provably Alice's. The column would be withheld from the OUTPUT while the
// INPUT still selected on it — the projection reading as enforcement and
// enforcing nothing.
func TestAudit_SearchCannotDeanonymiseAContentRow(t *testing.T) {
	repo, _, _, member := auditFixture(t)
	ctx := context.Background()
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditLinkCreated, ActorID: &member.ID, ActorEmail: member.Email,
		TargetEmail: member.Email, Subject: "a private title",
	})

	byEmail, err := repo.ListAudit(ctx, auth.AuditFilter{Search: member.Email})
	require.NoError(t, err)
	assert.Empty(t, byEmail,
		"a content row must not be reachable by searching for the account that wrote it")

	scoped, err := repo.ListAudit(ctx,
		auth.AuditFilter{Search: member.Email, Category: auth.CategoryContent})
	require.NoError(t, err)
	assert.Empty(t, scoped, "narrowing to content must not open the e-mail arm either")

	// The same row is still findable by the things the projection DOES return,
	// so the gate narrowed the oracle rather than the feature.
	byAction, err := repo.ListAudit(ctx, auth.AuditFilter{Search: "link.created"})
	require.NoError(t, err)
	assert.Len(t, byAction, 1)
}

// The other half: an IDENTITY row stays searchable by e-mail, which is what an
// administrator investigating an account actually needs.
func TestAudit_SearchStillFindsIdentityRowsByEmail(t *testing.T) {
	repo, _, owner, member := auditFixture(t)
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditRoleChanged, ActorID: &owner.ID, ActorEmail: owner.Email,
		TargetID: &member.ID, TargetEmail: member.Email,
	})

	found, err := repo.ListAudit(context.Background(), auth.AuditFilter{Search: owner.Email})
	require.NoError(t, err)
	assert.Len(t, found, 1)
}

// An address is on the row regardless of category, and searching for it is the
// point of recording it.
func TestAudit_SearchFindsAContentRowByItsAddress(t *testing.T) {
	repo, _, _, member := auditFixture(t)
	writeAudit(t, repo, auth.AuditRecord{
		Action: auth.AuditLinkCreated, ActorID: &member.ID,
		Subject: "a private title", IP: "191.55.8.140",
	})

	found, err := repo.ListAudit(context.Background(), auth.AuditFilter{Search: "191.55"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Nil(t, found[0].Subject)
}

// A search term's wildcards belong to the server. "%" from the box must match
// a literal percent sign, not every row in the window.
func TestAudit_SearchTreatsWildcardsAsLiterals(t *testing.T) {
	repo, _, _, _ := auditFixture(t)
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed, TargetEmail: "a@foldex.test"})
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed, TargetEmail: "100%@foldex.test"})

	all, err := repo.ListAudit(context.Background(), auth.AuditFilter{Search: "%"})
	require.NoError(t, err)
	assert.Len(t, all, 1, "a bare %% must be a literal, not a wildcard")

	under, err := repo.ListAudit(context.Background(), auth.AuditFilter{Search: "_"})
	require.NoError(t, err)
	assert.Empty(t, under, "a bare _ must not match a single character")
}

// The trail grows at its head, so an offset-paged second page would repeat
// rows as soon as anything was written between the two requests.
func TestAudit_KeysetPaginationDoesNotRepeatRows(t *testing.T) {
	repo, _, _, _ := auditFixture(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		writeAudit(t, repo, auth.AuditRecord{
			Action: auth.AuditLoginFailed, TargetEmail: fmt.Sprintf("u%d@foldex.test", i)})
	}
	first, err := repo.ListAudit(ctx, auth.AuditFilter{Limit: 4})
	require.NoError(t, err)
	require.Len(t, first, 4)

	// Something arrives between the two page loads — the case OFFSET gets wrong.
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed, TargetEmail: "late@foldex.test"})

	second, err := repo.ListAudit(ctx, auth.AuditFilter{Limit: 4, BeforeID: first[3].ID})
	require.NoError(t, err)
	require.Len(t, second, 4)
	for _, a := range first {
		for _, b := range second {
			assert.NotEqual(t, a.ID, b.ID, "page two repeated a row from page one")
		}
	}
}

// Oldest-first has to be a different QUERY, not a reversed page. With the page
// reversed in the client, "oldest first" would show the oldest of the newest
// fifty — a control that looks like it works and answers a different question.
func TestAudit_AscendingOrderPagesFromTheOtherEnd(t *testing.T) {
	repo, _, _, _ := auditFixture(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		writeAudit(t, repo, auth.AuditRecord{
			Action: auth.AuditLoginFailed, TargetEmail: fmt.Sprintf("u%d@foldex.test", i)})
	}
	oldest, err := repo.ListAudit(ctx, auth.AuditFilter{Limit: 3, Ascending: true})
	require.NoError(t, err)
	require.Len(t, oldest, 3)
	assert.Equal(t, "u0@foldex.test", *oldest[0].TargetEmail, "ascending must start at the oldest row")
	assert.Less(t, oldest[0].ID, oldest[2].ID)

	newest, err := repo.ListAudit(ctx, auth.AuditFilter{Limit: 3})
	require.NoError(t, err)
	assert.Equal(t, "u9@foldex.test", *newest[0].TargetEmail)

	// The cursor flips with the direction: continuing an ascending page means
	// "past this id" in the other direction.
	next, err := repo.ListAudit(ctx, auth.AuditFilter{Limit: 3, Ascending: true, BeforeID: oldest[2].ID})
	require.NoError(t, err)
	require.Len(t, next, 3)
	assert.Equal(t, "u3@foldex.test", *next[0].TargetEmail)
	for _, a := range oldest {
		for _, b := range next {
			assert.NotEqual(t, a.ID, b.ID)
		}
	}
}

// The window is what keeps a typed filter from reading a ninety-day table.
func TestAudit_WindowExcludesOlderRows(t *testing.T) {
	repo, pool, _, _ := auditFixture(t)
	ctx := context.Background()
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed, TargetEmail: "old@foldex.test"})
	_, err := pool.Exec(ctx, `UPDATE audit_log SET created_at = now() - interval '40 days'`)
	require.NoError(t, err)
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed, TargetEmail: "new@foldex.test"})

	recent, err := repo.ListAudit(ctx, auth.AuditFilter{Since: time.Now().Add(-24 * time.Hour)})
	require.NoError(t, err)
	require.Len(t, recent, 1)
	assert.Equal(t, "new@foldex.test", *recent[0].TargetEmail)
}

// ─────────────────────────────────────────────────────────────────────
// Aggregates
// ─────────────────────────────────────────────────────────────────────

func TestAuditStats_CountsTotalsAndTheirPrecedingWindow(t *testing.T) {
	repo, pool, owner, _ := auditFixture(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed, TargetEmail: "v@foldex.test"})
	}
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditRoleChanged,
		ActorID: &owner.ID, ActorEmail: owner.Email})
	// Push one failure into the PRECEDING window, so the delta has something
	// to compare against rather than always reading as "everything is new".
	_, err := pool.Exec(ctx, `UPDATE audit_log SET created_at = now() - interval '10 days'
		WHERE id = (SELECT min(id) FROM audit_log)`)
	require.NoError(t, err)

	stats, err := repo.AuditStatsSince(ctx, time.Now().Add(-7*24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats.Totals.Events)
	assert.Equal(t, int64(2), stats.Totals.Failures)
	assert.Equal(t, int64(1), stats.Totals.FailuresPrev, "the preceding window must be counted")
	assert.Equal(t, int64(1), stats.Totals.AccessChanges)
	assert.GreaterOrEqual(t, stats.Totals.ActiveUsers, int64(2))
}

// A day on which nothing happened has no rows to group. A chart that dropped
// empty days would compress a quiet week into a busy-looking one.
func TestAuditStats_DaysIncludeTheEmptyOnes(t *testing.T) {
	repo, _, _, _ := auditFixture(t)
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginSucceeded, TargetEmail: "a@foldex.test"})

	stats, err := repo.AuditStatsSince(context.Background(), time.Now().Add(-7*24*time.Hour))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(stats.Days), 7, "every day in the window needs a column")
	var total int64
	for _, d := range stats.Days {
		total += d.Logins + d.Failed + d.Admin + d.Content
	}
	assert.Equal(t, int64(1), total)
}

// The four series are disjoint and together cover the vocabulary, so their sum
// is the day's total and a stacked column is honest.
func TestAuditStats_DaySeriesAreDisjointAndComplete(t *testing.T) {
	repo, _, owner, member := auditFixture(t)
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginSucceeded, ActorID: &owner.ID})
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed, TargetEmail: "v@foldex.test"})
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditRoleChanged, ActorID: &owner.ID})
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLinkCreated, ActorID: &member.ID})

	stats, err := repo.AuditStatsSince(context.Background(), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	var logins, failed, admin, content int64
	for _, d := range stats.Days {
		logins += d.Logins
		failed += d.Failed
		admin += d.Admin
		content += d.Content
	}
	assert.Equal(t, int64(1), logins)
	assert.Equal(t, int64(1), failed)
	assert.Equal(t, int64(1), admin)
	assert.Equal(t, int64(1), content)
}

// The actors card names LIVE accounts and shows the role each holds now — and
// deliberately carries NO id, so an administrator cannot join it back to the
// pseudonymous actor reference on a content line.
func TestAuditStats_ActorsNameLiveAccountsWithoutAnId(t *testing.T) {
	repo, _, owner, member := auditFixture(t)
	for i := 0; i < 3; i++ {
		writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLinkCreated, ActorID: &member.ID})
	}
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginSucceeded, ActorID: &owner.ID})

	stats, err := repo.AuditStatsSince(context.Background(), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.Len(t, stats.Actors, 2)
	assert.Equal(t, member.Email, stats.Actors[0].Email, "busiest first")
	assert.Equal(t, int64(3), stats.Actors[0].Count)
	assert.Equal(t, string(authctx.RoleEditor), stats.Actors[0].Role)
}

func TestAuditStats_OriginsGroupByAddress(t *testing.T) {
	repo, _, _, _ := auditFixture(t)
	for i := 0; i < 4; i++ {
		writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed,
			TargetEmail: "v@foldex.test", IP: "189.42.11.7", UserAgent: "curl/8"})
	}
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginSucceeded,
		IP: "203.0.113.9", IPTrusted: true, UserAgent: "Mozilla/5.0"})

	stats, err := repo.AuditStatsSince(context.Background(), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.Len(t, stats.Origins, 2)
	assert.Equal(t, "189.42.11.7", stats.Origins[0].IP)
	assert.Equal(t, int64(4), stats.Origins[0].Count)
	assert.Equal(t, int64(4), stats.Origins[0].Failures)
	assert.Equal(t, "curl/8", *stats.Origins[0].UserAgent)
	assert.False(t, stats.Origins[0].Blocked)
	assert.True(t, stats.Origins[1].Trusted)
}

// The burst is what the screen leads with. Below the threshold there is no
// incident to announce, and announcing one would train people to ignore it.
func TestAuditStats_RiskAppearsOnlyAtTheThreshold(t *testing.T) {
	repo, _, _, _ := auditFixture(t)
	ctx := context.Background()
	for i := 0; i < auth.RiskBurstThreshold-1; i++ {
		writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed,
			TargetEmail: "v@foldex.test", IP: "189.42.11.7"})
	}
	stats, err := repo.AuditStatsSince(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Nil(t, stats.Risk, "four failures is a person forgetting a password")

	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed,
		TargetEmail: "v@foldex.test", IP: "189.42.11.7"})
	stats, err = repo.AuditStatsSince(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.NotNil(t, stats.Risk)
	assert.Equal(t, "189.42.11.7", stats.Risk.IP)
	assert.Equal(t, int64(auth.RiskBurstThreshold), stats.Risk.Failures)
	assert.Equal(t, int64(1), stats.Risk.Targets)
}

// Failures spread over days are somebody with a bad memory, not an attack. The
// window is measured between the first and last failure of that address.
func TestAuditStats_RiskIgnoresFailuresSpreadOverTime(t *testing.T) {
	repo, pool, _, _ := auditFixture(t)
	ctx := context.Background()
	for i := 0; i < auth.RiskBurstThreshold+2; i++ {
		writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed,
			TargetEmail: "v@foldex.test", IP: "189.42.11.7"})
	}
	_, err := pool.Exec(ctx, `UPDATE audit_log SET created_at = now() - interval '5 hours'
		WHERE id = (SELECT min(id) FROM audit_log)`)
	require.NoError(t, err)

	stats, err := repo.AuditStatsSince(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Nil(t, stats.Risk, "a five-hour spread is not a burst")
}

// Severity is a property of the ADDRESS's behaviour, not of the single row.
func TestFailureBursts_CountsPerAddress(t *testing.T) {
	repo, _, _, _ := auditFixture(t)
	for i := 0; i < 6; i++ {
		writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed,
			TargetEmail: "v@foldex.test", IP: "189.42.11.7"})
	}
	writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed,
		TargetEmail: "v@foldex.test", IP: "203.0.113.9"})

	bursts, err := repo.FailureBursts(context.Background(), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 6, bursts["189.42.11.7"])
	assert.Equal(t, 1, bursts["203.0.113.9"])
	assert.Equal(t, auth.SeverityCritical, auth.AuditSeverity(auth.AuditLoginFailed, bursts["189.42.11.7"]))
	assert.Equal(t, auth.SeverityWarning, auth.AuditSeverity(auth.AuditLoginFailed, bursts["203.0.113.9"]))
}

// ─────────────────────────────────────────────────────────────────────
// Blocklist
// ─────────────────────────────────────────────────────────────────────

func TestIPBlock_RoundTripsAndIsIdempotent(t *testing.T) {
	repo, _, owner, _ := auditFixture(t)
	ctx := context.Background()
	first, err := repo.BlockIP(ctx, "189.42.11.7", "brute force", &owner.ID, owner.Email)
	require.NoError(t, err)
	assert.Equal(t, "189.42.11.7", first.IP)
	assert.Equal(t, "brute force", *first.Reason)
	assert.Equal(t, owner.Email, *first.CreatedBy)

	// Blocking twice is the state the caller asked for — an error would make
	// the button look broken to whoever clicked it again.
	second, err := repo.BlockIP(ctx, "189.42.11.7", "again", &owner.ID, owner.Email)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)

	blocks, err := repo.ListIPBlocks(ctx)
	require.NoError(t, err)
	assert.Len(t, blocks, 1)

	removed, err := repo.UnblockIP(ctx, "189.42.11.7")
	require.NoError(t, err)
	assert.True(t, removed)
	removed, err = repo.UnblockIP(ctx, "189.42.11.7")
	require.NoError(t, err)
	assert.False(t, removed, "removing an absent block is still the asked-for state")
}

// A block installed under one spelling must be removable under the other, or
// the operator sees an entry they cannot delete.
func TestIPBlock_NormalizesTheAddressOnBothSides(t *testing.T) {
	repo, _, owner, _ := auditFixture(t)
	ctx := context.Background()
	_, err := repo.BlockIP(ctx, "198.51.100.4", "", &owner.ID, owner.Email)
	require.NoError(t, err)
	removed, err := repo.UnblockIP(ctx, "::ffff:198.51.100.4")
	require.NoError(t, err)
	assert.True(t, removed)
}

// The enforcement path holds every entry in memory and consults it on every
// request. An unbounded list is an unbounded per-request working set, installed
// by a control whose whole purpose is to be clicked in a hurry.
func TestIPBlock_RefusesToGrowPastTheCap(t *testing.T) {
	repo, pool, owner, _ := auditFixture(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO ip_block (ip)
		SELECT ('198.18.' || (i / 256) || '.' || (i % 256))::inet
		FROM generate_series(0, $1) AS i`, auth.MaxIPBlocks-1)
	require.NoError(t, err)

	_, err = repo.BlockIP(ctx, "203.0.113.9", "", &owner.ID, owner.Email)
	assert.ErrorIs(t, err, auth.ErrBlockFull)
}

// The origins and risk cards say whether the address they name is already
// blocked, so the button does not offer an action that has been taken.
func TestAuditStats_ReportsWhetherAnOriginIsAlreadyBlocked(t *testing.T) {
	repo, _, owner, _ := auditFixture(t)
	ctx := context.Background()
	for i := 0; i < auth.RiskBurstThreshold; i++ {
		writeAudit(t, repo, auth.AuditRecord{Action: auth.AuditLoginFailed,
			TargetEmail: "v@foldex.test", IP: "189.42.11.7"})
	}
	_, err := repo.BlockIP(ctx, "189.42.11.7", "brute force", &owner.ID, owner.Email)
	require.NoError(t, err)

	stats, err := repo.AuditStatsSince(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.NotNil(t, stats.Risk)
	assert.True(t, stats.Risk.Blocked)
	require.NotEmpty(t, stats.Origins)
	assert.True(t, stats.Origins[0].Blocked)
}

// BlockedIPs feeds the enforcement snapshot, so it must return exactly what the
// gate will compare against.
func TestIPBlock_BlockedIPsFeedsTheSnapshot(t *testing.T) {
	repo, _, owner, _ := auditFixture(t)
	ctx := context.Background()
	_, err := repo.BlockIP(ctx, "189.42.11.7", "", &owner.ID, owner.Email)
	require.NoError(t, err)
	_, err = repo.BlockIP(ctx, "2001:db8::1", "", &owner.ID, owner.Email)
	require.NoError(t, err)

	ips, err := repo.BlockedIPs(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"189.42.11.7", "2001:db8::1"}, ips)
}

func ptr[T any](v T) *T { return &v }

// ─────────────────────────────────────────────────────────────────────
// The HTTP surface
// ─────────────────────────────────────────────────────────────────────

// testTrustedProxyIP is the address the harness's block rail treats as a
// configured proxy. Behind nginx every request arrives from one whenever the
// forwarding chain is not what the operator thinks — and the screen would then
// show it as the busiest origin.
const testTrustedProxyIP = "10.4.2.7"

func TestAuditAPI_ServesTheTrailItsHeaderAndItsVocabulary(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	list := decode(t, owner.do(http.MethodGet, "/api/admin/audit", nil))
	entries := list["entries"].([]any)
	require.NotEmpty(t, entries, "the bootstrap sign-in is itself an event")
	first := entries[0].(map[string]any)
	assert.Equal(t, "identity", first["category"])
	assert.Contains(t, first, "ip")
	assert.Contains(t, first, "ip_trusted")

	stats := decode(t, owner.do(http.MethodGet, "/api/admin/audit/stats", nil))
	require.Contains(t, stats, "totals")
	assert.NotEmpty(t, stats["days"])

	vocab := decode(t, owner.do(http.MethodGet, "/api/admin/audit/vocabulary", nil))
	assert.Len(t, vocab["actions"].([]any), len(auth.AuditActions()))
	assert.Len(t, vocab["windows"].([]any), 3)
}

// An unknown action would run a full backward scan of the window to return
// nothing — the cheapest way for a caller to make the database work.
func TestAuditAPI_RefusesAFilterOutsideTheVocabulary(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	for path, code := range map[string]string{
		"/api/admin/audit?action=link.invented": "invalid_action",
		"/api/admin/audit?window=10y":           "invalid_window",
		"/api/admin/audit?category=secret":      "invalid_category",
		"/api/admin/audit?limit=9999":           "invalid_limit",
		"/api/admin/audit?before=abc":           "invalid_cursor",
	} {
		rec := owner.do(http.MethodGet, path, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, "path %s", path)
		assert.Contains(t, rec.Body.String(), code, "path %s", path)
	}
}

// The CSV is opened later on an administrator's own machine, and the trail
// records values an UNAUTHENTICATED caller controls.
func TestAuditAPI_ExportIsCSVWithFormulasDefused(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	// A failed sign-in writes the ATTEMPTED address verbatim, and that path
	// deliberately never validates it.
	h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "=HYPERLINK(\"http://evil\")@x.test", "password": "wrong",
	})

	rec := owner.do(http.MethodGet, "/api/admin/audit/export.csv", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/csv")
	assert.Contains(t, rec.Header().Get("Content-Disposition"), "attachment")
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	body := rec.Body.String()
	assert.Contains(t, body, "id,created_at,action")
	assert.NotContains(t, body, "subject", "the administrative export must not carry content labels")
	// Lowercased because the failed-login path normalizes the attempted
	// address before recording it — the defusing is what this asserts, not the
	// casing.
	assert.Contains(t, body, `"'=hyperlink`, "a formula must be quoted as text")
}

// Every rail, at the HTTP layer, answering with the code that names it. An
// operator told only "invalid" in front of a control that can make the instance
// unreachable cannot act on it.
func TestAuditAPI_BlockRailsAnswerWithTheReasonTheyFired(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	for _, tc := range []struct{ ip, code string }{
		// The harness's client dials from this address, so it is the one the
		// self rail has to catch — the rail that fires in practice, because the
		// operator investigating a burst is often behind the same NAT as it.
		{"192.0.2.10", "block_self"},
		{"127.0.0.1", "block_loopback"},
		{"::1", "block_loopback"},
		{testTrustedProxyIP, "block_proxy"},
		{"not-an-ip", "invalid_ip"},
		{"10.0.0.0/8", "invalid_ip"},
	} {
		rec := owner.do(http.MethodPost, "/api/admin/audit/blocks",
			map[string]string{"ip": tc.ip, "reason": "test"})
		require.NotEqual(t, http.StatusCreated, rec.Code, "address %q must be refused", tc.ip)
		assert.Contains(t, rec.Body.String(), tc.code, "address %q", tc.ip)
	}
}

func TestAuditAPI_BlockRoundTripsAndIsAudited(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	rec := owner.do(http.MethodPost, "/api/admin/audit/blocks",
		map[string]string{"ip": "189.42.11.7", "reason": "brute force"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	blocks := decode(t, owner.do(http.MethodGet, "/api/admin/audit/blocks", nil))
	require.Len(t, blocks["blocks"].([]any), 1)

	// The blocklist is authority over who reaches the instance, so installing
	// one is itself an identity event the trail has to carry.
	trail := decode(t, owner.do(http.MethodGet,
		"/api/admin/audit?action="+auth.AuditIPBlocked, nil))
	require.Len(t, trail["entries"].([]any), 1)

	require.Equal(t, http.StatusNoContent,
		owner.do(http.MethodDelete, "/api/admin/audit/blocks/189.42.11.7", nil).Code)
	// Idempotent: "not blocked" is the state the caller asked for.
	require.Equal(t, http.StatusNoContent,
		owner.do(http.MethodDelete, "/api/admin/audit/blocks/189.42.11.7", nil).Code)
}

func TestAuditAPI_UnblockRefusesAMalformedAddress(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	rec := owner.do(http.MethodDelete, "/api/admin/audit/blocks/not-an-ip", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// The caller reading their OWN rows. Not an admin route: it needs no
// administrative permission and must work for a viewer.
func TestActivityAPI_ServesTheCallerTheirOwnRowsWithSubjects(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	me := decode(t, owner.do(http.MethodGet, "/api/auth/me", nil))
	uid := int64(me["user"].(map[string]any)["id"].(float64))
	repo := auth.NewRepository(h.pool)
	id := authctx.UserID(uid)
	require.NoError(t, repo.Audit(context.Background(), auth.AuditRecord{
		Action: auth.AuditLinkCreated, ActorID: &id,
		Subject: "My private bookmark", EntityKind: "link", EntityID: ptr(int64(9)),
	}))

	body := decode(t, owner.do(http.MethodGet, "/api/activity", nil))
	entries := body["entries"].([]any)
	require.Len(t, entries, 1)
	assert.Equal(t, "My private bookmark", entries[0].(map[string]any)["subject"])

	// And the SAME row, read through the administrative projection, carries no
	// label at all — the two answers are the read split.
	admin := decode(t, owner.do(http.MethodGet,
		"/api/admin/audit?action="+auth.AuditLinkCreated, nil))
	row := admin["entries"].([]any)[0].(map[string]any)
	assert.NotContains(t, row, "subject")
	assert.Nil(t, row["actor_email"])
	assert.Equal(t, float64(uid), row["actor_ref"])
}

func TestActivityAPI_RefusesABadCursorOrLimit(t *testing.T) {
	h := newHarness(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	assert.Equal(t, http.StatusBadRequest, owner.do(http.MethodGet, "/api/activity?before=abc", nil).Code)
	assert.Equal(t, http.StatusBadRequest, owner.do(http.MethodGet, "/api/activity?limit=9999", nil).Code)
}
