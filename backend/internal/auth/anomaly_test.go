package auth

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func at(min int) time.Time {
	return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC).Add(time.Duration(min) * time.Minute)
}

// The panel is scanned, not read: the worst line has to be first, and among
// equally bad lines the most recent one is the one an operator is acting on.
func TestRankAnomalies_OrdersBySeverityThenRecency(t *testing.T) {
	in := []Anomaly{
		{Kind: AnomalyKindHammer, IP: "198.51.100.1", Severity: AnomalySeverityWarn, LastSeen: at(50)},
		{Kind: AnomalyKindSpray, IP: "203.0.113.7", Severity: AnomalySeverityCritical, LastSeen: at(10)},
		{Kind: AnomalyKindThrottle, IP: "203.0.113.9", Severity: AnomalySeverityCritical, LastSeen: at(40)},
		{Kind: AnomalyKindSpray, IP: "192.0.2.4", Severity: AnomalySeverityWarn, LastSeen: at(60)},
	}
	out := rankAnomalies(in, 100, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	require.Len(t, out, 4)
	assert.Equal(t, []string{"203.0.113.9", "203.0.113.7", "192.0.2.4", "198.51.100.1"},
		[]string{out[0].IP, out[1].IP, out[2].IP, out[3].IP})
}

// One origin can trip more than one rule, and collapsing them would hide the
// distinction the operator is deciding on — a spray and a lockout from the same
// address are two different facts.
func TestRankAnomalies_KeepsOneRowPerIPAndKind(t *testing.T) {
	in := []Anomaly{
		{Kind: AnomalyKindSpray, IP: "203.0.113.7", Severity: AnomalySeverityCritical, LastSeen: at(10)},
		{Kind: AnomalyKindHammer, IP: "203.0.113.7", Severity: AnomalySeverityCritical, LastSeen: at(11)},
		{Kind: AnomalyKindThrottle, IP: "203.0.113.7", Severity: AnomalySeverityCritical, LastSeen: at(12)},
	}
	out := rankAnomalies(in, 100, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	assert.Len(t, out, 3)
}

// Truncating in silence turns "there are nine anomalies" into a fact the screen
// asserts and the data does not support. The cap stays; the LINE is what makes
// the cut visible to whoever reads the logs during the incident.
func TestRankAnomalies_LogsWhatItCut(t *testing.T) {
	var buf bytes.Buffer
	in := make([]Anomaly, 0, 5)
	for i := 0; i < 5; i++ {
		in = append(in, Anomaly{Kind: AnomalyKindSpray, IP: "203.0.113.7",
			Severity: AnomalySeverityWarn, LastSeen: at(i)})
	}
	out := rankAnomalies(in, 2, slog.New(slog.NewTextHandler(&buf, nil)))
	require.Len(t, out, 2)
	assert.Contains(t, buf.String(), "anomaly")
	assert.Contains(t, buf.String(), "3", "the line must say how many rows were dropped")
}

func TestRankAnomalies_SaysNothingWhenNothingWasCut(t *testing.T) {
	var buf bytes.Buffer
	out := rankAnomalies([]Anomaly{{Kind: AnomalyKindSpray, IP: "203.0.113.7"}}, 2,
		slog.New(slog.NewTextHandler(&buf, nil)))
	assert.Len(t, out, 1)
	assert.Empty(t, buf.String(), "a warning that fires when nothing is wrong is one people learn to ignore")
}

// THE assertion of this file. The attacked mailbox is identity data that
// already lives on the trail's own timeline, behind its own read split
// (INV-175). Repeating the list here would create a SECOND surface returning
// it — one that no projection guards — which is exactly the multiplication
// INV-175 went to the trouble of reducing to one.
func TestAnomaly_CarriesNoEmailAnywhereInItsJSON(t *testing.T) {
	a := Anomaly{
		Kind: AnomalyKindSpray, IP: "203.0.113.7", IPTrusted: false,
		DistinctAccounts: 12, Failures: 40, Throttles: 0,
		FirstSeen: at(0), LastSeen: at(9), Blocked: false,
		Severity: AnomalySeverityCritical,
	}
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "@",
		"no field of an anomaly may carry an address; only the COUNT of accounts")

	// And the SHAPE is closed: a field added later that happens to hold an
	// e-mail would slip past the assertion above whenever the fixture leaves it
	// empty, so the key set itself is pinned.
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	keys := make([]string, 0, len(decoded))
	for k := range decoded {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{
		"kind", "ip", "ip_trusted", "distinct_accounts", "failures", "throttles",
		"first_seen", "last_seen", "blocked", "severity",
	}, keys, "the anomaly payload's shape is a contract the screen is built against")
	for _, k := range keys {
		assert.False(t, strings.Contains(k, "email"), "field %q names an identity", k)
	}
}

// A count that crosses the threshold is a warning; one that doubles it is not
// the same event, and a screen that painted both the same colour would make the
// operator find the real one by reading rather than by looking.
func TestAnomalySeverity_EscalatesAtTwiceTheThreshold(t *testing.T) {
	assert.Equal(t, AnomalySeverityWarn, anomalySeverity(5, 5))
	assert.Equal(t, AnomalySeverityWarn, anomalySeverity(9, 5))
	assert.Equal(t, AnomalySeverityCritical, anomalySeverity(10, 5))
	assert.Equal(t, AnomalySeverityCritical, anomalySeverity(400, 5))
	// A zero or negative threshold cannot make everything critical: the policy
	// bounds forbid it, and a guard here is what keeps a future widening of
	// those bounds from silently repainting the whole panel.
	assert.Equal(t, AnomalySeverityWarn, anomalySeverity(1, 0))
}

// A closed vocabulary for parseAuditFilter's reason: an arbitrary window is an
// arbitrary amount of scanning an administrator can ask the database for. An
// ABSENT one is the configured window, not a hard-coded default — the owner
// already answered that question when they saved the policy.
func TestAnomalyWindow_IsAClosedVocabularyAndFallsBackToThePolicy(t *testing.T) {
	d, label, ok := anomalyWindow("1h", 15)
	require.True(t, ok)
	assert.Equal(t, time.Hour, d)
	assert.Equal(t, "1h", label)

	d, label, ok = anomalyWindow("", 45)
	require.True(t, ok)
	assert.Equal(t, 45*time.Minute, d, "an absent window is the configured one")
	assert.Equal(t, "45m", label)

	_, _, ok = anomalyWindow("30s", 15)
	assert.False(t, ok, "an arbitrary window is an arbitrary amount of scanning to ask for")
}
