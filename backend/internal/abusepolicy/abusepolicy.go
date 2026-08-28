// Package abusepolicy holds the numbers this instance uses to decide when a
// caller is abusing it — how many distinct accounts one origin may fail against
// before it is throttled, how many writes a session may issue per minute, how
// long a repeated public hit is coalesced, and the thresholds the anomaly panel
// reports on.
//
// It is a leaf, for the same reason internal/policy is one: internal/auth and
// internal/server ENFORCE these values, so they import abusepolicy and
// abusepolicy imports nothing of them. Putting the numbers inside auth would
// have forced the administration handler that edits them — and the server
// middleware that reads them — to import the whole identity surface.
//
// # Why these are configurable at all
//
// docs/SDD-ABUSE-DEFENSE.md fixed the SHAPE of each control (what it counts,
// and against which key) and left the MAGNITUDE open, because magnitude is the
// one part that depends on the instance: five people on a home server and forty
// behind a corporate NAT are not the same traffic, and a number compiled into
// the binary would be wrong for one of them with no way to say so.
//
// # Why the bounds are two-sided, unlike internal/policy
//
// A password floor is only dangerous when it is too LOW, so its bound is a
// floor. A rate limit is dangerous in BOTH directions:
//
//   - Too high, and the control stops existing. An origin allowed 10 000 failed
//     accounts is not throttled, it is observed.
//   - Too low, and the control becomes the attack. A limiter set to one failed
//     account per hour hands anyone who can reach the login form the ability to
//     lock an office out of its own instance by typing one wrong password. The
//     SDD's own failure criterion says so: a limiter that locks out legitimate
//     users is worse than a loose one, because it denies service for free.
//
// So every enforcement knob carries a Min AND a Max, and both are refused at
// write time with a message that names the real numbers.
//
// # Why a bad value degrades one knob, not the document
//
// internal/policy answers a failed Validate with the WHOLE default document,
// and INV-169 records what that cost: tightening one bound would have made an
// instance configured above it silently lose its Google allowlist and its OTP
// lifetime too. Here the fallback is per FIELD — Sanitize replaces exactly the
// knob that is out of range and keeps every other one as written. A knob that
// drifts out of bounds is then a knob that reverts, never an instance that
// quietly reverts to baseline everywhere while its screen still shows the
// values the owner chose.
package abusepolicy

import "fmt"

// Bounds for the ENFORCEMENT knobs — the ones that refuse requests.
//
// The defaults are the values docs/SDD-ABUSE-DEFENSE.md argued for, except
// LoginDistinctAccountsPerIP, which the SDD proposed at 5 and which was raised
// to 10 before implementation: five is inside the range a genuinely confused
// office can reach on a bad morning (a shared machine, a stale saved password,
// two people helping a third), and the cost of being wrong in that direction is
// a locked-out building. Ten still stops a spray long before it is useful — an
// attacker walking a leaked credential list burns the budget in ten requests.
const (
	// MinLoginDistinctAccountsPerIP is not 1. At 1, a single wrong password —
	// which every human produces — would throttle the whole origin, and behind
	// a NAT that origin is a building. The floor exists so the screen cannot
	// build the exact denial-of-service the change was made to remove.
	MinLoginDistinctAccountsPerIP = 3
	MaxLoginDistinctAccountsPerIP = 100

	MinLoginFailuresPerAccount = 3
	MaxLoginFailuresPerAccount = 50

	MinLoginWindowMinutes = 5
	MaxLoginWindowMinutes = 1440

	// MinAPIWritesPerMinute is set where a legitimate burst still fits. Pasting
	// a folder of links, an import applying rows, a backup restore streaming
	// writes: these are real users doing normal things quickly, and the quota
	// exists to stop a loop, not a person in a hurry.
	MinAPIWritesPerMinute = 30
	MaxAPIWritesPerMinute = 6000

	MinAPIExpensivePerHour = 5
	MaxAPIExpensivePerHour = 1000

	// MinPublicClickCoalesceSeconds is 0, and 0 means OFF.
	//
	// Coalescing trades exactness of the click counter for removing a write
	// amplifier, and an operator may legitimately want the exact counter back:
	// nginx's limit_req still covers the surface, so turning this off weakens
	// one defence out of two rather than removing the last one. It is the only
	// knob here whose disabled state is a supported configuration.
	MinPublicClickCoalesceSeconds = 0
	MaxPublicClickCoalesceSeconds = 3600
)

// Bounds for the OBSERVATIONAL knobs — the anomaly panel's thresholds.
//
// These refuse nothing. They decide which rows the panel calls anomalous, and
// a wrong value produces a noisy screen or a quiet one, never a locked-out
// user. Their bounds are therefore only wide enough to keep the query sane,
// and deliberately looser than the enforcement knobs above: this is the
// difference between a number that acts and a number that reports, and the two
// should not be guarded as if they were the same thing.
const (
	MinAnomalySprayAccounts = 2
	MaxAnomalySprayAccounts = 1000

	MinAnomalyHammerFailures = 3
	MaxAnomalyHammerFailures = 1000

	MinAnomalyWindowMinutes = 5
	MaxAnomalyWindowMinutes = 10080 // one week
)

