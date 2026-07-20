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

// An empty side is NO EVIDENCE, not a match. This used to return true ("don't cry wolf")
// when it only drove a warning; now it decides where a file is placed, and an episode
// whose title is punctuation only ("!!!") normalizes to nothing — treating that as alike
// made it match every file in the season and collect them all.
func TestEmptyTitlesAreNotAMatch(t *testing.T) {
	for _, p := range [][2]string{{"", "Doppelgängers"}, {"Doppelgängers", ""}, {"", ""}, {"Doppelgängers", "!!!"}, {"...", "Doppelgängers"}} {
		if titlesAlike(p[0], p[1]) {
			t.Errorf("titlesAlike(%q, %q) = true — an empty key must never act as a wildcard", p[0], p[1])
		}
	}
}

// A short prefix is not evidence. "Go" is a prefix of "Go Big or Go Home", and acting on
// that would move a file onto an episode it has nothing to do with — the exact failure a
// tolerant rule invites once it starts deciding placement rather than logging.
func TestShortPrefixesAreNotAMatch(t *testing.T) {
	tooShort := [][2]string{
		{"Go", "Go Big or Go Home"},
		{"The", "The Wall"},
		{"Part 1", "Part 1 of a Longer Name"},
		{"Jam", "Jam Session Extravaganza"},
	}
	for _, p := range tooShort {
		if titlesAlike(p[0], p[1]) {
			t.Errorf("titlesAlike(%q, %q) = true — too little of the title to identify an episode", p[0], p[1])
		}
	}

	// A genuine truncation carries enough of the title to be unambiguous.
	if !titlesAlike("The Johnny Karate Super Awesome", "The Johnny Karate Super Awesome Musical Explosion Show") {
		t.Error("a long truncation should still match — releases shorten titles routinely")
	}
}
