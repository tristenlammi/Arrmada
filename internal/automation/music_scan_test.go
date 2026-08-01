package automation

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTrack makes a file big enough to clear the 256 KB floor that keeps artwork and
// jingles out of the scan.
func writeTrack(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, 300<<10), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The conventional layout is <Artist>/<Album>/tracks; a folder directly under the root is
// read as "Artist - Album" instead. Getting this wrong files real audio under the wrong name.
func TestFindAlbumFolders(t *testing.T) {
	root := t.TempDir()
	writeTrack(t, filepath.Join(root, "Radiohead", "OK Computer (1997)", "01 - Airbag.flac"))
	writeTrack(t, filepath.Join(root, "Radiohead", "OK Computer (1997)", "02 - Paranoid Android.flac"))
	writeTrack(t, filepath.Join(root, "Portishead", "Dummy", "01 - Mysterons.flac"))
	// No artist level — the folder itself carries both.
	writeTrack(t, filepath.Join(root, "Massive Attack - Mezzanine (1998)", "01 - Angel.flac"))
	// Artwork and a tiny file must be ignored.
	if err := os.WriteFile(filepath.Join(root, "Radiohead", "OK Computer (1997)", "cover.jpg"), make([]byte, 400<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTrackSmall(t, filepath.Join(root, "Radiohead", "OK Computer (1997)", "intro.mp3"))

	got := findAlbumFolders(root)
	if len(got) != 3 {
		t.Fatalf("want 3 album folders, got %d: %+v", len(got), got)
	}

	byAlbum := map[string]albumFolder{}
	for _, f := range got {
		byAlbum[f.album] = f
	}

	ok, exists := byAlbum["OK Computer"]
	if !exists {
		t.Fatalf("OK Computer not found; got %v", keysOf(byAlbum))
	}
	if ok.artist != "Radiohead" || ok.year != 1997 {
		t.Errorf("OK Computer parsed as artist=%q year=%d", ok.artist, ok.year)
	}
	if len(ok.files) != 2 {
		t.Errorf("want 2 audio files (cover.jpg and the tiny mp3 excluded), got %d", len(ok.files))
	}

	if d, exists := byAlbum["Dummy"]; !exists || d.artist != "Portishead" {
		t.Errorf("Dummy parsed as %+v", d)
	}
	// The flat "Artist - Album (Year)" shape.
	mz, exists := byAlbum["Mezzanine"]
	if !exists {
		t.Fatalf("Mezzanine not found; got %v", keysOf(byAlbum))
	}
	if mz.artist != "Massive Attack" || mz.year != 1998 {
		t.Errorf("flat folder parsed as artist=%q album=%q year=%d", mz.artist, mz.album, mz.year)
	}
}

func writeTrackSmall(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, 10<<10), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keysOf(m map[string]albumFolder) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
