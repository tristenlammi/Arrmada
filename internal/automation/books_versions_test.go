package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// While a book has a full-cast version, the standard audiobook never takes a release
// that belongs to it — and the version takes only those.
func TestVersionReleasesAreKeptApart(t *testing.T) {
	full := books.AudioVersion{ID: 7, Label: "Full cast", Terms: []string{"GraphicAudio", "full cast"}}
	b := books.Book{Title: "Empire of the Vampire", AudioVersions: []books.AudioVersion{full}}
	rels := []indexer.Release{
		{Title: "Jay Kristoff - Empire of the Vampire [M4B]", Narrator: "Damian Lynch"},
		{Title: "Empire of the Vampire [M4B]", Narrator: "GraphicAudio"},
		{Title: "Empire of the Vampire (Full Cast Dramatization) MP3"},
	}
	std := dropVersionReleases(b, rels)
	if len(std) != 1 || std[0].Narrator != "Damian Lynch" {
		t.Errorf("standard pool = %v, want only the Damian Lynch reading", titles(std))
	}
	if got := releasesForVersion(full, rels); len(got) != 2 {
		t.Errorf("version pool = %v, want the two full-cast releases", titles(got))
	}
	if got := dropVersionReleases(books.Book{Title: "x"}, rels); len(got) != 3 {
		t.Error("a book without versions must keep every release for the standard audiobook")
	}
}

// A version is grabbable under an ebook-only profile, and a profile that rejects the
// version's own word for the standard audiobook doesn't veto the version.
func TestVersionProfile(t *testing.T) {
	v := books.AudioVersion{Label: "Full cast", Terms: []string{"GraphicAudio"}}
	sp := quality.StoredProfile{FormatScores: map[string]int{"EPUB": 40}, Rejected: []string{"GraphicAudio", "abridged"}}
	vp := versionProfile(sp, v)
	if vp.FormatScores["M4B"] <= 0 {
		t.Error("an ebook-only profile must still accept audio for a version")
	}
	if sp.FormatScores["M4B"] != 0 {
		t.Error("versionProfile must not modify the book's profile")
	}
	if len(vp.Rejected) != 1 || vp.Rejected[0] != "abridged" {
		t.Errorf("rejects = %q, want only abridged", vp.Rejected)
	}
	rel := indexer.Release{Title: "Empire of the Vampire [M4B]", Narrator: "GraphicAudio"}
	if _, ok := bookRelScore(vp, rel); !ok {
		t.Error("the version's own release must be eligible under its profile")
	}
	if _, ok := bookRelScore(sp, rel); ok {
		t.Error("the book's profile alone would have refused it")
	}
}

// A library folder named "<Title> (<Label>)" is that book's version, not a new book.
func TestVersionForScanFolder(t *testing.T) {
	lib := []books.Book{{ID: 1, Title: "Empire of the Vampire", AudioVersions: []books.AudioVersion{{ID: 7, Label: "Full cast"}}}}
	if b, v, ok := versionForScanFolder("Empire of the Vampire (Full Cast)", lib); !ok || b.ID != 1 || v.ID != 7 {
		t.Errorf("folder not recognised as the version: %v %v %v", b.ID, v.ID, ok)
	}
	if _, _, ok := versionForScanFolder("Empire of the Vampire", lib); ok {
		t.Error("the standard folder is not a version")
	}
}
