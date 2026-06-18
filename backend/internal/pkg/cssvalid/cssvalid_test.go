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

func TestSanitize(t *testing.T) {
	const fb = "#6366F1"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty falls back", "", fb},
		{"whitespace falls back", "   ", fb},
		{"tracking-pixel url() falls back", `red url("https://evil/exfil")`, fb},
		{"named color falls back", "red", fb},
		{"expression() falls back", "expression(alert(1))", fb},
		{"valid hex passes through", "#abc", "#abc"},
		{"valid gradient passes through", "linear-gradient(135deg, #8B85FF, #6366F1)", "linear-gradient(135deg, #8B85FF, #6366F1)"},
		{"whitespace trimmed on valid", "  #abc  ", "#abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sanitize(tc.in, fb); got != tc.want {
				t.Fatalf("Sanitize(%q, %q) = %q; want %q", tc.in, fb, got, tc.want)
			}
		})
	}
}
