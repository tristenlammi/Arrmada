package library_test

import (
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
