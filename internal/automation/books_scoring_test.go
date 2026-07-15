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