// Policy is the whole editable surface, and the shape the API returns.
type Policy struct {
	// LoginDistinctAccountsPerIP caps how many DISTINCT accounts one origin may
	// fail against inside LoginWindowMinutes.
	//
	// It counts breadth, not depth — the correction SDD §4.2 argued for. The
	// question this bucket exists to answer is "is this origin sweeping many
	// accounts?", and counting consecutive failures answered "is anyone at this
	// address getting passwords wrong?" instead, which is a different question
	// with a different right answer.
	LoginDistinctAccountsPerIP int `json:"login_distinct_accounts_per_ip"`

	// LoginFailuresPerAccount caps consecutive failures against ONE account,
	// from anywhere. This is the anti-brute-force control and it is unchanged
	// in shape by the SDD; only its magnitude became configurable.
	LoginFailuresPerAccount int `json:"login_failures_per_account"`

	// LoginWindowMinutes is the lockout duration shared by both login buckets.
	LoginWindowMinutes int `json:"login_window_minutes"`

	// APIWritesPerMinute caps mutating requests per authenticated principal.
	//
	// Per principal, not per route: a quota per route would let a caller hold
	// the whole pool by spreading a loop across twenty endpoints, each of them
	// individually well behaved.
	APIWritesPerMinute int `json:"api_writes_per_minute"`

	// APIExpensivePerHour caps the routes that do external I/O or per-request
	// CPU work — import, backup restore, screenshot capture, preview refresh.
	// They need a smaller ceiling than ordinary writes because one of them
	// costs far more than one row.
	APIExpensivePerHour int `json:"api_expensive_per_hour"`

	// PublicClickCoalesceSeconds suppresses repeat click rows from the same
	// visitor on the same entity inside this window. 0 disables coalescing.
	//
	// A POINTER, and it is the only one — for the same reason
	// backupagent.JobConfig.Enabled is one. Every other knob can treat zero as
	// "absent, use the default", because zero is not a meaningful setting for
	// any of them. Here it is: 0 means coalescing off, which is a supported
	// configuration. With a plain int, a document written by a binary that
	// predates this field would be byte-identical to an operator who chose to
	// turn it off, and Sanitize would have to guess which. nil is absent, and
	// a present 0 is a decision.
	PublicClickCoalesceSeconds *int `json:"public_click_coalesce_seconds"`

	// AnomalySprayAccounts is the panel's threshold for calling an origin a
	// spray: at least this many distinct accounts failed inside
	// AnomalyWindowMinutes.
	AnomalySprayAccounts int `json:"anomaly_spray_accounts"`

	// AnomalyHammerFailures is the panel's threshold for calling an origin a
	// hammer: at least this many failures against a SINGLE account.
	AnomalyHammerFailures int `json:"anomaly_hammer_failures"`

	// AnomalyWindowMinutes is the window both anomaly rules look back over.
	AnomalyWindowMinutes int `json:"anomaly_window_minutes"`
}

// Default is what an instance that never opens the screen runs.
func Default() Policy {
	return Policy{
		LoginDistinctAccountsPerIP: 10,
		LoginFailuresPerAccount:    5,
		LoginWindowMinutes:         15,
		APIWritesPerMinute:         120,
		APIExpensivePerHour:        20,
		PublicClickCoalesceSeconds: intp(10),
		AnomalySprayAccounts:       10,
		AnomalyHammerFailures:      20,
		AnomalyWindowMinutes:       15,
	}
}

// field is one knob, described once so validation, sanitisation and the API's
// bounds payload cannot drift apart.
//
// The alternative — a Validate with nine hand-written comparisons, a Sanitize
// with nine more, and a handler assembling the bounds JSON by hand — is three
// lists that must agree and no mechanism that makes them. The repo has already
// paid for that shape once: the audit vocabulary is a closed map for the same
// reason a prefix test looked like it decided something and did not.
type field struct {
	name   string
	get    func(*Policy) *int
	min    int
	max    int
	defval int
}

