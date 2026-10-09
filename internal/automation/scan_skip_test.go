package automation

import (
	"os"
	"path/filepath"
	"testing"
)

// sparse makes a file that reports n bytes without writing them.
func sparse(t *testing.T, p string, n int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(n); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// The music scan never reads an album out of the recycle bin: a deleted album would
// otherwise come straight back into the library.
func TestScannersSkipRecycleDir(t *testing.T) {
	root := t.TempDir()
	sparse(t, filepath.Join(root, "Artist", "Kept Album", "01 - Song.flac"), 1<<20)
	sparse(t, filepath.Join(root, ".arrmada-recycle", "Gone Album", "01 - Song.flac"), 1<<20)
	sparse(t, filepath.Join(root, "Artist", ".hidden", "01 - Song.flac"), 1<<20)
	folders := findAlbumFolders(root)
	if len(folders) != 1 || folders[0].album != "Kept Album" {
		t.Fatalf("album folders = %+v, want only Kept Album", folders)
	}
}
