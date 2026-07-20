package extract

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractZip(t *testing.T) {
	dir := t.TempDir()

	// Build a zip containing a "video" file, in the download dir.
	zipPath := filepath.Join(dir, "release.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("Movie.2024.1080p.BluRay.x264-GRP/movie.mkv")
	_, _ = w.Write(make([]byte, 1234))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	n, err := ExtractAll(dir)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != 1 {
		t.Fatalf("extracted %d archives, want 1", n)
	}

	// The video is flattened into dir (basename), so the importer finds it.
	if _, err := os.Stat(filepath.Join(dir, "movie.mkv")); err != nil {
		t.Errorf("expected extracted movie.mkv: %v", err)
	}
}

func TestExtractNoArchives(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "movie.mkv"), []byte("x"), 0o644)
	n, err := ExtractAll(dir)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != 0 {
		t.Errorf("extracted %d, want 0 (no archives)", n)
	}
}

func TestIsFirstRarVolume(t *testing.T) {
	cases := map[string]bool{
		"file.rar":        true,
		"file.part01.rar": true,
		"file.part1.rar":  true,
		"file.part02.rar": false,
		"file.part2.rar":  false,
		"file.part10.rar": false,
	}
	for name, want := range cases {
		if got := isFirstRarVolume(name); got != want {
			t.Errorf("isFirstRarVolume(%q) = %v, want %v", name, got, want)
		}
	}
}
