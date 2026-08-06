package logsafe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// newTestLogger returns a logger writing JSON through the redactor, plus the
// buffer it writes into.
func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := NewRedactHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return slog.New(h), &buf
}

const secret = "s3cr3t-do-not-log-me"

func assertRedacted(t *testing.T, buf *bytes.Buffer, key string) {
	t.Helper()
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("%s leaked into the log: %s", key, out)
	}
	if !strings.Contains(out, Redacted) {
		t.Fatalf("%s was dropped rather than marked redacted: %s", key, out)
	}
}

// wantRedacted is written out by hand, NOT derived from sensitiveKeys.
//
// Ranging over the map would make the test a restatement of the code: deleting
// an entry would delete its own test case and stay green, while CLAUDE.md §4
// enumerates these keys as locked behaviour. Spelling them out means a removal
// fails here, and the parity check below means an addition cannot be smuggled
// in unlisted either.
var wantRedacted = []string{
	"password", "current_password", "new_password", "master_password",
	"code", "otp", "recovery_code",
	"token", "raw_token", "access_token", "refresh_token", "csrf_token",
	"pre_auth", "unlock_token",
	"secret", "secret_base32", "code_verifier", "state",
	"authorization", "cookie", "set-cookie",
	"sub", "email", "api_key",
}

func TestEverySensitiveKeyIsRedacted(t *testing.T) {
	t.Parallel()
	for _, key := range wantRedacted {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			logger, buf := newTestLogger()
			logger.Info("attempt", key, secret)
			assertRedacted(t, buf, key)
		})
	}
}

// Both directions: the hand-written list and the package's map must agree, so
// neither a silent removal nor a silent addition survives review.
func TestRedactionListMatchesTheDocumentedSet(t *testing.T) {
	t.Parallel()
	want := make(map[string]struct{}, len(wantRedacted))
	for _, k := range wantRedacted {
		want[k] = struct{}{}
	}
	for k := range sensitiveKeys {
		if _, ok := want[k]; !ok {
			t.Errorf("sensitiveKeys has %q, which is not in the documented list "+
				"— add it to wantRedacted and to CLAUDE.md §4", k)
		}
	}
	for k := range want {
		if _, ok := sensitiveKeys[k]; !ok {
			t.Errorf("%q is documented as redacted but missing from sensitiveKeys", k)
		}
	}
}

func TestKeyMatchingIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"Password", "PASSWORD", "Refresh_Token", "Set-Cookie"} {
		logger, buf := newTestLogger()
		logger.Info("attempt", key, secret)
		assertRedacted(t, buf, key)
	}
}

// Ordinary attributes must survive untouched — a redactor that blanks
// everything is useless, and the failure would be invisible until someone
// needed the log.
func TestNonSensitiveAttributesPassThrough(t *testing.T) {
	t.Parallel()
	logger, buf := newTestLogger()
	logger.Info("login", "user_id", 42, "status", "active", "dur_ms", 17)

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if got["user_id"] != float64(42) || got["status"] != "active" || got["dur_ms"] != float64(17) {
		t.Fatalf("ordinary attributes were altered: %v", got)
	}
	if strings.Contains(buf.String(), Redacted) {
		t.Fatal("a benign record was redacted")
	}
}

// The trap a naive implementation falls into.
//
// `logger.With("token", raw)` stores the attribute ONCE and replays it on every
// subsequent record. A handler that only cleaned Handle would pass the obvious
// test and then leak the value on every line for the life of that logger.
func TestWithAttrsIsRedacted(t *testing.T) {
	t.Parallel()
	logger, buf := newTestLogger()
	scoped := logger.With("refresh_token", secret, "user_id", 7)

	scoped.Info("first")
	scoped.Info("second")

	assertRedacted(t, buf, "refresh_token")
	if strings.Count(buf.String(), Redacted) != 2 {
		t.Fatalf("the stored attribute was not redacted on every record: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"user_id":7`) {
		t.Fatal("the benign stored attribute was lost")
	}
}

// A grouped key is still the same credential. `auth.password` must redact as
// readily as `password`, or nesting becomes an accidental bypass.
func TestGroupedKeysAreRedacted(t *testing.T) {
	t.Parallel()

	t.Run("group value", func(t *testing.T) {
		logger, buf := newTestLogger()
		logger.Info("login", slog.Group("auth", "password", secret, "user_id", 3))
		assertRedacted(t, buf, "auth.password")
		if !strings.Contains(buf.String(), `"user_id":3`) {
			t.Fatal("the sibling attribute inside the group was lost")
		}
	})

	t.Run("WithGroup", func(t *testing.T) {
		logger, buf := newTestLogger()
		logger.WithGroup("auth").Info("login", "code", secret)
		assertRedacted(t, buf, "auth.code")
	})

	t.Run("nested groups", func(t *testing.T) {
		logger, buf := newTestLogger()
		logger.Info("x", slog.Group("a", slog.Group("b", "secret", secret)))
		assertRedacted(t, buf, "a.b.secret")
	})
}

// A type that renders itself through LogValue could hand back a credential
// under a harmless-looking key. Resolving before the check is what stops that.
type credentialBag struct{ raw string }

func (c credentialBag) LogValue() slog.Value {
	return slog.GroupValue(slog.String("password", c.raw), slog.Int("user_id", 5))
}

func TestLogValuerIsResolvedBeforeRedacting(t *testing.T) {
	t.Parallel()
	logger, buf := newTestLogger()
	logger.Info("login", "principal", credentialBag{raw: secret})

	assertRedacted(t, buf, "principal.password")
	if !strings.Contains(buf.String(), `"user_id":5`) {
		t.Fatal("the resolved group lost its benign field")
	}
}

// Enabled must delegate, or the wrapper silently changes which levels are
// emitted — a redactor that also swallows debug output is a different bug
// wearing the same coat.
func TestEnabledDelegatesToTheInnerHandler(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	h := NewRedactHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info should be disabled when the inner handler is warn-level")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("error should be enabled")
	}
}

// A nil inner handler is a wiring mistake, and it must fail where the mistake
// is. Returning a typed-nil would produce a NON-nil slog.Handler interface that
// slog.New accepts, moving the crash to the first log call — possibly an error
// path in production.
func TestNilInnerPanicsAtConstruction(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("a nil inner handler must panic at construction")
		}
	}()
	_ = NewRedactHandler(nil)
}

func TestIsSensitive(t *testing.T) {
	t.Parallel()
	for _, k := range []string{"password", "Password", "auth.password", "a.b.token", "SET-COOKIE"} {
		if !IsSensitive(k) {
			t.Errorf("IsSensitive(%q) = false", k)
		}
	}
	// Near-misses must NOT match: over-redaction hides the fields an operator
	// actually needs during an incident.
	for _, k := range []string{"password_changed", "token_version", "has_password",
		"email_verified_at", "status", "user_id", "codepoint"} {
		if IsSensitive(k) {
			t.Errorf("IsSensitive(%q) = true, which over-redacts", k)
		}
	}
}
