package series

import "testing"

// TitleMatcher must mirror MatchByTitle: normalized-title lookup, first-wins on a
// collision, indexing the library once for many torrents.
func TestTitleMatcher(t *testing.T) {
	match := (&Service{}).TitleMatcher([]Series{
		{ID: 1, Title: "Naruto"},
		{ID: 2, Title: "Naruto"}, // a duplicate title — the first must win, as MatchByTitle does
		{ID: 3, Title: "Bleach"},
	})
	if got, ok := match(NormTitle("Naruto")); !ok || got.ID != 1 {
		t.Errorf("first series with the title should win, got %+v/%v", got, ok)
	}
	if got, ok := match(NormTitle("Bleach")); !ok || got.ID != 3 {
		t.Errorf("Bleach should match, got %+v/%v", got, ok)
	}
	if _, ok := match(NormTitle("Unknown Show")); ok {
		t.Error("an unknown title must not match")
	}
}
