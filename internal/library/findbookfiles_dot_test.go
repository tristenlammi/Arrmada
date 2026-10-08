package library_test

import (
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
)

// Merge backups and other hidden folders inside a book are never book files, so a scan
// can't count the originals of a merged audiobook as part of it again.
func TestFindBookFilesSkipsDotDirs(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".hidden-parent-is-fine", "Book")
	for _, rel := range []string{
		"Book.m4b",
		"CD1/01.mp3",
		".arrmada-merge-backup/7-20260101T000000Z/01.mp3",
		".AppleDouble/Book.m4b",
	} {
		writeTemp(t, filepath.Join(root, rel))
	}
	var got []string
	for _, f := range library.FindBookFiles(root) {
		rel, _ := filepath.Rel(root, f.Path)
		got = append(got, filepath.ToSlash(rel))
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "Book.m4b" || got[1] != "CD1/01.mp3" {
		t.Fatalf("FindBookFiles = %v, want [Book.m4b CD1/01.mp3]", got)
	}
}

// The library scan skips them too: a merge backup under the audiobooks root must not show
// up as a book called "<id>-<time>" by ".arrmada-merge-backup".
func TestFindBookFoldersSkipsDotDirs(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".hidden-root-is-fine")
	for _, rel := range []string{
		"A. Sine/The Tone Book/The Tone Book.m4b",
		".arrmada-merge-backup/7-20260101T000000Z/Chapter 1.mp3",
		"A. Sine/.AppleDouble/x.mp3",
	} {
		writeTemp(t, filepath.Join(root, rel))
	}
	imp := library.NewImporter(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := imp.FindBookFoldersIn(root)
	if len(got) != 1 || got[0].Title != "The Tone Book" || got[0].Author != "A. Sine" {
		t.Fatalf("FindBookFoldersIn = %+v, want just The Tone Book by A. Sine", got)
	}
}
