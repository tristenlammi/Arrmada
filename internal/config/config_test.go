package config

import "testing"

// ARRMADA_BASE_URL is no longer an option. A leftover sub-path value is kept only so
// startup can warn about it; empty and "/" already meant the root, so they don't warn.
func TestBaseURLIgnored(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want string
	}{
		{"", ""},
		{"/", ""},
		{" // ", ""},
		{"/arrmada", "/arrmada"},
		{"arrmada/", "arrmada/"},
	} {
		t.Setenv("ARRMADA_BASE_URL", tc.val)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.BaseURLIgnored != tc.want {
			t.Errorf("ARRMADA_BASE_URL=%q: BaseURLIgnored = %q, want %q", tc.val, c.BaseURLIgnored, tc.want)
		}
	}
}
