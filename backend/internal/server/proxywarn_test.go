package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"foldex/internal/config"
)

// The two boot messages about TRUSTED_PROXY_IPS are not decoration: they are
// the ONLY way an operator discovers that their proxy configuration is wrong.
// The symptom otherwise is per-IP rate limits behaving as one global bucket,
// which looks like an unrelated bug and is close to undiagnosable from the
// outside.
//
// Building the router is enough to trigger them — no database, no handlers.
func bootLog(t *testing.T, cfg config.Config) string {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	_ = New(Deps{Logger: logger, Config: cfg})
	return buf.String()
}

// A misspelt entry is skipped, and skipping it silently would leave the
// operator believing a proxy is trusted when it is not.
func TestBootReportsUnparseableTrustedProxyEntries(t *testing.T) {
	t.Parallel()
	out := bootLog(t, config.Config{
		BindAddr:        "127.0.0.1",
		TrustedProxyIPs: "10.0.0.1, nginx, 999.999.999.999",
	})

	for _, entry := range []string{"nginx", "999.999.999.999"} {
		if !strings.Contains(out, entry) {
			t.Errorf("the unparseable entry %q was dropped without a word: %s", entry, out)
		}
	}
	if !strings.Contains(out, "NOT trusted") {
		t.Errorf("the message does not say what the consequence is: %s", out)
	}
	// It must be an ERROR: this is misconfiguration, not information.
	if !strings.Contains(out, `"level":"ERROR"`) {
		t.Errorf("unparseable entries should be logged at ERROR: %s", out)
	}
}

// Empty on a network-reachable bind is the shipped-and-broken shape: nginx in
// front, nobody told the backend, every request attributed to the proxy.
func TestBootWarnsWhenTrustedProxiesAreEmptyOnANetworkBind(t *testing.T) {
	t.Parallel()
	out := bootLog(t, config.Config{BindAddr: "0.0.0.0:9089", TrustedProxyIPs: ""})

	if !strings.Contains(out, "TRUSTED_PROXY_IPS is empty") {
		t.Fatalf("no warning on a non-loopback bind with no trusted proxies: %s", out)
	}
	if !strings.Contains(out, `"level":"WARN"`) {
		t.Errorf("expected WARN level: %s", out)
	}
}

// A loopback bind has nothing in front of it, so the warning would be noise —
// and a warning that fires on the correct default is a warning people learn to
// ignore.
func TestBootIsSilentOnALoopbackBind(t *testing.T) {
	t.Parallel()
	out := bootLog(t, config.Config{BindAddr: "127.0.0.1", TrustedProxyIPs: ""})
	if strings.Contains(out, "TRUSTED_PROXY_IPS is empty") {
		t.Fatalf("warned about a loopback bind, where there is no proxy to trust: %s", out)
	}
}

// Configured correctly: no complaint at all.
func TestBootIsSilentWhenTrustedProxiesAreConfigured(t *testing.T) {
	t.Parallel()
	out := bootLog(t, config.Config{
		BindAddr:        "0.0.0.0:9089",
		TrustedProxyIPs: "172.16.0.0/12",
	})
	if strings.Contains(out, "TRUSTED_PROXY_IPS") {
		t.Fatalf("complained about a valid configuration: %s", out)
	}
}

// The entry is operator-supplied and reaches a structured log, so it goes
// through logsafe: a value carrying CR/LF could otherwise forge log lines.
func TestBootSanitisesTheReportedEntry(t *testing.T) {
	t.Parallel()
	out := bootLog(t, config.Config{
		BindAddr:        "127.0.0.1",
		TrustedProxyIPs: "bad\nENTRY",
	})

	// One JSON object per line: a forged newline would produce a second.
	lines := 0
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line is not valid JSON, so something forged it: %q", l)
		}
		lines++
	}
	if lines != 1 {
		t.Fatalf("expected exactly one log record, got %d: %s", lines, out)
	}
}
