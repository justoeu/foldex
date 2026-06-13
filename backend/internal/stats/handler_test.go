package stats

import "testing"

// TestClampInt locks the P5.2 fix. Without the clamp, `?days=2147483647`
// lands in `generate_series(now() - 2.1e9 * interval '1 day', ...)` — the
// planner attempts it and the request takes the DB down (auth-gated DoS).
func TestClampInt(t *testing.T) {
	cases := []struct {
		s    string
		def  int
		min  int
		max  int
		want int
	}{
		{"", 60, 1, 365, 60},        // empty → default
		{"abc", 60, 1, 365, 60},     // parse failure → default
		{"30", 60, 1, 365, 30},      // in-range stays
		{"-5", 60, 1, 365, 1},       // below min → clamp to min
		{"999999", 60, 1, 365, 365}, // above max → clamp to max
		{"0", 60, 1, 365, 1},        // zero is below min=1
		{"1", 60, 1, 365, 1},        // boundary low
		{"365", 60, 1, 365, 365},    // boundary high
		{" 30 ", 60, 1, 365, 60},    // strconv.Atoi rejects whitespace → default
	}
	for _, tc := range cases {
		t.Run(tc.s, func(t *testing.T) {
			got := clampInt(tc.s, tc.def, tc.min, tc.max)
			if got != tc.want {
				t.Fatalf("clampInt(%q, def=%d, [%d,%d]) = %d; want %d", tc.s, tc.def, tc.min, tc.max, got, tc.want)
			}
		})
	}
}
