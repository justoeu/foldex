package clampint

import "testing"

func TestInt(t *testing.T) {
	cases := []struct {
		s    string
		def  int
		lo   int
		hi   int
		want int
	}{
		{"", 60, 1, 365, 60},        // empty → default
		{"abc", 60, 1, 365, 60},     // parse failure → default
		{"30", 60, 1, 365, 30},      // in-range stays
		{"-5", 60, 1, 365, 1},       // below lo → clamp to lo
		{"999999", 60, 1, 365, 365}, // above hi → clamp to hi
		{"0", 60, 1, 365, 1},        // zero is below lo=1
		{"1", 60, 1, 365, 1},        // boundary low
		{"365", 60, 1, 365, 365},    // boundary high
		{" 30 ", 60, 1, 365, 60},    // strconv.Atoi rejects whitespace → default
	}
	for _, tc := range cases {
		t.Run(tc.s, func(t *testing.T) {
			got := Int(tc.s, tc.def, tc.lo, tc.hi)
			if got != tc.want {
				t.Fatalf("Int(%q, def=%d, [%d,%d]) = %d; want %d", tc.s, tc.def, tc.lo, tc.hi, got, tc.want)
			}
		})
	}
}
