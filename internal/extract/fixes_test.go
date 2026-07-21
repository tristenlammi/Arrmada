package extract

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeZip builds a zip at path with the given entry name → content pairs (ordered).
func makeZip(t *testing.T, path string, entries [][2]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// TestExtractCollisionDisambiguates pins fix #3b: two flattened entries sharing a
// basename must both survive, the second with a deterministic numeric suffix,
// instead of the second being silently dropped.
func TestExtractCollisionDisambiguates(t *testing.T) {
	dir := t.TempDir()
	makeZip(t, filepath.Join(dir, "pack.zip"), [][2]string{
		{"CD1/movie.mkv", "first-part-data"},
		{"CD2/movie.mkv", "second-part-data!!"},
	})
	if _, err := ExtractAll(dir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	b1, err := os.ReadFile(filepath.Join(dir, "movie.mkv"))
	if err != nil || string(b1) != "first-part-data" {
		t.Errorf("movie.mkv = %q err=%v, want first entry", b1, err)
	}
	b2, err := os.ReadFile(filepath.Join(dir, "movie.2.mkv"))
	if err != nil || string(b2) != "second-part-data!!" {
		t.Errorf("movie.2.mkv = %q err=%v, want second entry preserved with suffix", b2, err)
	}
	// Re-extraction stays idempotent under the same deterministic names.
	if _, err := ExtractAll(dir); err != nil {
		t.Fatalf("second extract: %v", err)
	}
}

// TestExtractAtomicNoTempLeft pins fix #3: extraction goes through a temp+rename,
// and no *.arrmada-tmp files remain after a successful pass.
func TestExtractAtomicNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	makeZip(t, filepath.Join(dir, "rel.zip"), [][2]string{{"video.mkv", strings.Repeat("x", 4096)}})
	if _, err := ExtractAll(dir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".arrmada-tmp") {
			t.Errorf("stale temp file left behind: %s", e.Name())
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "video.mkv")); err != nil || len(b) != 4096 {
		t.Errorf("video.mkv len=%d err=%v, want 4096", len(b), err)
	}
}

// TestExtractUnpackLimit pins fix #3a: the total unpacked bytes per extraction are
// capped, and hitting the cap surfaces ErrUnpackLimit rather than filling the disk.
func TestExtractUnpackLimit(t *testing.T) {
	old := maxUnpackedBytes
	maxUnpackedBytes = 100 // tiny cap for the test
	defer func() { maxUnpackedBytes = old }()

	dir := t.TempDir()
	makeZip(t, filepath.Join(dir, "bomb.zip"), [][2]string{
		{"a.bin", strings.Repeat("a", 80)},
		{"b.bin", strings.Repeat("b", 80)}, // pushes past the 100-byte cap
	})
	_, err := ExtractTree(dir)
	if !errors.Is(err, ErrUnpackLimit) {
		t.Fatalf("err = %v, want ErrUnpackLimit", err)
	}
	// The over-budget entry must not exist as a plausible partial file.
	if _, err := os.Stat(filepath.Join(dir, "b.bin")); !os.IsNotExist(err) {
		t.Error("over-budget entry should not have been left on disk")
	}
	// And no temp litter either.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".arrmada-tmp") {
			t.Errorf("stale temp file left behind: %s", e.Name())
		}
	}
}

// TestExtractArchiveSingleFile pins fix #1's extract-side helper: a lone archive
// file (single-file torrent) can be extracted into an explicit destination dir.
func TestExtractArchiveSingleFile(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "Movie.2024.1080p-GRP.zip")
	makeZip(t, zipPath, [][2]string{{"Movie.2024.1080p-GRP/movie.mkv", "data"}})
	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ExtractArchive(zipPath, dest); err != nil {
		t.Fatalf("ExtractArchive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "movie.mkv")); err != nil {
		t.Errorf("expected extracted movie.mkv in dest: %v", err)
	}
	// Non-first RAR volumes are a quiet no-op.
	rar2 := filepath.Join(dir, "file.part2.rar")
	if err := os.WriteFile(rar2, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ExtractArchive(rar2, dest); err != nil {
		t.Errorf("non-first volume should be a no-op, got %v", err)
	}
}

func TestIsArchive(t *testing.T) {
	cases := map[string]bool{
		"a.zip": true, "A.RAR": true, "b.part1.rar": true,
		"movie.mkv": false, "notes.txt": false,
	}
	for name, want := range cases {
		if got := IsArchive(name); got != want {
			t.Errorf("IsArchive(%q) = %v, want %v", name, got, want)
		}
	}
}
