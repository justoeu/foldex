package cssvalid

import "testing"

func TestIsValidColor(t *testing.T) {
	cases := []struct {
		c    string
		want bool
	}{
		{"#abc", true},
		{"#abcd", true},
		{"#aabbcc", true},
		{"#AABBCC", true},
		{"#aabbccdd", true},
		{"linear-gradient(135deg, #6366F1, #818CF8)", true},
		{"linear-gradient(135deg,#abc,#def)", true},
		{"linear-gradient( 135deg , #aabbcc , #ddeeff )", true},
		// Rejected: empty, named, RGB, HSL, URL-based, multi-stop, wrong angle.
		{"", false},
		{"red", false},
		{"rgb(0,0,0)", false},
		{"hsl(0,0%,0%)", false},
		{"#xyz", false},
		{"#1", false},
		{"#12345", false},
		{"#abcabc;background:url(x)", false},
		{"url(https://evil)", false},
		{`red url("https://evil/exfil")`, false},
		{"linear-gradient(90deg, #abc, #def)", false},
		{"linear-gradient(135deg, #abc, #def, #fff)", false},
		{"linear-gradient(135deg, red, blue)", false},
		{"expression(alert(1))", false},
		// Newlines in the gradient form must be rejected: a literal LF can
		// split the value across CSS declarations once concatenated.
		{"linear-gradient(135deg,\n#abc,\n#def)", false},
		{"linear-gradient(135deg,#abc,\n#def)", false},
	}
	for _, tc := range cases {
		t.Run(tc.c, func(t *testing.T) {
			got := IsValidColor(tc.c)
			if got != tc.want {
				t.Fatalf("IsValidColor(%q) = %v; want %v", tc.c, got, tc.want)
			}
		})
	}
}
