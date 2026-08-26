package backupagent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scrape(t *testing.T, m *Metrics, token, auth string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	m.Handler(token).ServeHTTP(rec, req)
	body, err := io.ReadAll(rec.Result().Body)
	require.NoError(t, err)
	return rec.Code, string(body)
}

func TestMetricsHandler_TokenContractMatchesTheBackend(t *testing.T) {
	m := NewMetrics()

	code, _ := scrape(t, m, "", "Bearer anything")
	assert.Equal(t, http.StatusServiceUnavailable, code,
		"empty token is 503, never an anonymous metrics endpoint")

	code, _ = scrape(t, m, "sekret", "")
	assert.Equal(t, http.StatusUnauthorized, code)
	code, _ = scrape(t, m, "sekret", "Bearer wrong")
	assert.Equal(t, http.StatusUnauthorized, code)

	code, body := scrape(t, m, "sekret", "Bearer sekret")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "go_goroutines")
}

func TestMetrics_ObserveWritesTheSeriesTheAlertRulesWatch(t *testing.T) {
	m := NewMetrics()
	finished := time.Date(2026, 8, 25, 3, 31, 0, 0, time.UTC)

	m.ObserveFailure(JobDump, 2*time.Second, 3)
	_, body := scrape(t, m, "tk", "Bearer tk")
	assert.Contains(t, body, `foldex_backup_consecutive_failures{job="dump"} 3`)
	assert.Contains(t, body, `foldex_backup_runs_total{job="dump",status="failed"} 1`)

	m.ObserveSuccess(JobDump, finished, 5*time.Second, 12345)
	_, body = scrape(t, m, "tk", "Bearer tk")
	assert.Contains(t, body, `foldex_backup_consecutive_failures{job="dump"} 0`,
		"a success must reset the consecutive counter or the alert never clears")
	assert.Contains(t, body, `foldex_backup_artifact_bytes{job="dump"} 12345`)
	assert.Contains(t, body, `foldex_backup_last_success_timestamp_seconds{job="dump"} 1.78`,
		"the staleness alert compares time() against this gauge (prometheus renders the value in scientific notation)")
	assert.Contains(t, body, `foldex_backup_runs_total{job="dump",status="succeeded"} 1`)
}

func TestMetrics_SeedPrimesStalenessAcrossRestarts(t *testing.T) {
	m := NewMetrics()
	m.SeedLastSuccess(JobDump, time.Time{})
	_, body := scrape(t, m, "tk", "Bearer tk")
	assert.False(t, strings.Contains(body, `foldex_backup_last_success_timestamp_seconds{job="dump"}`),
		"never-succeeded must stay ABSENT so the absent() alert can see it — seeding zero would silence it")

	m.SeedLastSuccess(JobDump, time.Date(2026, 8, 24, 3, 30, 0, 0, time.UTC))
	_, body = scrape(t, m, "tk", "Bearer tk")
	assert.Contains(t, body, `foldex_backup_last_success_timestamp_seconds{job="dump"}`)
}
