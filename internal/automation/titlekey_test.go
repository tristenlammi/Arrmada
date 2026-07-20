package automation

import "testing"

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
