package automation

import "testing"

// The check has to tolerate how releases actually write titles — punctuation, case and
// accents all differ freely from metadata, and none of that is a real disagreement.
// Crying wolf on those would make the warning worthless.
func TestTitlesAlikeToleratesFormatting(t *testing.T) {
	same := [][2]string{
		{"Gin It Up", "Gin It Up!"},
		{"Toms Divorce", "Tom's Divorce"},
		{"Doppelgangers", "Doppelgängers"},
		{"Li l Sebastian", "Li'l Sebastian"},
		{"One in 8 000", "One in 8,000"},
		{"Ms Knope Goes to Washington", "Ms. Knope Goes to Washington"},
		// Releases truncate long titles — a prefix is not a disagreement.
		{"The Johnny Karate Super Awesome", "The Johnny Karate Super Awesome Musical Explosion Show"},
	}
	for _, p := range same {
		if !titlesAlike(p[0], p[1]) {
			t.Errorf("titlesAlike(%q, %q) = false — formatting differences are not disagreements", p[0], p[1])
		}
	}
}

// But a genuinely different episode must be caught — that's the whole point.
func TestTitlesAlikeCatchesRealMismatches(t *testing.T) {
	differ := [][2]string{
		{"The Pawnee-Eagleton Tip-Off Classic", "Doppelgängers"},
		{"Doppelgängers", "Gin It Up!"},
		{"Filibuster", "Recall Vote"},
		{"One in 8,000", "Moving Up"},
	}
	for _, p := range differ {
		if titlesAlike(p[0], p[1]) {
			t.Errorf("titlesAlike(%q, %q) = true — these are different episodes", p[0], p[1])
		}
	}
}

// An empty side means there's nothing to compare, which must not produce a warning.
func TestTitlesAlikeIgnoresEmpty(t *testing.T) {
	for _, p := range [][2]string{{"", "Doppelgängers"}, {"Doppelgängers", ""}, {"", ""}} {
		if !titlesAlike(p[0], p[1]) {
			t.Errorf("titlesAlike(%q, %q) should be quiet when there's nothing to compare", p[0], p[1])
		}
	}
}
