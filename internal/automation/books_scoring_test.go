package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/quality"
)

func bookRel(title string, seeders int) indexer.Release {
	return indexer.Release{Title: title, Seeders: seeders}
}

// TestPickBestBookKeyword: a keyword boost (GraphicAudio +100) wins over a plain
// release of the same format even with far fewer seeders, and a hard-reject term
// (abridged) is skipped entirely.
func TestPickBestBookKeyword(t *testing.T) {
	sp := quality.StoredProfile{
		FormatScores: map[string]int{"M4B": 40, "MP3": 25},
		Keywords:     []quality.Keyword{{Term: "graphicaudio", Score: 100}},
		Rejected:     []string{"abridged"},
	}
	releases := []indexer.Release{
		bookRel("Some Book M4B", 500),
		bookRel("Some Book GraphicAudio M4B", 10),
		bookRel("Some Book Abridged GraphicAudio M4B", 999), // rejected
	}
	best := pickBestBookForKind(sp, releases, "audiobook")
	if best == nil || best.Title != "Some Book GraphicAudio M4B" {
		t.Fatalf("got %v, want the GraphicAudio M4B", best)
	}
}

// TestPickBestBookKeywordInNarrator: a GraphicAudio preference must win even when
// "GraphicAudio" is only in the structured narrator field (as MyAnonaMouse
// presents it), not in the release title. The plain-narration M4B has far more
// seeders but must lose to the GraphicAudio one.
func TestPickBestBookKeywordInNarrator(t *testing.T) {
	sp := quality.StoredProfile{
		FormatScores: map[string]int{"M4B": 40},
		Keywords:     []quality.Keyword{{Term: "graphicaudio", Score: 100}},
	}
	releases := []indexer.Release{
		{Title: "Brandon Sanderson - Rhythm of War [M4B]", Narrator: "Kate Reading, Michael Kramer", Format: "M4B", Seeders: 1072},
		{Title: "Brandon Sanderson - Rhythm of War [M4B]", Narrator: "GraphicAudio", Format: "M4B", Seeders: 61},
	}
	best := pickBestBookForKind(sp, releases, "audiobook")
	if best == nil || best.Narrator != "GraphicAudio" {
		t.Fatalf("got %+v, want the GraphicAudio narration to win on the keyword preference", best)
	}
}

// TestPickBestBookPrefersComplete: a complete release beats a "(Part N of M)"
// split of the same format/keyword, even with fewer seeders — so a partial
// GraphicAudio set is never grabbed over the whole book.
func TestPickBestBookPrefersComplete(t *testing.T) {
	sp := quality.StoredProfile{
		FormatScores: map[string]int{"M4B": 40},
		Keywords:     []quality.Keyword{{Term: "graphicaudio", Score: 100}},
	}
	releases := []indexer.Release{
		{Title: "Rhythm of War (Part 1 of 6) [M4B]", Narrator: "GraphicAudio", Format: "M4B", Seeders: 500},
		{Title: "Rhythm of War [M4B]", Narrator: "GraphicAudio", Format: "M4B", Seeders: 40},
	}
	best := pickBestBookForKind(sp, releases, "audiobook")
	if best == nil || best.Title != "Rhythm of War [M4B]" {
		t.Fatalf("got %+v, want the complete release", best)
	}
}

func TestIsPartialBook(t *testing.T) {
	partial := []string{"Rhythm of War (Part 3 of 6) [M4B]", "Words of Radiance (2of5)", "Book (4 of 6)"}
	complete := []string{"Rhythm of War [M4B]", "Words of Radiance", "The Way of Kings"}
	for _, s := range partial {
		if !isPartialBook(s) {
			t.Errorf("isPartialBook(%q) = false, want true", s)
		}
	}
	for _, s := range complete {
		if isPartialBook(s) {
			t.Errorf("isPartialBook(%q) = true, want false", s)
		}
	}
}

// TestPickBestBookFormatPreference: with no keywords, the higher-scored format
// wins regardless of seeders.
func TestPickBestBookFormatPreference(t *testing.T) {
	sp := quality.StoredProfile{FormatScores: map[string]int{"M4B": 40, "MP3": 25}}
	releases := []indexer.Release{
		bookRel("Some Book MP3", 900),
		bookRel("Some Book M4B", 5),
	}
	best := pickBestBookForKind(sp, releases, "audiobook")
	if best == nil || best.Title != "Some Book M4B" {
		t.Fatalf("got %v, want M4B (higher format score)", best)
	}
}

func TestParseNarrator(t *testing.T) {
	cases := map[string]string{
		"The Way of Kings - Brandon Sanderson (Narrated by Michael Kramer and Kate Reading) M4B": "Michael Kramer and Kate Reading",
		"Project Hail Mary [Read by Ray Porter] MP3":                                             "Ray Porter",
		"Some Audiobook Narrator: Scott Brick 64kbps":                                            "Scott Brick",
		"The Way of Kings EPUB":                                                                  "",
	}
	for in, want := range cases {
		if got := parseNarrator(in); got != want {
			t.Errorf("parseNarrator(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPickBestBookEditionFilter: an audiobook pick ignores ebook-format releases.
func TestPickBestBookEditionFilter(t *testing.T) {
	sp := quality.StoredProfile{FormatScores: map[string]int{"EPUB": 40, "M4B": 40}}
	releases := []indexer.Release{
		bookRel("Some Book EPUB", 999), // ebook — not for the audiobook edition
		bookRel("Some Book M4B", 1),
	}
	best := pickBestBookForKind(sp, releases, "audiobook")
	if best == nil || best.Title != "Some Book M4B" {
		t.Fatalf("got %v, want the M4B audiobook", best)
	}
}
