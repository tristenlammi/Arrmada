package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
)

// My Books' downloads and uploaded covers are the book endpoints open from outside the
// network; their neighbours (the book itself, its files) stay LAN-only.
func TestExternalAllowsOnlyTheEbookDownload(t *testing.T) {
	rt := testRouter(t, true)
	for path, want := range map[string]bool{
		"/api/v1/books/12/ebook":         true,
		"/api/v1/books/12/audiobook":     true,
		"/api/v1/books/12/cover-image":   true,
		"/api/v1/me/books":               true,
		"/api/v1/books/12":               false,
		"/api/v1/books/12/edition-files": false,
		"/api/v1/books/12/covers":        false,
		"/api/v1/books":                  false,
	} {
		rec := callFromOutside(rt, "GET", path, &auth.User{ID: 7, Role: auth.RoleRequester})
		if got := rec.Code == sentinelStatus; got != want {
			t.Errorf("GET %s from outside as a requester: HTTP %d, reachable=%v, want %v", path, rec.Code, got, want)
		}
	}
}

// A stored path is normally the file; a folder yields the most readable format in it.
func TestEbookFilePicksTheBestFormatInAFolder(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"book.pdf", "book.mobi", "book.epub", "cover.jpg", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ebookFile(dir)
	if err != nil || filepath.Base(got) != "book.epub" {
		t.Errorf("folder pick = %q, %v; want book.epub", got, err)
	}
	single := filepath.Join(dir, "book.mobi")
	if got, err := ebookFile(single); err != nil || got != single {
		t.Errorf("single file = %q, %v; want it back unchanged", got, err)
	}
	if _, err := ebookFile(filepath.Join(dir, "missing.epub")); err == nil {
		t.Error("a missing file must error, not serve nothing")
	}
	empty := t.TempDir()
	if _, err := ebookFile(empty); err == nil {
		t.Error("a folder with no ebook must error")
	}
}

// The shelf's Listen button plays an audiobook through the listening API, so each
// audiobook's item_key must be the key that API (and the apps) use for it: the standard
// one and an extra version alike. A version without a file isn't listed.
func TestMyBooksAudiobookItemKey(t *testing.T) {
	b := books.Book{ID: 12,
		Audiobook: &books.BookFile{Path: "/a/std", Format: "m4b", FileCount: 1},
		AudioVersions: []books.AudioVersion{
			{ID: 3, Label: "Full cast", File: &books.BookFile{Path: "/a/cast", Format: "mp3", FileCount: 20}},
			{ID: 4, Label: "Wanted", File: nil},
		}}
	got := myAudiobooks(b)
	if len(got) != 2 {
		t.Fatalf("got %d audiobooks, want 2: %+v", len(got), got)
	}
	if got[0].VersionID != 0 || got[0].ItemKey != audioserver.ItemKey(12, 0) {
		t.Errorf("standard audiobook = %+v, want version 0 with key %q", got[0], audioserver.ItemKey(12, 0))
	}
	if got[1].VersionID != 3 || got[1].ItemKey != audioserver.ItemKey(12, 3) || got[1].Label != "Full cast" {
		t.Errorf("extra version = %+v, want version 3 with key %q", got[1], audioserver.ItemKey(12, 3))
	}
	if only := myAudiobooks(books.Book{ID: 5, AudioVersions: b.AudioVersions[:1]}); len(only) != 1 || only[0].ItemKey != audioserver.ItemKey(5, 3) {
		t.Errorf("a book with only an extra version = %+v", only)
	}
}

// The saved filename is the title and author, minus anything a filesystem or the
// header can't carry; a title of nothing but junk still yields a usable name.
func TestDownloadNameIsSafe(t *testing.T) {
	b := books.Book{Title: `Iron Lung: "Deep" / Dive?`, Author: "Mark Lawrence"}
	if got, want := downloadName(b, ".epub"), "Iron Lung Deep Dive - Mark Lawrence.epub"; got != want {
		t.Errorf("downloadName = %q, want %q", got, want)
	}
	if got := downloadName(books.Book{Title: "///"}, ".pdf"); got != "book.pdf" {
		t.Errorf("junk title → %q, want book.pdf", got)
	}
	h := attachmentHeader("Les Misérables - Victor Hugo.epub")
	if !strings.HasPrefix(h, "attachment;") || !strings.Contains(h, "filename") {
		t.Errorf("non-ASCII name produced an unusable header: %q", h)
	}
}
