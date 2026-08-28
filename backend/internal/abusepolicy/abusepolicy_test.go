package abusepolicy

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestDefault_IsInsideItsOwnBounds(t *testing.T) {
	// A default outside its own advertised range would be an instance that
	// boots into a state its own screen refuses to save.
	if err := Default().ValidateForWrite(); err != nil {
		t.Fatalf("the default policy must be writable: %v", err)
	}
}

func TestBounds_DescribeEveryField(t *testing.T) {
	// Bounds is what the form renders from. A knob missing here is a field the
	// screen cannot show limits for, and the TypeScript would grow a second
	// copy of the numbers — the copy that goes stale.
	var p Policy
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, b := range Bounds() {
		got[b.Field] = true
	}
	for name := range shape {
		if !got[name] {
			t.Errorf("field %q is in the JSON payload but has no Bound; add it to fields()", name)
		}
	}
	if len(got) != len(shape) {
		t.Errorf("Bounds() describes %d fields, the payload has %d", len(got), len(shape))
	}
}

func TestValidateForWrite_RefusesBothDirections(t *testing.T) {
	// Two-sided on purpose: too high stops being a control, too low BECOMES the
	// attack. A test that only checked the low end would pass on a limiter that
	// can be set to a million.
	for _, b := range Bounds() {
		for _, tc := range []struct {
			label string
			value int
		}{
			{"below the floor", b.Min - 1},
			{"above the ceiling", b.Max + 1},
		} {
			p := Default()
			set(t, &p, b.Field, tc.value)
			err := p.ValidateForWrite()
			if err == nil {
				t.Errorf("%s %s (%d) must be refused", b.Field, tc.label, tc.value)
				continue
			}
			// The message is returned verbatim to the screen, so it has to name
			// the field and the real numbers rather than say "invalid".
			msg := err.Error()
			for _, want := range []string{b.Field, strconv.Itoa(b.Min), strconv.Itoa(b.Max)} {
				if !strings.Contains(msg, want) {
					t.Errorf("%s: message %q must contain %q", b.Field, msg, want)
				}
			}
		}
		// The boundaries themselves are legal.
		for _, v := range []int{b.Min, b.Max} {
			p := Default()
			set(t, &p, b.Field, v)
			if err := p.ValidateForWrite(); err != nil {
				t.Errorf("%s = %d is the boundary and must be accepted: %v", b.Field, v, err)
			}
		}
	}
}

func TestSanitize_RevertsOneKnobAndKeepsTheRest(t *testing.T) {
	// This is the INV-169 lesson applied by construction: a value out of range
	// must not take the neighbouring settings down with it. internal/policy
	// answers a failed Validate with the whole default document, and that cost
	// an instance its Google allowlist when an unrelated bound was tightened.
	stored := Default()
	stored.LoginFailuresPerAccount = 7
	stored.APIWritesPerMinute = 900
	stored.LoginDistinctAccountsPerIP = 100000 // out of range

	got := stored.Sanitize()

	if got.LoginDistinctAccountsPerIP != Default().LoginDistinctAccountsPerIP {
		t.Errorf("the out-of-range knob must revert, got %d", got.LoginDistinctAccountsPerIP)
	}
	if got.LoginFailuresPerAccount != 7 {
		t.Errorf("a neighbouring knob must survive, got %d", got.LoginFailuresPerAccount)
	}
	if got.APIWritesPerMinute != 900 {
		t.Errorf("a neighbouring knob must survive, got %d", got.APIWritesPerMinute)
	}
}

func TestSanitize_AbsentTakesTheDefaultExceptWhereZeroMeansOff(t *testing.T) {
	// A document written by an older binary carries zeros for knobs it never
	// had. Those must resolve to the baseline, not to "no limit at all" — a
	// zero-valued quota read literally is either an open door or a closed one,
	// and both are worse than the default.
	var empty Policy
	got := empty.Sanitize()
	if !sameAs(got, Default()) {
		t.Errorf("an empty document must resolve to the default, got %s", render(got))
	}

	// PublicClickCoalesceSeconds is the exception: 0 is a real, supported
	// setting there (coalescing off), so it must NOT be rewritten. This is the
	// case a plain int could not express — absent and off would be the same
	// bytes, and one of them has to lose.
	off := Default()
	zero := 0
	off.PublicClickCoalesceSeconds = &zero
	if got := off.Sanitize(); got.ClickCoalesceSeconds() != 0 {
		t.Errorf("0 means off for public_click_coalesce_seconds and must survive Sanitize, got %d",
			got.ClickCoalesceSeconds())
	}
}

func TestSanitize_IsIdempotent(t *testing.T) {
	// Read, write, read again must not drift. The policy is re-Sanitized on
	// every cache refresh; a non-idempotent pass would walk the value.
	weird := Policy{LoginDistinctAccountsPerIP: -4, APIExpensivePerHour: 999999}
	once := weird.Sanitize()
	if twice := once.Sanitize(); !sameAs(twice, once) {
		t.Errorf("Sanitize must be idempotent:\n once=%s\ntwice=%s", render(once), render(twice))
	}
}

func TestEnforcementFloorsAreNotOne(t *testing.T) {
	// The floor of an enforcement knob is the whole reason the screen cannot
	// build the denial-of-service it exists to prevent. At 1, one wrong
	// password throttles the origin — and behind a NAT the origin is a
	// building. Named explicitly so lowering it is a deliberate act.
	if MinLoginDistinctAccountsPerIP < 2 {
		t.Errorf("MinLoginDistinctAccountsPerIP = %d would let the screen lock out a whole NAT on one typo",
			MinLoginDistinctAccountsPerIP)
	}
	if MinLoginFailuresPerAccount < 3 {
		t.Errorf("MinLoginFailuresPerAccount = %d is below the number of typos a real person makes",
			MinLoginFailuresPerAccount)
	}
}

// set writes a field by its JSON name, so the table-driven tests above walk
// Bounds() instead of repeating the nine field names a third time.
func set(t *testing.T, p *Policy, jsonName string, v int) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m[jsonName]; !ok {
		t.Fatalf("unknown field %q", jsonName)
	}
	m[jsonName] = v
	raw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, p); err != nil {
		t.Fatal(err)
	}
}

// sameAs compares by encoded VALUE, not by struct identity: Policy holds a
// pointer, and == on it would compare addresses — two documents with the same
// settings would read as different, and the test would pass for the wrong
// reason after any refactor that stops sharing the pointer.
func sameAs(a, b Policy) bool { return render(a) == render(b) }

func render(p Policy) string {
	b, err := json.Marshal(p)
	if err != nil {
		return err.Error()
	}
	return string(b)
}
