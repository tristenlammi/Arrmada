package books

import (
	"os"
	"path/filepath"
	"testing"
)

// A stored path is normally the file; a folder yields the most readable format in it.
func TestEbookFilePicksTheBestFormatInAFolder(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"book.pdf", "book.mobi", "book.epub", "cover.jpg", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := EbookFile(dir)
	if err != nil || filepath.Base(got) != "book.epub" {
		t.Errorf("folder pick = %q, %v; want book.epub", got, err)
	}
	single := filepath.Join(dir, "book.mobi")
	if got, err := EbookFile(single); err != nil || got != single {
		t.Errorf("single file = %q, %v; want it back unchanged", got, err)
	}
	if _, err := EbookFile(filepath.Join(dir, "missing.epub")); err == nil {
		t.Error("a missing file must error, not serve nothing")
	}
	empty := t.TempDir()
	if _, err := EbookFile(empty); err == nil {
		t.Error("a folder with no ebook must error")
	}
}

// Send to Kindle prefers a format Amazon takes: the PDF in a folder with an AZW3, rather
// than the AZW3 the plain pick would choose. With nothing acceptable, the usual pick.
func TestEbookFileForPrefersAcceptedFormats(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"book.azw3", "book.pdf"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := EbookFile(dir); filepath.Base(got) != "book.azw3" {
		t.Errorf("plain pick = %q, want the AZW3", got)
	}
	if got, _ := EbookFileFor(dir, KindleAccepts); filepath.Base(got) != "book.pdf" {
		t.Errorf("Kindle pick = %q, want the PDF", got)
	}
	only := t.TempDir()
	_ = os.WriteFile(filepath.Join(only, "book.mobi"), []byte("x"), 0o644)
	if got, _ := EbookFileFor(only, KindleAccepts); filepath.Base(got) != "book.mobi" {
		t.Errorf("nothing acceptable: pick = %q, want the MOBI so the caller can name it", got)
	}
}

// The saved filename is the title and author, minus anything a filesystem or the
// header can't carry; a title of nothing but junk still yields a usable name.
func TestDownloadNameIsSafe(t *testing.T) {
	b := Book{Title: `Iron Lung: "Deep" / Dive?`, Author: "Mark Lawrence"}
	if got, want := EbookDownloadName(b, ".epub"), "Iron Lung Deep Dive - Mark Lawrence.epub"; got != want {
		t.Errorf("EbookDownloadName = %q, want %q", got, want)
	}
	if got := EbookDownloadName(Book{Title: "///"}, ".pdf"); got != "book.pdf" {
		t.Errorf("junk title → %q, want book.pdf", got)
	}
}

// Amazon's Send to Kindle email takes EPUB and PDF; not MOBI or AZW any more, and not
// Kobo's KEPUB.
func TestKindleAccepts(t *testing.T) {
	for format, want := range map[string]bool{
		"epub": true, "EPUB": true, ".epub": true, "pdf": true, ".PDF": true,
		"azw3": false, "azw": false, "mobi": false, "kepub": false, ".cbz": false, "": false,
	} {
		if got := KindleAccepts(format); got != want {
			t.Errorf("KindleAccepts(%q) = %v, want %v", format, got, want)
		}
	}
}
