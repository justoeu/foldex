package auth

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The administrative projection is the enforcement of INV-045 for a table two
// readers share. `subject` is the one column holding another account's content,
// and this asserts against the SQL TEXT because that is where the guarantee
// lives: no filter applied in Go afterwards would survive someone adding the
// column back "to make the CSV richer".
func TestAdminProjectionNeverSelectsSubject(t *testing.T) {
	assert.NotContains(t, adminProjection, "subject",
		"the administrative trail must never read the content label — see INV-045")
	assert.NotContains(t, adminProjection, "entity_id",
		"a content row's id is owner-scoped too")
	assert.Contains(t, adminProjection, "actor_id",
		"the opaque actor reference is what replaces the name on a content row")
}

// A search term's wildcards belong to the server. Without escaping, "%" turns
// one filter into a scan of every row in the window — from an input box.
func TestLikeEscape_TreatsTheTermAsALiteral(t *testing.T) {
	assert.Equal(t, `%100\%%`, likeEscape("100%"))
	assert.Equal(t, `%a\_b%`, likeEscape("a_b"))
	assert.Equal(t, `%c:\\x%`, likeEscape(`c:\x`))
	assert.Equal(t, `%plain%`, likeEscape("plain"))
}

// An unbounded query is a backward scan of a ninety-day table whose bulk is
// failed logins, and the search predicate is not indexable. The window is what
// keeps a typed filter from reading all of it.
func TestAuditFilter_NormalizeAlwaysBoundsTheWindow(t *testing.T) {
	now := time.Now()
	f := AuditFilter{}.Normalize(now)
	assert.False(t, f.Since.IsZero(), "a filter with no window must not be unbounded")
	// The ceiling is the floor under a DIRECT repository call. Every HTTP
	// caller goes through parseAuditFilter, which always names one of three
	// windows — asserted separately below.
	assert.WithinDuration(t, now.Add(-AuditWindowCeiling), f.Since, time.Second)
	assert.Equal(t, 50, f.Limit)
}

func TestAuditFilter_NormalizeClampsTheLimit(t *testing.T) {
	for _, in := range []int{0, -1, maxAuditPageSize + 1, 1 << 30} {
		assert.Equal(t, 50, AuditFilter{Limit: in}.Normalize(time.Now()).Limit, "limit %d", in)
	}
	assert.Equal(t, 25, AuditFilter{Limit: 25}.Normalize(time.Now()).Limit)
	assert.Equal(t, maxAuditPageSize, AuditFilter{Limit: maxAuditPageSize}.Normalize(time.Now()).Limit)
}

// A long term is a long LIKE pattern, and the box is reachable by anyone who
// can read the trail.
func TestAuditFilter_NormalizeBoundsTheSearchTerm(t *testing.T) {
	f := AuditFilter{Search: "  " + strings.Repeat("x", 5000) + "  "}.Normalize(time.Now())
	assert.Len(t, f.Search, maxAuditSearch)
	assert.Equal(t, "abc", AuditFilter{Search: "  abc \n"}.Normalize(time.Now()).Search)
}

// The trail records values an UNAUTHENTICATED caller controls — the attempted
// address on a failed login and the User-Agent header on every row — and the
// CSV is opened later on an administrator's own machine. A leading =, +, - or @
// is executed as a formula by Excel, Sheets and LibreOffice.
func TestCSVSafe_DefusesSpreadsheetFormulas(t *testing.T) {
	for _, payload := range []string{
		`=HYPERLINK("http://evil","click")`,
		`+1+1`,
		`-2+3`,
		`@SUM(A1:A9)`,
	} {
		assert.Equal(t, "'"+payload, csvSafe(payload), "payload %q must be quoted as text", payload)
	}
}

func TestCSVSafe_LeavesOrdinaryValuesAlone(t *testing.T) {
	for _, v := range []string{"", "user@example.com", "203.0.113.9", "Mozilla/5.0", "role admin"} {
		assert.Equal(t, v, csvSafe(v), "value %q must not be rewritten", v)
	}
}

