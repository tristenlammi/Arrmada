package extract

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// TestExtractTreeNested checks that ExtractTree reaches an archive nested inside a
// subfolder — the season-pack shape where each episode has its own folder of RARs.
func TestExtractTreeNested(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "Show.S03E10.1080p.BluRay.x264-GRP")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(sub, "release.zip"))
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	fw, _ := w.Create("Show.S03E10.mkv")
	_, _ = fw.Write(make([]byte, 1024))
	w.Close()
	f.Close()

	n, err := ExtractTree(root)
	if err != nil || n != 1 {
		t.Fatalf("ExtractTree = %d, %v; want 1 archive", n, err)
	}
	if _, err := os.Stat(filepath.Join(sub, "Show.S03E10.mkv")); err != nil {
		t.Error("expected the nested archive's video to be extracted next to it")
	}
}
