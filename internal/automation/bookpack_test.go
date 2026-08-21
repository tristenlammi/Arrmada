package automation

import (
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
)

// A "Red Rising" grab is routinely the whole trilogy. Every file used to be hardlinked
// into Red Rising's folder — three books filed as one, with the other two still counted
// as missing, so the searcher kept hunting for what was already on disk.
func TestMatchBookFileUsesNameThenFolder(t *testing.T) {
	lib := []books.Book{
		{ID: 1, Title: "Red Rising", Author: "Pierce Brown"},
		{ID: 2, Title: "Golden Son", Author: "Pierce Brown"},
		{ID: 3, Title: "Morning Star", Author: "Pierce Brown"},
	}
	match := func(name string) (books.Book, bool) {
		for _, b := range lib {
			if k := name; len(k) > 0 {
				if containsFold(k, b.Title) {
					return b, true
				}
			}
		}
		return books.Book{}, false
	}

	// Named file: resolved from the filename alone.
	if b, ok := matchBookFile(match, filepath.Join("pack", "Golden Son.m4b")); !ok || b.ID != 2 {
		t.Errorf("by filename: got %+v ok=%v, want Golden Son", b, ok)
	}
	// Generic filename inside a per-book folder: the folder carries the identity.
	if b, ok := matchBookFile(match, filepath.Join("pack", "Morning Star", "01 - Chapter 1.mp3")); !ok || b.ID != 3 {
		t.Errorf("by folder: got %+v ok=%v, want Morning Star", b, ok)
	}
	// Nothing identifiable either way — the caller must keep it with the grabbed book
	// rather than guess.
	if _, ok := matchBookFile(match, filepath.Join("pack", "disc1", "track03.mp3")); ok {
		t.Error("an unidentifiable file must not resolve to a book")
	}
}

func containsFold(hay, needle string) bool {
	h, n := []rune(hay), []rune(needle)
	if len(n) == 0 || len(n) > len(h) {
		return false
	}
	lower := func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}
	for i := 0; i+len(n) <= len(h); i++ {
		ok := true
		for j := range n {
			if lower(h[i+j]) != lower(n[j]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// A metadata search is a fuzzy, ranked guess. "Pierce Brown Golden Son" can rank Red
// Rising first — same author, same series, more popular — and taking the top hit on faith
// filed the Golden Son folder under Red Rising. Because Red Rising was already in the
// library the scan reported nothing amiss, and Golden Son never appeared.
func TestPickScanMatchRequiresTheTitleToCorrespond(t *testing.T) {
	results := []metadata.BookResult{
		{Key: "/works/OL1W", Title: "Red Rising", Author: "Pierce Brown"},
		{Key: "/works/OL2W", Title: "Golden Son", Author: "Pierce Brown"},
	}
	got, ok := pickScanMatch("Golden Son", results)
	if !ok || got.Key != "/works/OL2W" {
		t.Errorf("got %+v ok=%v, want the Golden Son result even though it ranked second", got, ok)
	}

	// A subtitled edition is still the same book — the result may carry more than the
	// folder does.
	subtitled := []metadata.BookResult{{Key: "/works/OL2W", Title: "Golden Son: Red Rising Book 2"}}
	if _, ok := pickScanMatch("Golden Son", subtitled); !ok {
		t.Error("a result whose title contains the folder's must match")
	}

	// Never the reverse: a folder holding a MORE specific title must not be swallowed by
	// the shorter result, or Dune Messiah lands in Dune.
	if _, ok := pickScanMatch("Dune Messiah", []metadata.BookResult{{Key: "/works/OLD", Title: "Dune"}}); ok {
		t.Error("a shorter result must not claim a more specific folder title")
	}

	// Nothing corresponds: leave it uncatalogued and say so, rather than file it wrongly.
	if _, ok := pickScanMatch("Golden Son", []metadata.BookResult{{Key: "/works/OL1W", Title: "Red Rising"}}); ok {
		t.Error("an unrelated top hit must not be accepted")
	}
	if _, ok := pickScanMatch("", results); ok {
		t.Error("an empty folder title has nothing to verify against")
	}
}
