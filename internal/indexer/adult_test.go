package indexer

import "testing"

func TestIsAdultRelease(t *testing.T) {
	adult := []string{
		"Blacked Hope Heaven Pull Chapter 5 Flight (12.06.2026) rq.mp4",
		"Vixen.Hope.Heaven.1080p",
		"LegalPorno Hailey Hope (11.05.2026) rq.mp4",
		"Reality.Kings.Something.2026",
		"Brazzers - Whatever 2026",
		"Some.Movie.2026.720p.gangbang",
	}
	for _, s := range adult {
		if !isAdultRelease(s, nil) {
			t.Errorf("isAdultRelease(%q) = false, want true", s)
		}
	}

	// Mainstream titles that must NOT be flagged (word-boundary safety).
	clean := []string{
		"Hope 2026 1080p WEB-DL x265",
		"Romance at Hope Ranch 2026 1080p WEB-DL H264-NoRBiT",
		"Analyze This 1999 1080p BluRay",   // "anal" is a prefix, not a word
		"xXx Return of Xander Cage 2017",   // the Vin Diesel film — bare token ok, but "xxx" word...
		"Wicked 2024 2160p",                // studio "wicked pictures" not bare "wicked"
		"The Deeper You Dig 2019 1080p",    // "deeper" isn't in the list
		"Popcorn 1991 1080p",               // contains "corn", not "porn"
	}
	for _, s := range clean {
		if isAdultRelease(s, nil) {
			t.Errorf("isAdultRelease(%q) = true, want false", s)
		}
	}

	// XXX indexer category (Newznab/Torznab 6000–6999) is adult regardless of title.
	if !isAdultRelease("Ambiguous Title 2026", []int{6010}) {
		t.Error("category 6010 should be treated as adult")
	}
	if isAdultRelease("Ambiguous Title 2026", []int{2040}) {
		t.Error("category 2040 (HD movies) must not be adult")
	}
}
