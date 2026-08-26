package backupagent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAlertRulesOnlyReferenceSeriesTheAgentServes pins the shipped alert
// rules to the live registry. An alert whose series was renamed fires never —
// silence wearing a green badge — and no reviewer diffs a YAML file against a
// Go constructor reliably. The absent() rule is the one this matters most
// for: it exists to catch the agent that never came up, and it can only do
// that if the series name it watches is the one a healthy agent serves.
func TestAlertRulesOnlyReferenceSeriesTheAgentServes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "observability", "prometheus-alerts.yml"))
	require.NoError(t, err, "the alert rules ship in-repo; a moved file must move this test with it")

	referenced := regexp.MustCompile(`foldex_backup_[a-z_]+`).FindAllString(string(raw), -1)
	require.NotEmpty(t, referenced, "no foldex series in the rules file means the file is not the one this test guards")

	m := NewMetrics()
	// Touch every job label so all declared series render.
	m.ObserveSuccess(JobDump, time.Now(), time.Second, 1)
	m.ObserveFailure(JobDrill, time.Second, 1)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer tk")
	rec := httptest.NewRecorder()
	m.Handler("tk").ServeHTTP(rec, req)
	served := rec.Body.String()

	for _, series := range referenced {
		assert.Contains(t, served, series,
			"alert rule references %q but the agent registry does not serve it", series)
	}
}
