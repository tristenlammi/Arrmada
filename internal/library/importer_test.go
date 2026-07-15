package library

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestImportMovie(t *testing.T) {
	src := t.TempDir()
	lib := t.TempDir()

	// A download folder containing the movie + a small sample.
	name := "Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX"
	writeFile(t, filepath.Join(src, name, name+".mkv"), 5000)
	writeFile(t, filepath.Join(src, name, "sample.mkv"), 50) // smaller → ignored

	im := NewImporter(lib, quiet())
	res, err := im.Import(name, filepath.Join(src, name))
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	want := filepath.Join(lib, "Dune Part Two (2024)", "Dune Part Two (2024) - 2160p WEB-DL.mkv")
	if res.TargetPath != want {
		t.Errorf("target = %q\n want %q", res.TargetPath, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected imported file at %q: %v", want, err)
	}
	if res.Title != "Dune Part Two" {
		t.Errorf("title = %q", res.Title)
	}
}

func TestImportEpisode(t *testing.T) {
	src := t.TempDir()
	lib := t.TempDir()

	name := "Andor.S02E01.1080p.WEB-DL.DDP5.1.H.264-NTb"
	writeFile(t, filepath.Join(src, name+".mkv"), 3000) // single file download

	im := NewImporter(lib, quiet())
	res, err := im.Import(name, filepath.Join(src, name+".mkv"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	want := filepath.Join(lib, "Andor", "Season 02", "Andor - S02E01 - 1080p WEB-DL.mkv")
	if res.TargetPath != want {
		t.Errorf("target = %q\n want %q", res.TargetPath, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected imported file: %v", err)
	}
}

// TestImportEpisodeIntoExistingFolder checks that a supplied series folder wins
// over the derived "<Title> (<Year>)" name, so new episodes join the show's
// existing on-disk folder instead of spawning a duplicate.
func TestImportEpisodeIntoExistingFolder(t *testing.T) {
	src := t.TempDir()
	lib := t.TempDir()

	name := "Below.Deck.S11E01.1080p.WEB-DL-NTb"
	writeFile(t, filepath.Join(src, name+".mkv"), 3000)

	im := NewImporter(lib, quiet())
	// Series is known to TMDB with year 2013, but the library folder is "Below Deck".
	res, ok, err := im.ImportEpisodeInto("Below Deck", "Below Deck", 2013, filepath.Join(src, name+".mkv"))
	if err != nil || !ok {
		t.Fatalf("import: ok=%v err=%v", ok, err)
	}
	want := filepath.Join(lib, "Below Deck", "Season 11", "Below Deck - S11E01 - 1080p WEB-DL.mkv")
	if res.TargetPath != want {
		t.Errorf("target = %q\n want %q (should not create a \"Below Deck (2013)\" folder)", res.TargetPath, want)
	}
}

// TestImportEpisodeSkipsEmpty checks that a 0-byte source is refused rather than
// overwriting a good library file with nothing (broken hardlink / mid-move).
func TestImportEpisodeSkipsEmpty(t *testing.T) {
	src := t.TempDir()
	lib := t.TempDir()

	name := "Below.Deck.S11E02.1080p.WEB-DL-NTb"
	writeFile(t, filepath.Join(src, name+".mkv"), 0)

	im := NewImporter(lib, quiet())
	res, ok, err := im.ImportEpisode("Below Deck", 0, filepath.Join(src, name+".mkv"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if ok || res != nil {
		t.Errorf("expected empty file to be skipped, got ok=%v res=%v", ok, res)
	}
}

// TestImportRoutesByType checks that per-media-type roots send movies, TV, and
// book editions to their own library folders (falling back to the base root when
// a type has no dedicated dir).
func TestImportRoutesByType(t *testing.T) {
	src := t.TempDir()
	base := t.TempDir()
	movies := filepath.Join(base, "m")
	tv := filepath.Join(base, "t")
	ebooks := filepath.Join(base, "e")
	audiobooks := filepath.Join(base, "a")

	im := NewImporter(base, quiet())
	im.SetRoots(movies, tv, ebooks, audiobooks)

	// Movie → movies root.
	mv := "Dune.Part.Two.2024.2160p.WEB-DL-FLUX"
	writeFile(t, filepath.Join(src, mv+".mkv"), 5000)
	res, err := im.Import(mv, filepath.Join(src, mv+".mkv"))
	if err != nil {
		t.Fatalf("movie import: %v", err)
	}
	if want := filepath.Join(movies, "Dune Part Two (2024)"); filepath.Dir(res.TargetPath) != want {
		t.Errorf("movie dir = %q, want under %q", filepath.Dir(res.TargetPath), want)
	}

	// Episode → tv root.
	ep := "Andor.S02E01.1080p.WEB-DL-NTb"
	writeFile(t, filepath.Join(src, ep+".mkv"), 3000)
	epRes, err := im.Import(ep, filepath.Join(src, ep+".mkv"))
	if err != nil {
		t.Fatalf("episode import: %v", err)
	}
	if !hasPrefix(epRes.TargetPath, tv) {
		t.Errorf("episode target = %q, want under tv root %q", epRes.TargetPath, tv)
	}

	// Ebook → ebooks root; audiobook → audiobooks root.
	eb, err := im.ImportBookEdition("Some Author", "Some Book", []FoundFile{{Path: mustWrite(t, src, "book.epub")}})
	if err != nil {
		t.Fatalf("ebook import: %v", err)
	}
	if !hasPrefix(eb.TargetPath, ebooks) {
		t.Errorf("ebook target = %q, want under ebooks root %q", eb.TargetPath, ebooks)
	}
	ab, err := im.ImportBookEdition("Some Author", "Some Book", []FoundFile{{Path: mustWrite(t, src, "book.m4b")}})
	if err != nil {
		t.Fatalf("audiobook import: %v", err)
	}
	if !hasPrefix(ab.TargetPath, audiobooks) {
		t.Errorf("audiobook target = %q, want under audiobooks root %q", ab.TargetPath, audiobooks)
	}
}

func hasPrefix(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func mustWrite(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	writeFile(t, p, 1000)
	return p
}

// TestImportAsCleanTitle checks that importing under a metadata title strips
// punctuation into a clean folder name ("tick, tick... BOOM!" → "tick tick BOOM").
func TestImportAsCleanTitle(t *testing.T) {
	src := t.TempDir()
	lib := t.TempDir()
	writeFile(t, filepath.Join(src, "ttb.2160p.WEB-DL.mkv"), 5000)

	im := NewImporter(lib, quiet())
	res, err := im.ImportAs("tick, tick... BOOM!", 2021, filepath.Join(src, "ttb.2160p.WEB-DL.mkv"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	want := filepath.Join(lib, "tick tick BOOM (2021)")
	if got := filepath.Dir(res.TargetPath); got != want {
		t.Errorf("folder = %q, want %q", got, want)
	}
}

// TestImportSidecarSubs checks that external subtitles in a download land next to
// the imported video with the target's base name and preserved language tag.
func TestImportSidecarSubs(t *testing.T) {
	src := t.TempDir()
	lib := t.TempDir()
	name := "Dune.Part.Two.2024.1080p.WEB-DL-FLUX"
	writeFile(t, filepath.Join(src, name, name+".mkv"), 5000)
	writeFile(t, filepath.Join(src, name, name+".en.srt"), 40)  // english, tagged
	writeFile(t, filepath.Join(src, name, name+".srt"), 40)     // bare (no lang)
	writeFile(t, filepath.Join(src, name, "Subs", "spanish.srt"), 40) // in a Subs folder

	im := NewImporter(lib, quiet())
	res, err := im.Import(name, filepath.Join(src, name))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	base := strings.TrimSuffix(res.TargetPath, filepath.Ext(res.TargetPath))
	for _, want := range []string{base + ".en.srt", base + ".srt", base + ".es.srt"} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("expected subtitle at %q: %v", want, err)
		}
	}
}

// TestImportSidecarSubsIgnoresNeighbors checks a single-file torrent (living in a
// shared save dir) never picks up a different download's subtitle.
func TestImportSidecarSubsIgnoresNeighbors(t *testing.T) {
	dl := t.TempDir() // shared downloads dir
	lib := t.TempDir()
	name := "Dune.Part.Two.2024.1080p.WEB-DL-FLUX"
	writeFile(t, filepath.Join(dl, name+".mkv"), 5000)
	writeFile(t, filepath.Join(dl, name+".en.srt"), 40)              // ours
	writeFile(t, filepath.Join(dl, "Some.Other.Movie.2020.en.srt"), 40) // a neighbor

	im := NewImporter(lib, quiet())
	res, err := im.Import(name, filepath.Join(dl, name+".mkv"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	base := strings.TrimSuffix(res.TargetPath, filepath.Ext(res.TargetPath))
	if _, err := os.Stat(base + ".en.srt"); err != nil {
		t.Errorf("expected our subtitle imported: %v", err)
	}
	// Only our sub should be present — the neighbor must be skipped.
	entries, _ := os.ReadDir(filepath.Dir(res.TargetPath))
	subs := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".srt") {
			subs++
		}
	}
	if subs != 1 {
		t.Errorf("expected exactly 1 subtitle imported, got %d", subs)
	}
}

func TestImportNoVideo(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "readme.txt"), 10)
	im := NewImporter(t.TempDir(), quiet())
	if _, err := im.Import("Whatever.2020", src); err == nil {
		t.Error("expected error when no video file present")
	}
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}
