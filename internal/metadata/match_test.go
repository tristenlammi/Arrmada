package metadata

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

func mTitle(r MovieResult) string { return r.Title }
func mYear(r MovieResult) int      { return r.Year }

// TestTitleYearMatchReported reproduces the mis-matches the user hit: a folder
// with no year must not grab the more-popular same-word show.
func TestTitleYearMatchReported(t *testing.T) {
	untamed := []MovieResult{
		{TMDBID: 1, Title: "The Untamed", Year: 2019}, // popular — was wrongly picked
		{TMDBID: 2, Title: "Untamed", Year: 2025},     // the real match
	}
	loveDeath := []MovieResult{
		{TMDBID: 3, Title: "Love, Death & Robots", Year: 2019}, // was wrongly picked
		{TMDBID: 4, Title: "Love & Death", Year: 2023},         // the real match
	}

	cases := []struct {
		folder  string
		results []MovieResult
		wantID  int
	}{
		{"UNTAMED", untamed, 2},
		{"Love & Death", loveDeath, 4},
	}
	for _, c := range cases {
		rel := parser.Parse(c.folder) // scan uses the parsed folder title/year
		got, ok := TitleYearMatch(c.results, rel.Title, rel.Year, mTitle, mYear)
		if !ok {
			t.Errorf("%q: no match (parsed title %q year %d)", c.folder, rel.Title, rel.Year)
			continue
		}
		if got.TMDBID != c.wantID {
			t.Errorf("%q → %q (id %d), want id %d", c.folder, got.Title, got.TMDBID, c.wantID)
		}
	}
}

func TestTitleYearMatchDisambiguatesByYear(t *testing.T) {
	results := []MovieResult{
		{TMDBID: 1, Title: "Dune", Year: 1984},
		{TMDBID: 2, Title: "Dune", Year: 2021},
	}
	got, ok := TitleYearMatch(results, "Dune", 2021, mTitle, mYear)
	if !ok || got.TMDBID != 2 {
		t.Errorf("Dune 2021 → id %d ok=%v, want id 2", got.TMDBID, ok)
	}
}

func TestTitleYearMatchNoConfidentMatch(t *testing.T) {
	results := []MovieResult{{TMDBID: 1, Title: "Completely Different Show", Year: 2019}}
	if _, ok := TitleYearMatch(results, "My Obscure Show", 0, mTitle, mYear); ok {
		t.Error("expected no match for an unrelated title with no year")
	}
}
