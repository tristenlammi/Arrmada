package httpapi

import "testing"

func TestValidatePlexImagePath(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		// Rejected — the SSRF / traversal vectors from the security review.
		{"traversal to sessions", "/library/../status/sessions", "", false},
		{"query param", "/library/metadata/1/thumb?x=y", "", false},
		{"double traversal to accounts", "/photo/../../accounts", "", false},
		{"outside namespace", "/etc/passwd", "", false},
		{"scheme-relative host", "//evil", "", false},
		{"empty", "", "", false},
		{"fragment", "/library/metadata/1/thumb#x", "", false},
		{"not rooted", "library/metadata/1/thumb", "", false},
		{"token injection", "/library/metadata/1/thumb?X-Plex-Token=abc", "", false},

		// Accepted — real thumb/photo paths (no query, no dot-segments).
		{"episode thumb", "/library/metadata/123/thumb/1700000000", "/library/metadata/123/thumb/1700000000", true},
		{"photo path", "/photo/456/thumb", "/photo/456/thumb", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := validatePlexImagePath(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("validatePlexImagePath(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Fatalf("validatePlexImagePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClampWindow(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 0},                      // pass through → service applies its 30d default
		{-5, -5},                    // negative also falls through to the default
		{1, 1},                      // floor of an explicit positive
		{30, 30},                    // in range
		{365, 365},                  // at the cap
		{10_000_000, maxWindowDays}, // the DoS value is clamped
	}
	for _, tc := range cases {
		if got := clampWindow(tc.in); got != tc.want {
			t.Errorf("clampWindow(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestClampPageSize(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 0},                // pass through → service default
		{50, 50},              // in range
		{200, 200},            // at the cap
		{10_000, maxPageSize}, // clamped
	}
	for _, tc := range cases {
		if got := clampPageSize(tc.in); got != tc.want {
			t.Errorf("clampPageSize(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
