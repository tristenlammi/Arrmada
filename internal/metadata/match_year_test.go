package metadata

import "testing"

type hit struct {
	title string
	year  int
}

func pick(results []hit, title string, year int) (hit, bool) {
	return TitleYearMatch(results, title, year,
		func(h hit) string { return h.title },
		func(h hit) int { return h.year })
}

// The year fallback used to accept ANY result within a year, with no title check — so a
// library folder could be silently re-identified as a different show, adopting its files
// and then downloading the wrong series' episodes into them.
func TestYearFallbackRequiresASimilarTitle(t *testing.T) {
	results := []hit{
		{"Love, Death & Robots", 2019},
		{"Love & Death", 2023},
	}

	// Exact title wins regardless of ordering.
	if got, ok := pick(results, "Love & Death", 2023); !ok || got.title != "Love & Death" {
		t.Fatalf("exact title match failed: %+v ok=%v", got, ok)
	}

	// A year-only coincidence must NOT match a different show.
	if got, ok := pick([]hit{{"Love, Death & Robots", 2019}}, "Love & Death", 2019); ok {
		t.Errorf("year alone must not match a different title, got %+v", got)
	}

	// A near-identical title with a close year still matches.
	if _, ok := pick([]hit{{"The Bear", 2022}}, "The Bear", 2023); !ok {
		t.Error("a near-identical title within a year should match")
	}
}

func TestTitlesClose(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"lovedeath", "lovedeathrobots", false}, // different shows
		{"thebear", "thebear2022", true},        // same show, year suffix
		{"thewire", "thewire", true},
		{"lost", "lostinspace", false},
		{"", "anything", false},
	}
	for _, c := range cases {
		if got := titlesClose(c.a, c.b); got != c.want {
			t.Errorf("titlesClose(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
