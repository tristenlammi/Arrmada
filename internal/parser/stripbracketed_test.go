package parser

import "testing"

func TestStripBracketed(t *testing.T) {
	cases := []struct{ in, want string }{
		{"My Hero Academia (Boku no Hero Academia)", "My Hero Academia"},
		{"Attack on Titan [Shingeki no Kyojin]", "Attack on Titan"},
		{"Nested (a (b) c) end", "Nested  end"},
		{"Unbalanced (open", "Unbalanced"},
		{"No brackets here", "No brackets here"},
		{"", ""},
	}
	for _, c := range cases {
		if got := StripBracketed(c.in); got != c.want {
			t.Errorf("StripBracketed(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
