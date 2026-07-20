package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/movies"
)

// Releases are named in ASCII, so a library title carrying diacritics has to fold to the
// same key as its accent-free release name. unicode.IsLetter accepts 'é', so "Pokémon"
// kept its accent and matched nothing — the searcher folds the outbound query, found the
// releases, and then the title check discarded every one of them.
func TestTitleKeyFoldsAccents(t *testing.T) {
	same := [][2]string{
		{"Pokémon Heroes", "Pokemon Heroes"},
		{"Pokémon", "Pokemon"},
		{"Amélie", "Amelie"},
		{"Les Misérables", "Les Miserables"},
		{"Æon Flux", "Aeon Flux"},
		{"WALL·E", "WALL-E"},
	}
	for _, p := range same {
		if titleKey(p[0]) != titleKey(p[1]) {
			t.Errorf("titleKey(%q)=%q != titleKey(%q)=%q — should match",
				p[0], titleKey(p[0]), p[1], titleKey(p[1]))
		}
	}
}

// End-to-end: the real release names from the log, against the real library title.
// All six were being rejected as "wrong_title".
func TestReleaseIsForMovieAcceptsFoldedTitle(t *testing.T) {
	m := movies.Movie{Title: "Pokémon Heroes", Year: 2002}
	for _, rel := range []string{
		"Pokemon Heroes 2002 1080p BluRay x264-GROUP",
		"Pokemon.Heroes.2002.720p.WEB-DL.AAC2.0.H.264",
		"Pokémon Heroes 2002 2160p UHD BluRay x265",
	} {
		if !releaseIsForMovie(rel, m) {
			t.Errorf("release %q should match movie %q", rel, m.Title)
		}
	}
	// Folding must not make unrelated films collide.
	for _, rel := range []string{
		"Pokemon The First Movie 1998 1080p BluRay x264",
		"Pokemon Detective Pikachu 2019 1080p BluRay x264",
	} {
		if releaseIsForMovie(rel, m) {
			t.Errorf("release %q must NOT match movie %q", rel, m.Title)
		}
	}
}

// Releases spell "&" and "and" interchangeably, so both must reduce to the same key.
// Treating the ampersand as plain punctuation made "Love & Death" (lovedeath) and
// "Love and Death" (loveanddeath) different shows, and every release using the word form
// was rejected as belonging to something else.
func TestTitleKeyTreatsAmpersandAsAnd(t *testing.T) {
	same := [][2]string{
		{"Love & Death", "Love and Death"},
		{"Love & Death", "Love And Death"},
		{"Law & Order", "Law and Order"},
		{"Will & Grace", "Will.and.Grace"},
		{"Love & Death", "Love & Death"},
	}
	for _, p := range same {
		if titleKey(p[0]) != titleKey(p[1]) {
			t.Errorf("titleKey(%q)=%q != titleKey(%q)=%q — should match",
				p[0], titleKey(p[0]), p[1], titleKey(p[1]))
		}
	}

	// Genuinely different shows must stay different — this is the filter that stops
	// "Love, Death & Robots" being grabbed for "Love & Death".
	differ := [][2]string{
		{"Love & Death", "Love Death and Robots"},
		{"Love & Death", "Love, Death & Robots"},
		{"Below Deck", "Below Deck Mediterranean"},
	}
	for _, p := range differ {
		if titleKey(p[0]) == titleKey(p[1]) {
			t.Errorf("titleKey(%q) == titleKey(%q) — different shows must not collide", p[0], p[1])
		}
	}
}

// The end-to-end check: a real release name against a real library title.
func TestReleaseIsForSeriesAcceptsBothSpellings(t *testing.T) {
	for _, rel := range []string{
		"Love and Death S01 2160p MAX WEB-DL DDP5 1 HDR DoVi H 265-NTb",
		"Love & Death S01 2160p AMZN WEB-DL DDP5 1 Atmos H 265-XEBEC",
		"Love And Death S01E03 2160p MAX WEB-DL DDP5 1 DV HEVC-NTb",
	} {
		if !releaseIsForSeries(rel, "Love & Death") {
			t.Errorf("release %q should match series %q", rel, "Love & Death")
		}
	}
	// And still rejects the show that caused the confusion.
	for _, rel := range []string{
		"Love Death and Robots S04 1080p NF WEB-DL DDP5 1 Atmos DV HDR H 265-FLUX",
		"Love Death & Robots (2019) S01 1080p WEBRip 10bit EAC3 5 1 x265-iVy",
	} {
		if releaseIsForSeries(rel, "Love & Death") {
			t.Errorf("release %q must NOT match series %q", rel, "Love & Death")
		}
	}
}
