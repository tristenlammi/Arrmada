package library

import (
	"path/filepath"
	"testing"
)

// The book scan and a book's own file listing never read the recycle bin.
func TestBookScanSkipsRecycleDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Author", "Kept", "Kept.epub"), 10)
	writeFile(t, filepath.Join(root, RecycleDirName, "Author", "Gone.epub"), 10)
	im := NewImporter("", quiet())
	im.SetRootFuncs(RootFuncs{Ebook: func() string { return root }, Audiobook: func() string { return root }})
	got := im.FindBookFolders()
	if len(got) != 1 || got[0].Title != "Kept" {
		t.Fatalf("book folders = %+v, want only Kept", got)
	}
	if files := FindBookFiles(root); len(files) != 1 {
		t.Errorf("FindBookFiles = %+v, want only the kept book", files)
	}
	if !SkipScanDir(RecycleDirName) || !SkipScanDir(".AppleDouble") || SkipScanDir("Season 01") {
		t.Error("SkipScanDir must skip hidden folders and only those")
	}
}
