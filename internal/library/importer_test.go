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

	// A download folder containing the movie + a small sample. The movie must be
	// over the ~50MB directory-scan floor now that findMediaFile filters samples.
	name := "Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX"
	writeFile(t, filepath.Join(src, name, name+".mkv"), 60<<20)
	writeFile(t, filepath.Join(src, name, "sample.mkv"), 50) // sample-named + tiny → ignored

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

	want := filepath.Join(lib, "Andor", "Season 2", "Andor - S02E01 - 1080p WEB-DL.mkv")
	if res.TargetPath != want {
		t.Errorf("target = %q\n want %q", res.TargetPath, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected imported file: %v", err)
	}
}

// TestSeasonDirNameIsReadOnly pins the fix for seasonDirName renaming folders as a
// side effect of merely DERIVING a path: computing a target (including previews)
// must never mutate the disk. An existing variant spelling is reused as-is; folder
// normalization is the explicit SeriesRename flow's job. (This test previously
// pinned the opposite — rename-on-derive — behavior.)
func TestSeasonDirNameIsReadOnly(t *testing.T) {
	lib := t.TempDir()
	// A legacy padded season folder already on disk.
	if err := os.MkdirAll(filepath.Join(lib, "Andor", "Season 04"), 0o755); err != nil {
		t.Fatal(err)
	}
	im := NewImporter(lib, quiet())

	// The legacy "Season 04" is reused, NOT renamed.
	if got := seasonDirName(filepath.Join(lib, "Andor"), 4, "Season 4"); got != "Season 04" {
		t.Errorf("import seasonDirName = %q, want the existing %q reused", got, "Season 04")
	}
	if _, err := os.Stat(filepath.Join(lib, "Andor", "Season 04")); err != nil {
		t.Errorf("existing folder must be untouched by path derivation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lib, "Andor", "Season 4")); err == nil {
		t.Error("deriving a path must not create/rename folders on disk")
	}
	// The rename path (EpisodeTargetIn) still targets the canonical unpadded name —
	// actually moving files there is the SeriesRename flow's job.
	got := im.EpisodeTargetIn("Andor", "Andor", 2022, 4, 1, "Andor.S04E01.1080p.WEB-DL.mkv", ".mkv")
	want := filepath.Join(lib, "Andor", "Season 4", "Andor - S04E01 - 1080p WEB-DL.mkv")
	if got != want {
		t.Errorf("rename target = %q\n want %q (canonical)", got, want)
	}
}

type fixedSeriesNaming struct{ n SeriesNaming }

func (f fixedSeriesNaming) SeriesNaming() SeriesNaming { return f.n }

// TestSeriesNamingScheme checks a fully custom series scheme renders through: a bare
// series folder, a zero-padded season folder via {season00}, and an episode file with
// the episode title and quality.
func TestSeriesNamingScheme(t *testing.T) {
	lib := t.TempDir()
	im := NewImporter(lib, quiet())
	im.SetEpisodeTitleFunc(func(string, int, int, int) string { return "The Pilot" })
	im.SetSeriesNaming(fixedSeriesNaming{SeriesNaming{
		Folder:       "{title}",
		SeasonFolder: "Season {season00}",
		EpisodeFile:  "{title} {episode} {episodetitle} [{quality}]",
	}})
	got := im.EpisodeTargetIn("", "Foo", 2020, 4, 1, "Foo.S04E01.1080p.WEB-DL.mkv", ".mkv")
	want := filepath.Join(lib, "Foo", "Season 04", "Foo S04E01 The Pilot [1080p WEB-DL].mkv")
	if got != want {
		t.Errorf("custom scheme target =\n %q\n want %q", got, want)
	}
}

// TestLinkOrCopyIdempotent is the regression test for the 0-byte-both bug: a second
// import of the same file must be a no-op, never a destructive truncate that zeroes
// the hardlinked source (the torrent) along with the destination.
func TestLinkOrCopyIdempotent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	writeFile(t, src, 4096)

	if _, err := linkOrCopy(src, dst); err != nil {
		t.Fatalf("first import: %v", err)
	}
	// Re-import (what the sweep does every ~30s) must detect it's already there.
	m2, err := linkOrCopy(src, dst)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if m2 != "already" {
		t.Errorf("second linkOrCopy = %q, want %q", m2, "already")
	}
	// Both files must still hold their data — the whole point.
	for _, p := range []string{src, dst} {
		fi, err := os.Stat(p)
		if err != nil || fi.Size() != 4096 {
			t.Errorf("%s = size %v (err %v), want 4096 — data was zeroed", p, fi, err)
		}
	}
}

func TestParseSeasonDir(t *testing.T) {
	ok := map[string]int{"Season 1": 1, "Season 01": 1, "season 12": 12, "S1": 1, "S01": 1, "Specials": 0, "Season 0": 0}
	for name, want := range ok {
		if n, valid := parseSeasonDir(name); !valid || n != want {
			t.Errorf("parseSeasonDir(%q) = (%d,%v), want (%d,true)", name, n, valid, want)
		}
	}
	for _, name := range []string{"Extras", "Season", "Behind the Scenes", "S"} {
		if _, valid := parseSeasonDir(name); valid {
			t.Errorf("parseSeasonDir(%q) = valid, want invalid", name)
		}
	}
}

// TestSeasonDirNameReusesExisting checks that an import joins the show's existing
// "Season 1" folder instead of creating a padded "Season 01" duplicate.
func TestSeasonDirNameReusesExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Season 1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := seasonDirName(dir, 1, "Season 1"); got != "Season 1" {
		t.Errorf("seasonDirName = %q, want the existing %q", got, "Season 1")
	}
	// A season with no existing folder falls back to the given canonical default.
	if got := seasonDirName(dir, 2, "Season 2"); got != "Season 2" {
		t.Errorf("seasonDirName(new) = %q, want %q", got, "Season 2")
	}
	// An existing variant spelling is reused as-is — seasonDirName is read-only, so
	// it never renames toward the canonical form. (Previously pinned normalization;
	// updated for the no-side-effect fix.)
	if got := seasonDirName(dir, 1, "Season 01"); got != "Season 1" {
		t.Errorf("seasonDirName(variant) = %q, want the existing %q reused", got, "Season 1")
	}
}

// When BOTH spellings exist, the existing folder is reused rather than renamed — merging
// two directories isn't this function's job, and clobbering one would lose episodes.
func TestSeasonDirNameDoesNotMergeFolders(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"Season 3", "Season 03"} {
		if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := seasonDirName(dir, 3, "Season 3")
	if got != "Season 3" && got != "Season 03" {
		t.Errorf("seasonDirName = %q, want one of the existing folders", got)
	}
	for _, n := range []string{"Season 3", "Season 03"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s should still exist — folders must never be merged: %v", n, err)
		}
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

	// Configured roots must exist up front — the importer now refuses to create a
	// library root itself (mount guard), only subdirectories beneath it.
	for _, d := range []string{movies, tv, ebooks, audiobooks} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
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
	writeFile(t, filepath.Join(src, name, name+".mkv"), 60<<20)       // over the dir-scan size floor
	writeFile(t, filepath.Join(src, name, name+".en.srt"), 40)        // english, tagged
	writeFile(t, filepath.Join(src, name, name+".srt"), 40)           // bare (no lang)
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
	writeFile(t, filepath.Join(dl, name+".en.srt"), 40)                 // ours
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