// An unknown action would otherwise run a full backward scan of the window to
// return nothing — the cheapest way for a caller to make the database work.
func TestParseAuditFilter_RefusesAnActionOutsideTheVocabulary(t *testing.T) {
	_, err := parseAuditFilter(httptest.NewRequest("GET", "/?action=link.invented", nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown audit action")

	f, err := parseAuditFilter(httptest.NewRequest("GET", "/?action=login.failed", nil))
	require.NoError(t, err)
	assert.Equal(t, AuditLoginFailed, f.Action)
}

// An arbitrary "since" is an arbitrary amount of scanning a caller can ask for.
func TestParseAuditFilter_WindowIsAClosedSet(t *testing.T) {
	_, err := parseAuditFilter(httptest.NewRequest("GET", "/?window=10y", nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "24h, 7d or 30d")

	for name, want := range auditWindows {
		f, err := parseAuditFilter(httptest.NewRequest("GET", "/?window="+name, nil))
		require.NoError(t, err, "window %q", name)
		assert.WithinDuration(t, time.Now().Add(-want), f.Since, 2*time.Second, "window %q", name)
	}
}

func TestParseAuditFilter_DefaultsToTheSevenDayWindow(t *testing.T) {
	f, err := parseAuditFilter(httptest.NewRequest("GET", "/", nil))
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(-auditWindows[auditWindowDefault]), f.Since, 2*time.Second)
}

func TestParseAuditFilter_RefusesAnUnknownCategory(t *testing.T) {
	_, err := parseAuditFilter(httptest.NewRequest("GET", "/?category=secret", nil))
	require.Error(t, err)
	for _, c := range []string{"", CategoryContent, CategoryIdentity} {
		_, err := parseAuditFilter(httptest.NewRequest("GET", "/?category="+c, nil))
		assert.NoError(t, err, "category %q", c)
	}
}

// Range-checked BEFORE the narrowing conversion: on a 32-bit build 2^32+50
// truncates to a perfectly plausible 50 that no later clamp can recognise.
func TestParseAuditFilter_RefusesAnOutOfRangeLimit(t *testing.T) {
	for _, raw := range []string{"-1", "201", "4294967346", "abc"} {
		_, err := parseAuditFilter(httptest.NewRequest("GET", "/?limit="+raw, nil))
		assert.Error(t, err, "limit %q must be refused", raw)
	}
}

// A sort order is a preference, not a filter: an unknown value falls back to
// the default rather than refusing the page.
func TestParseAuditFilter_OrderIsAPreferenceNotAFilter(t *testing.T) {
	f, err := parseAuditFilter(httptest.NewRequest("GET", "/?order=asc", nil))
	require.NoError(t, err)
	assert.True(t, f.Ascending)
	for _, raw := range []string{"", "desc", "oldest", "garbage"} {
		f, err := parseAuditFilter(httptest.NewRequest("GET", "/?order="+raw, nil))
		require.NoError(t, err, "order %q", raw)
		assert.False(t, f.Ascending, "order %q must fall back to newest-first", raw)
	}
}

func TestParseAuditFilter_RefusesABadCursor(t *testing.T) {
	for _, raw := range []string{"-5", "abc", "1.5"} {
		_, err := parseAuditFilter(httptest.NewRequest("GET", "/?before="+raw, nil))
		assert.Error(t, err, "cursor %q must be refused", raw)
	}
}

// The screen builds its filter row from this, so it renders exactly what the
// binary can produce instead of a list copied into the client that drifts the
// first time an action is added.
func TestAuditVocabularyPayload_CarriesEveryActionClassified(t *testing.T) {
	payload := auditActionsPayload()
	require.Len(t, payload, len(AuditActions()))
	for _, row := range payload {
		assert.True(t, KnownAuditAction(row["action"]), "action %q", row["action"])
		assert.Contains(t, []string{CategoryContent, CategoryIdentity}, row["category"])
		assert.Contains(t, []string{SeverityInfo, SeverityWarning, SeverityCritical}, row["severity"])
	}
}

// The export cap has to be a number a person can actually open, and one an
// investigation can work around with the window filters.
func TestAuditExportCap_IsASpreadsheetNotADump(t *testing.T) {
	assert.LessOrEqual(t, maxAuditExportRows, 50000)
	assert.GreaterOrEqual(t, maxAuditExportRows, 1000)
}
