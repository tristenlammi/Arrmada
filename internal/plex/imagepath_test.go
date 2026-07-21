package plex

import "testing"

func TestSafeImagePath(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		// Rejected: traversal, wrong namespace, query/fragment, scheme-relative.
		{"traversal to sessions", "/library/../status/sessions", "", false},
		{"query param injection", "/library/metadata/1/thumb?x=y", "", false},
		{"double traversal to accounts", "/photo/../../accounts", "", false},
		{"outside namespace", "/etc/passwd", "", false},
		{"scheme-relative host", "//evil", "", false},
		{"fragment", "/library/metadata/1/thumb#/status", "", false},
		{"empty", "", "", false},
		{"not rooted", "library/metadata/1/thumb", "", false},
		{"encoded token param", "/library/metadata/1/thumb?X-Plex-Token=abc", "", false},

		// Accepted: real Plex image paths.
		{"episode thumb", "/library/metadata/123/thumb/1700000000", "/library/metadata/123/thumb/1700000000", true},
		{"photo path", "/photo/123/thumb", "/photo/123/thumb", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := safeImagePath(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("safeImagePath(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Fatalf("safeImagePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
