package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
)

// The ebook download is the one book endpoint open from outside the network; its
// neighbours (the book itself, its files, its cover) stay LAN-only.
func TestExternalAllowsOnlyTheEbookDownload(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/v1/books/12/ebook":         true,
		"/api/v1/me/books":               true,
		"/api/v1/books/12":               false,
		"/api/v1/books/12/edition-files": false,
		"/api/v1/books/12/ebook/extra":   false,
		"/api/v1/books/abc/ebook":        false,
		"/api/v1/books":                  false,
	} {
		if got := externalAllowed(path); got != want {
			t.Errorf("externalAllowed(%q) = %v, want %v", path, got, want)
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
