package logsafe

import (
	"context"
	"log/slog"
	"strings"
)

// Redacted is what replaces a sensitive value. A marker rather than an empty
// string, so a reader can tell "this was withheld" from "this was absent" —
// the two mean very different things when reading an auth trace.
const Redacted = "[redacted]"

// sensitiveKeys are the attribute names whose VALUES never belong in a log.
//
// Matching is by key, case-insensitively, and on the last path segment of a
// grouped key, so `auth.password` redacts as readily as `password`.
//
// The list is deliberately about CREDENTIALS, not privacy in general: nearly
// every entry grants access if read. `sub` is here because an OAuth subject is
// a stable cross-service identifier, and `email` because SDD §9.2 asks for
// user_id in its place at info level — an auth log full of addresses is a
// ready-made target list.
//
// `code` is the one entry that over-redacts: it is the natural attribute name
// for an OTP and equally natural for an error code. It stays, because a leaked
// second factor costs more than a lost diagnostic, and the single benign call
// site in the tree (internal/storage) was renamed to `s3_error_code` instead.
// A new "code" attribute meaning something harmless should be named for what it
// is rather than argued out of this list.
var sensitiveKeys = map[string]struct{}{
	"password":           {},
	"current_password":   {},
	"new_password":       {},
	"master_password":    {},
	"code":               {},
	"otp":                {},
	"recovery_code":      {},
	"token":              {},
	"raw_token":          {},
	"access_token":       {},
	"refresh_token":      {},
	"csrf_token":         {},
	"pre_auth":           {},
	"unlock_token":       {},
	"secret":             {},
	"secret_base32":      {},
	"client_secret":      {},
	"code_verifier":      {},
	"api_token":          {},
	"temporary_password": {},
	"state":              {},
	"authorization":      {},
	"cookie":             {},
	"set-cookie":         {},
	"sub":                {},
	"email":              {},
	"api_key":            {},
}

// IsSensitive reports whether an attribute key names a credential.
func IsSensitive(key string) bool {
	k := strings.ToLower(key)
	if i := strings.LastIndexByte(k, '.'); i >= 0 {
		k = k[i+1:]
	}
	_, ok := sensitiveKeys[k]
	return ok
}

// RedactHandler wraps a slog.Handler and blanks the value of every attribute
// whose key names a credential.
//
// It is a HANDLER rather than a rule each call site follows, and that is the
// whole point. Nothing in the tree logs a secret today; this exists so that the
// next `logger.Info("login", "password", p)` — written in a hurry, during an
// incident, by someone debugging — is inert instead of permanent. A convention
// only holds while everyone remembers it, and log statements are added at
// exactly the moments when nobody is thinking about log hygiene.
//
// The same reasoning put SweepTouch on a ticker instead of trusting every
// revocation path to call forgetTouch: bounded by construction beats bounded by
// discipline.
//
// SCOPE, stated plainly because the guarantee is narrower than it looks. This
// matches on the attribute KEY, so two things still pass through untouched:
// the record MESSAGE (`logger.Info("login failed for "+password)`), and a
// struct logged whole under a harmless key (`slog.Any("input", loginInput{…})`
// serialises its fields by name, and this never sees them). Neither is
// reachable by a key rule; both remain the call site's responsibility.
type RedactHandler struct {
	inner slog.Handler
}

// NewRedactHandler wraps inner, and PANICS on a nil one.
//
// Returning a typed-nil *RedactHandler would not help: assigning it to the
// slog.Handler interface produces a NON-nil interface value, so slog.New
// accepts it happily and the dereference lands on the first log call — which
// could be an error path in production, long after the wiring mistake. Failing
// at construction puts the panic where the mistake is.
func NewRedactHandler(inner slog.Handler) *RedactHandler {
	if inner == nil {
		panic("logsafe: NewRedactHandler requires a non-nil handler")
	}
	return &RedactHandler{inner: inner}
}

func (h *RedactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *RedactHandler) Handle(ctx context.Context, r slog.Record) error {
	// Rebuild rather than mutate: slog.Record's attrs are not safe to edit in
	// place, and a Record may be shared once it has been cloned.
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

// WithAttrs must redact too.
//
// Attributes attached with `logger.With("token", raw)` are stored once and
// emitted on every subsequent record, so a handler that only cleaned Handle
// would leak the value repeatedly while looking correct in the obvious test.
func (h *RedactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		cleaned = append(cleaned, redactAttr(a))
	}
	return &RedactHandler{inner: h.inner.WithAttrs(cleaned)}
}

func (h *RedactHandler) WithGroup(name string) slog.Handler {
	return &RedactHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr blanks a sensitive attribute, recursing into groups.
func redactAttr(a slog.Attr) slog.Attr {
	// Resolve LogValuer first: a type whose LogValue() returns a group of
	// credentials would otherwise slip past the key check entirely, because at
	// this point its key is just the struct's field name.
	v := a.Value.Resolve()

	if IsSensitive(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	if v.Kind() == slog.KindGroup {
		src := v.Group()
		out := make([]slog.Attr, 0, len(src))
		for _, g := range src {
			out = append(out, redactAttr(g))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
