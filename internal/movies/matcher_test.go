package movies

import "testing"

// Matcher must reproduce MatchRelease/Match: normalized-title equality with the year within
// ±1, and it indexes the library once for many lookups (the downloads feed's hot path).
func TestMatcherTitleAndYear(t *testing.T) {
	match := (&Service{}).Matcher([]Movie{
		{ID: 1, Title: "The Odyssey", Year: 2026},
		{ID: 2, Title: "Dune", Year: 2021},
	})
	if got, ok := match("the odyssey", 2026); !ok || got.ID != 1 {
		t.Errorf("normalized title should match, got %+v/%v", got, ok)
	}
	if got, ok := match("Dune", 2022); !ok || got.ID != 2 {
		t.Errorf("year within ±1 should match, got %+v/%v", got, ok)
	}
	if _, ok := match("Dune", 2025); ok {
		t.Error("a year more than 1 off must not match")
	}
	if _, ok := match("Nothing Here", 0); ok {
		t.Error("an unknown title must not match")
	}
}
