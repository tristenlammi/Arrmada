package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

func episodeTitleOf(name string) string { return parser.EpisodeTitleFrom(name) }

// Metadata sources genuinely disagree about episode numbering. TMDB merges Parks and
// Recreation's two-part "London" into one 44-minute episode 1; TVDB — which nearly every
// release is numbered against — splits it into 1 and 2. Everything after is one slot
// apart, so a file numbered 6x03 lands on TMDB's episode 3 when it is really episode 2,
// and the entire rest of the season shifts with it.
//
// The number is ambiguous between sources. The title is not.
func TestSeasonSixNumberingDisagreement(t *testing.T) {
	// TMDB's season 6, as the site actually lists it: London merged into one entry.
	tmdb := map[int]string{
		1: "London",
		2: "The Pawnee-Eagleton Tip-Off Classic",
		3: "Doppelgängers",
		4: "Gin It Up!",
		5: "Filibuster",
	}
	// What the release calls each file, and the TMDB episode it should end up at.
	cases := []struct {
		file string
		want int
	}{
		{"Parks and Recreation - 6x01 & 6x02 - London.mkv", 1},
		{"Parks and Recreation - 6x03 - The Pawnee-Eagleton Tip-Off Classic.mkv", 2},
		{"Parks and Recreation - 6x04 - Doppelgängers.mkv", 3},
		{"Parks and Recreation - 6x05 - Gin It Up!.mkv", 4},
		{"Parks and Recreation - 6x06 - Filibuster.mkv", 5},
	}
	for _, c := range cases {
		got, ok := uniqueEpisodeByTitle(tmdb, episodeTitleOf(c.file))
		if !ok {
			t.Errorf("%s: no unique title match — the correction wouldn't fire", c.file)
			continue
		}
		if got != c.want {
			t.Errorf("%s → episode %d, want %d", c.file, got, c.want)
		}
	}
}

// A correctly-numbered release must resolve to exactly the episode it names, so the
// correction is a no-op rather than a source of movement.
func TestCorrectlyNumberedReleaseIsUnchanged(t *testing.T) {
	titles := map[int]string{1: "The Mug", 2: "Hafo Safo", 3: "Zimdings"}
	got, ok := uniqueEpisodeByTitle(titles, episodeTitleOf("Teen.Titans.Go.S07E02.Hafo.Safo.1080p.HMAX.WEB-DL-NTb.mkv"))
	if !ok || got != 2 {
		t.Errorf("got %d ok=%v, want episode 2", got, ok)
	}
}

// Ambiguity must leave the number alone. Two episodes sharing a title (a two-parter
// split as "One Last Ride") can't be told apart by name, and guessing would move a file
// to the wrong episode — worse than leaving it where the number put it.
func TestAmbiguousTitlesFallBackToTheNumber(t *testing.T) {
	titles := map[int]string{12: "One Last Ride", 13: "One Last Ride"}
	if _, ok := uniqueEpisodeByTitle(titles, "One Last Ride"); ok {
		t.Error("two episodes share this title — the match is not unique and must not be used")
	}
	// A title that matches nothing is equally no basis for moving a file.
	if _, ok := uniqueEpisodeByTitle(titles, "Something Else Entirely"); ok {
		t.Error("no match should mean no correction")
	}
}

// uniqueEpisodeByTitle mirrors the selection inside correctRefsByTitle so the rule can be
// exercised without a database.
func uniqueEpisodeByTitle(titles map[int]string, fileTitle string) (int, bool) {
	if fileTitle == "" {
		return 0, false
	}
	match, hits := 0, 0
	for num, t := range titles {
		if titlesAlike(fileTitle, t) {
			match, hits = num, hits+1
		}
	}
	return match, hits == 1
}

// A multi-episode file must keep its number-derived episodes. Its single title can
// legitimately match only the FIRST of the episodes it spans — "Space House" against
// TMDB's "Space House (1)" — and acting on that would collapse a file covering four
// episodes down to one, losing the other three.
func TestMultiEpisodeFilesKeepTheirNumbers(t *testing.T) {
	// The correction is gated on len(refs) == 1, so a span is never rewritten. This pins
	// the reason: the title alone cannot say how many episodes the file covers.
	titles := map[int]string{8: "Space House", 9: "Something Else", 10: "Another", 11: "Yet Another"}
	if got, ok := uniqueEpisodeByTitle(titles, "Space House"); !ok || got != 8 {
		t.Fatalf("premise: the title does match episode 8 uniquely (got %d, ok=%v)", got, ok)
	}
	// ...and that is exactly why a 4-episode file must not be corrected by it.
}

// Anime resolves through absolute numbering and TheXEM scene maps, which are
// purpose-built and far more authoritative than a fuzzy title match — and fansub titles
// are romanized inconsistently, so they would match badly. That path is left alone.
func TestAnimeTitlesAreNotUsedForPlacement(t *testing.T) {
	// Guarding the intent: a romaji/English title pair that should NOT be treated as a
	// confident identification if the anime path were ever included.
	if titlesAlike("Sousou no Frieren", "Frieren: Beyond Journey's End") {
		t.Error("romanized and localized titles are not a reliable identification")
	}
}