func fields() []field {
	d := Default()
	return []field{
		{"login_distinct_accounts_per_ip", func(p *Policy) *int { return &p.LoginDistinctAccountsPerIP },
			MinLoginDistinctAccountsPerIP, MaxLoginDistinctAccountsPerIP, d.LoginDistinctAccountsPerIP},
		{"login_failures_per_account", func(p *Policy) *int { return &p.LoginFailuresPerAccount },
			MinLoginFailuresPerAccount, MaxLoginFailuresPerAccount, d.LoginFailuresPerAccount},
		{"login_window_minutes", func(p *Policy) *int { return &p.LoginWindowMinutes },
			MinLoginWindowMinutes, MaxLoginWindowMinutes, d.LoginWindowMinutes},
		{"api_writes_per_minute", func(p *Policy) *int { return &p.APIWritesPerMinute },
			MinAPIWritesPerMinute, MaxAPIWritesPerMinute, d.APIWritesPerMinute},
		{"api_expensive_per_hour", func(p *Policy) *int { return &p.APIExpensivePerHour },
			MinAPIExpensivePerHour, MaxAPIExpensivePerHour, d.APIExpensivePerHour},
		{"anomaly_spray_accounts", func(p *Policy) *int { return &p.AnomalySprayAccounts },
			MinAnomalySprayAccounts, MaxAnomalySprayAccounts, d.AnomalySprayAccounts},
		{"anomaly_hammer_failures", func(p *Policy) *int { return &p.AnomalyHammerFailures },
			MinAnomalyHammerFailures, MaxAnomalyHammerFailures, d.AnomalyHammerFailures},
		{"anomaly_window_minutes", func(p *Policy) *int { return &p.AnomalyWindowMinutes },
			MinAnomalyWindowMinutes, MaxAnomalyWindowMinutes, d.AnomalyWindowMinutes},
	}
}

// Sanitize is the READ path: it replaces out-of-range knobs with their default
// and returns the rest exactly as stored.
//
// A zero value counts as "absent" and takes the default, which is what makes a
// document written by an older binary — one that had fewer knobs — resolve to
// the baseline for the fields it never knew about, instead of to zero. Zero is
// a real value for exactly one knob (PublicClickCoalesceSeconds, where it means
// off), so that knob is excluded from the absent-means-default rule and only
// range-checked.
func (p Policy) Sanitize() Policy {
	out := p
	for _, f := range fields() {
		v := f.get(&out)
		absent := *v == 0 && f.min > 0
		if absent || *v < f.min || *v > f.max {
			*v = f.defval
		}
	}
	out.PublicClickCoalesceSeconds = sanitizeCoalesce(p.PublicClickCoalesceSeconds)
	return out
}

// sanitizeCoalesce resolves the one nullable knob. nil is absent and takes the
// default; a present value is honoured when in range and reverted when not.
func sanitizeCoalesce(v *int) *int {
	if v == nil {
		return Default().PublicClickCoalesceSeconds
	}
	if *v < MinPublicClickCoalesceSeconds || *v > MaxPublicClickCoalesceSeconds {
		return Default().PublicClickCoalesceSeconds
	}
	return intp(*v)
}

// ClickCoalesceSeconds resolves the knob for callers, so no enforcement site
// has to remember that nil is a legal state. Always call this rather than
// dereferencing the field: a nil deref here would be a panic on the public
// /go path, reached by an anonymous visitor.
func (p Policy) ClickCoalesceSeconds() int {
	v := sanitizeCoalesce(p.PublicClickCoalesceSeconds)
	return *v
}

func intp(v int) *int { return &v }

// ValidateForWrite is the WRITE path: it refuses rather than clamps.
//
// Clamping a value the owner typed would show them a screen that disagrees with
// what they entered and never say why. The message names the real numbers
// because the API returns it verbatim and the UI renders it without rewriting —
// the same contract the password floor and the backup schedule already use.
func (p Policy) ValidateForWrite() error {
	for _, f := range fields() {
		v := *f.get(&p)
		if v < f.min || v > f.max {
			return fmt.Errorf("%s must be between %d and %d, got %d", f.name, f.min, f.max, v)
		}
	}
	// The nullable knob: absent on a write means "leave it at the default",
	// which is a legal document. A present value is bounded like any other.
	if v := p.PublicClickCoalesceSeconds; v != nil &&
		(*v < MinPublicClickCoalesceSeconds || *v > MaxPublicClickCoalesceSeconds) {
		return fmt.Errorf("public_click_coalesce_seconds must be between %d and %d, got %d",
			MinPublicClickCoalesceSeconds, MaxPublicClickCoalesceSeconds, *v)
	}
	return nil
}

// Bound is one knob's advertised range, for the screen that renders it.
type Bound struct {
	Field   string `json:"field"`
	Min     int    `json:"min"`
	Max     int    `json:"max"`
	Default int    `json:"default"`
}

// Bounds is what GET returns alongside the policy so the form can render its
// own limits instead of hard-coding a second copy of these numbers in
// TypeScript — the copy that would be the one to go stale.
func Bounds() []Bound {
	fs := fields()
	out := make([]Bound, 0, len(fs)+1)
	for _, f := range fs {
		out = append(out, Bound{Field: f.name, Min: f.min, Max: f.max, Default: f.defval})
	}
	out = append(out, Bound{
		Field:   "public_click_coalesce_seconds",
		Min:     MinPublicClickCoalesceSeconds,
		Max:     MaxPublicClickCoalesceSeconds,
		Default: Default().ClickCoalesceSeconds(),
	})
	return out
}
