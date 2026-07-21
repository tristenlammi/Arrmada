package books

import "testing"

// TestMatchRelease locks in release→book routing: word-boundary title matching (no
// substring bleed between "Dune" and "Dune Messiah"), author disambiguation when
// several titles match, longest-title preference, and short titles like "It".
func TestMatchRelease(t *testing.T) {
	lib := []Book{
		{ID: 1, Title: "Dune", Author: "Frank Herbert"},
		{ID: 2, Title: "Dune Messiah", Author: "Frank Herbert"},
		{ID: 3, Title: "The Stand", Author: "Stephen King"},
		{ID: 4, Title: "The Standoff", Author: "Jane Doe"},
		{ID: 5, Title: "It", Author: "Stephen King"},
	}
	cases := []struct {
		name    string
		release string
		wantID  int64
		wantOK  bool
	}{
		{"dune release routes to Dune, not Messiah", "Frank Herbert - Dune (1965) EPUB", 1, true},
		{"messiah release routes to Dune Messiah, not Dune", "Frank Herbert - Dune Messiah (1969) EPUB", 2, true},
		{"messiah without author still picks the longer title", "Dune.Messiah.1969.Retail.EPUB", 2, true},
		{"dotted separators keep word boundaries", "Frank.Herbert.Dune.1965.EPUB", 1, true},
		{"the stand routes to The Stand", "Stephen King - The Stand (Complete & Uncut) EPUB", 3, true},
		{"the standoff routes to The Standoff", "Jane Doe - The Standoff (2021) EPUB", 4, true},
		{"short title It matches on word boundary", "Stephen King - It (1986) EPUB", 5, true},
		{"'it' inside another word is not a match", "Digital Fortress 2004 EPUB", 0, false},
		{"no library book in the name", "Andy Weir - The Martian (2014) EPUB", 0, false},
		{"empty release matches nothing", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := matchRelease(lib, tc.release)
			if ok != tc.wantOK {
				t.Fatalf("matchRelease(%q) ok = %v, want %v", tc.release, ok, tc.wantOK)
			}
			if ok && got.ID != tc.wantID {
				t.Fatalf("matchRelease(%q) = %q (id %d), want id %d", tc.release, got.Title, got.ID, tc.wantID)
			}
		})
	}
}

// TestMatchReleaseAuthorDisambiguation: when two books share a title, the one whose
// author appears in the release wins.
func TestMatchReleaseAuthorDisambiguation(t *testing.T) {
	lib := []Book{
		{ID: 1, Title: "It", Author: "Stephen King"},
		{ID: 2, Title: "It", Author: "Alexa Chung"},
	}
	got, ok := matchRelease(lib, "Alexa Chung - It (2013) EPUB")
	if !ok || got.ID != 2 {
		t.Fatalf("want Alexa Chung's It (id 2), got ok=%v id=%d", ok, got.ID)
	}
	got, ok = matchRelease(lib, "Stephen King - It (1986) EPUB")
	if !ok || got.ID != 1 {
		t.Fatalf("want Stephen King's It (id 1), got ok=%v id=%d", ok, got.ID)
	}
}

// TestWordKey documents the boundary-preserving normalization the matcher rests on.
func TestWordKey(t *testing.T) {
	cases := map[string]string{
		"Dune Messiah":              "dune messiah",
		"Frank.Herbert-Dune_(1965)": "frank herbert dune 1965",
		"  The   Stand  ":           "the stand",
		"IT":                        "it",
		"!!!":                       "",
		"L'Étranger":                "l tranger", // non-ASCII dropped, boundary kept
	}
	for in, want := range cases {
		if got := wordKey(in); got != want {
			t.Errorf("wordKey(%q) = %q, want %q", in, got, want)
		}
	}
}
