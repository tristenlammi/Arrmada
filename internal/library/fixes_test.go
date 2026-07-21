package library

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeZipWithVideo writes a zip at zipPath holding one video entry of n bytes.
func makeZipWithVideo(t *testing.T, zipPath, entryName string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create(entryName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(make([]byte, n)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// TestImportAsExtractsArchives pins fix #1: ImportAs must unpack archives exactly
// like Import does before searching for the media file.
func TestImportAsExtractsArchives(t *testing.T) {
	src := t.TempDir()
	lib := t.TempDir()
	rel := filepath.Join(src, "Movie.2024.1080p.BluRay.x264-GRP")
	makeZipWithVideo(t, filepath.Join(rel, "movie.zip"), "Movie.2024.1080p.BluRay.x264-GRP.mkv", 60<<20)

	im := NewImporter(lib, quiet())
	res, err := im.ImportAs("Movie", 2024, rel)
	if err != nil {
		t.Fatalf("ImportAs should extract archives first: %v", err)
	}
	if _, err := os.Stat(res.TargetPath); err != nil {
		t.Errorf("expected imported file at %q: %v", res.TargetPath, err)
	}
}

// TestImportSingleFileArchive pins fix #1's second half: a contentPath that IS an
// archive (a lone .zip/.rar torrent) is extracted into a scratch dir and imported —
// in both Import and ImportAs.
func TestImportSingleFileArchive(t *testing.T) {
	src := t.TempDir()
	name := "Movie.2024.1080p.WEB-DL-GRP"
	zipPath := filepath.Join(src, name+".zip")
	makeZipWithVideo(t, zipPath, name+".mkv", 60<<20)

	im := NewImporter(t.TempDir(), quiet())
	res, err := im.Import(name, zipPath)
	if err != nil {
		t.Fatalf("Import of a bare archive file: %v", err)
	}
	if _, err := os.Stat(res.TargetPath); err != nil {
		t.Errorf("expected imported file: %v", err)
	}

	im2 := NewImporter(t.TempDir(), quiet())
	res2, err := im2.ImportAs("Movie", 2024, zipPath)
	if err != nil {
		t.Fatalf("ImportAs of a bare archive file: %v", err)
	}
	if _, err := os.Stat(res2.TargetPath); err != nil {
		t.Errorf("expected imported file: %v", err)
	}
}

// TestFindMediaFileFilters pins fix #2: directory scans never select a sample and
// apply the 50MB floor; a directory holding only sub-floor/sample videos returns a
// distinct error (so the failure path never treats junk as the movie).
func TestFindMediaFileFilters(t *testing.T) {
	t.Run("sample never selected even when largest", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "sample.mkv"), 70<<20) // big but sample-named
		writeFile(t, filepath.Join(dir, "movie.mkv"), 60<<20)
		got, _, err := findMediaFile(dir)
		if err != nil || filepath.Base(got) != "movie.mkv" {
			t.Errorf("findMediaFile = %q, %v; want movie.mkv", got, err)
		}
	})
	t.Run("only sample-sized videos is a distinct error", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "clip.mkv"), 1<<20) // under the floor
		_, _, err := findMediaFile(dir)
		if !errors.Is(err, ErrOnlySamples) {
			t.Errorf("err = %v, want ErrOnlySamples", err)
		}
	})
	t.Run("only a sample-named video is an error", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "sample.mkv"), 70<<20)
		if _, _, err := findMediaFile(dir); err == nil {
			t.Error("expected an error, a sample must never be the movie")
		}
	})
	t.Run("no videos at all keeps the old error", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "readme.txt"), 10)
		_, _, err := findMediaFile(dir)
		if err == nil || errors.Is(err, ErrOnlySamples) {
			t.Errorf("err = %v, want plain no-video-found", err)
		}
	})
	t.Run("single-file contentPath keeps current behavior", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "small.mkv")
		writeFile(t, p, 100) // tiny, but explicitly chosen
		got, _, err := findMediaFile(p)
		if err != nil || got != p {
			t.Errorf("findMediaFile(file) = %q, %v; want the file itself", got, err)
		}
	})
}

// TestReplaceRecyclesExisting pins fix #4: when a different file already occupies
// the destination and a recycle dir is configured, the old file is recycled first
// and the new one HARDLINKS into place (no silent full copy).
func TestReplaceRecyclesExisting(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	src := filepath.Join(dir, "new.mkv")
	dst := filepath.Join(dir, "lib", "movie.mkv")
	writeFile(t, src, 2000)
	writeFile(t, dst, 1000) // existing library file, different content/size

	im := NewImporter(dir, quiet())
	im.SetRecycleDir(bin)
	method, err := im.linkOrCopy(src, dst)
	if err != nil {
		t.Fatalf("linkOrCopy: %v", err)
	}
	if method != "hardlink" {
		t.Errorf("method = %q, want hardlink after recycling the old file", method)
	}
	fi, err := os.Stat(dst)
	if err != nil || fi.Size() != 2000 {
		t.Errorf("dst size = %v err=%v, want the new 2000-byte file", fi, err)
	}
	// The old file must be in the bin, restorable (sidecar present).
	binFile := filepath.Join(bin, "lib", "movie.mkv")
	if fi, err := os.Stat(binFile); err != nil || fi.Size() != 1000 {
		t.Errorf("recycled old file missing/wrong: %v %v", fi, err)
	}
	if _, err := os.Stat(binFile + RecycleMetaExt); err != nil {
		t.Errorf("recycled file should have a sidecar: %v", err)
	}
}

// TestReplaceWithoutRecycleStillWorks: with no recycle dir the old overwrite
// behavior stands (a copy over dst), just logged.
func TestReplaceWithoutRecycleStillWorks(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new.mkv")
	dst := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 2000)
	writeFile(t, dst, 1000)

	im := NewImporter(dir, quiet())
	method, err := im.linkOrCopy(src, dst)
	if err != nil {
		t.Fatalf("linkOrCopy: %v", err)
	}
	if method != "copy" {
		t.Errorf("method = %q, want copy (dst occupied, no recycling)", method)
	}
	if fi, _ := os.Stat(dst); fi == nil || fi.Size() != 2000 {
		t.Errorf("dst should hold the replacement")
	}
}

// TestCleanStaleTemps pins fix #5's sweep: day-old *.arrmada-tmp litter is removed
// from a target dir; fresh temps (a copy may be in flight) are kept.
func TestCleanStaleTemps(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "movie.mkv.123.arrmada-tmp")
	fresh := filepath.Join(dir, "movie.mkv.456.arrmada-tmp")
	other := filepath.Join(dir, "movie.mkv")
	for _, p := range []string{stale, fresh, other} {
		writeFile(t, p, 10)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	cleanStaleTemps(dir)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale temp should be removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh temp must be kept (copy may be in flight)")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("non-temp files must be untouched")
	}
}

// TestImportFailsWhenRootMissing pins fix #6b: a configured library root that does
// not exist (unmounted) fails the import instead of being silently created.
func TestImportFailsWhenRootMissing(t *testing.T) {
	src := t.TempDir()
	base := t.TempDir()
	missing := filepath.Join(base, "gone", "movies") // never created
	writeFile(t, filepath.Join(src, "Movie.2024.1080p.WEB-DL.mkv"), 1000)

	im := NewImporter(base, quiet())
	im.SetRoots(missing, "", "", "")
	_, err := im.Import("Movie.2024.1080p.WEB-DL", filepath.Join(src, "Movie.2024.1080p.WEB-DL.mkv"))
	if err == nil || !strings.Contains(err.Error(), "library root") {
		t.Errorf("err = %v, want a library-root-missing error", err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Error("the missing root must NOT have been created")
	}
	// Subdirectories under an existing root are still created fine (normal imports
	// elsewhere in the suite cover this, but assert the guard's scope explicitly).
	im2 := NewImporter(base, quiet())
	if _, err := im2.Import("Movie.2024.1080p.WEB-DL", filepath.Join(src, "Movie.2024.1080p.WEB-DL.mkv")); err != nil {
		t.Errorf("import under an existing root should succeed: %v", err)
	}
}

// TestSubReimportDoesNotStack pins fix #7a: re-importing the same release must not
// stack "movie.en.2.srt" duplicates — the already-imported check runs before any
// uniqueness suffixing.
func TestSubReimportDoesNotStack(t *testing.T) {
	src := t.TempDir()
	lib := t.TempDir()
	name := "Dune.Part.Two.2024.1080p.WEB-DL-FLUX"
	writeFile(t, filepath.Join(src, name, name+".mkv"), 60<<20)
	writeFile(t, filepath.Join(src, name, name+".en.srt"), 40)

	im := NewImporter(lib, quiet())
	res, err := im.Import(name, filepath.Join(src, name))
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if _, err := im.Import(name, filepath.Join(src, name)); err != nil {
		t.Fatalf("second import: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(res.TargetPath))
	subs := []string{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".srt") {
			subs = append(subs, e.Name())
		}
	}
	if len(subs) != 1 {
		t.Errorf("subtitles after re-import = %v, want exactly one (no .2 duplicates)", subs)
	}
}

// TestEpisodeImportsPackSubs pins fix #7b: an episode import scans the release
// folder's Subs/ directory for subtitles matching the episode (by basename, or any
// subtitle when the folder holds exactly one video).
func TestEpisodeImportsPackSubs(t *testing.T) {
	t.Run("basename match in Subs", func(t *testing.T) {
		src := t.TempDir()
		lib := t.TempDir()
		rel := filepath.Join(src, "Show.S01.1080p.WEB-DL")
		video := filepath.Join(rel, "Show.S01E01.1080p.mkv")
		writeFile(t, video, 3000)
		writeFile(t, filepath.Join(rel, "Show.S01E02.1080p.mkv"), 3000) // second video → no only-video shortcut
		writeFile(t, filepath.Join(rel, "Subs", "Show.S01E01.1080p.en.srt"), 40)
		writeFile(t, filepath.Join(rel, "Subs", "Show.S01E02.1080p.en.srt"), 44)

		im := NewImporter(lib, quiet())
		res, ok, err := im.ImportEpisode("Show", 0, video)
		if err != nil || !ok {
			t.Fatalf("import: ok=%v err=%v", ok, err)
		}
		base := strings.TrimSuffix(res.TargetPath, filepath.Ext(res.TargetPath))
		if _, err := os.Stat(base + ".en.srt"); err != nil {
			t.Errorf("expected the episode's pack subtitle imported: %v", err)
		}
		// The OTHER episode's subtitle must not have been dragged along.
		entries, _ := os.ReadDir(filepath.Dir(res.TargetPath))
		subs := 0
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".srt") {
				subs++
			}
		}
		if subs != 1 {
			t.Errorf("imported %d subtitles, want only this episode's", subs)
		}
	})
	t.Run("single video takes all Subs", func(t *testing.T) {
		src := t.TempDir()
		lib := t.TempDir()
		rel := filepath.Join(src, "Show.S01E01.1080p.WEB-DL")
		video := filepath.Join(rel, "Show.S01E01.1080p.mkv")
		writeFile(t, video, 3000)
		writeFile(t, filepath.Join(rel, "Subs", "2_English.srt"), 40)

		im := NewImporter(lib, quiet())
		res, ok, err := im.ImportEpisode("Show", 0, video)
		if err != nil || !ok {
			t.Fatalf("import: ok=%v err=%v", ok, err)
		}
		base := strings.TrimSuffix(res.TargetPath, filepath.Ext(res.TargetPath))
		if _, err := os.Stat(base + ".en.srt"); err != nil {
			t.Errorf("expected 2_English.srt imported as .en.srt: %v", err)
		}
	})
}

// TestImportBookEditionPartialFailure pins fix #8: per-file errors aren't
// swallowed — FileCount counts only successful placements, a partial success
// returns no error, and a total wipeout returns one.
func TestImportBookEditionPartialFailure(t *testing.T) {
	src := t.TempDir()
	lib := t.TempDir()
	im := NewImporter(lib, quiet())

	good1 := filepath.Join(src, "part1.mp3")
	good2 := filepath.Join(src, "part2.mp3")
	writeFile(t, good1, 1000)
	writeFile(t, good2, 1000)
	missing := filepath.Join(src, "nope.mp3") // never created

	res, err := im.ImportBookEdition("Author", "Book", []FoundFile{
		{Path: good1}, {Path: missing}, {Path: good2},
	})
	if err != nil {
		t.Fatalf("partial success must not error: %v", err)
	}
	if res.FileCount != 2 {
		t.Errorf("FileCount = %d, want 2 (only successful placements)", res.FileCount)
	}

	_, err = im.ImportBookEdition("Author", "Book2", []FoundFile{
		{Path: filepath.Join(src, "gone1.mp3")}, {Path: filepath.Join(src, "gone2.mp3")},
	})
	if err == nil {
		t.Error("expected an error when NOTHING could be placed")
	}
}

// TestRecycleFileOrphanSidecar pins fix #9's orphan case: when the copy+remove
// fallback copies into the bin but can't remove the source, the sidecar is still
// written so the bin copy stays restorable.
func TestRecycleFileOrphanSidecar(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection doesn't work as root")
	}
	bin := t.TempDir()
	srcDir := t.TempDir()
	sub := filepath.Join(srcDir, "Movie (2024)")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(sub, "movie.mkv")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Read-only parent: rename fails (can't unlink), copy succeeds (file readable),
	// remove fails → the orphan path.
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	_, err := RecycleFile(bin, src)
	if err == nil {
		t.Fatal("expected an error — the source could not be removed")
	}
	binCopy := filepath.Join(bin, "Movie (2024)", "movie.mkv")
	if b, rerr := os.ReadFile(binCopy); rerr != nil || string(b) != "data" {
		t.Fatalf("bin copy missing/corrupt: %q %v", b, rerr)
	}
	m := ReadRecycleMeta(binCopy)
	if m.Orig != src || m.Deleted == 0 {
		t.Errorf("sidecar = %+v, want origin recorded so the copy is restorable", m)
	}
}

// TestImportRefusesEmptyMovie pins fix #12: the movie import path gets the same
// 0-byte-source guard episodes have.
func TestImportRefusesEmptyMovie(t *testing.T) {
	src := t.TempDir()
	p := filepath.Join(src, "Movie.2024.1080p.WEB-DL.mkv")
	writeFile(t, p, 0)

	im := NewImporter(t.TempDir(), quiet())
	if _, err := im.Import("Movie.2024.1080p.WEB-DL", p); err == nil {
		t.Error("Import must refuse a 0-byte source")
	}
	if _, err := im.ImportAs("Movie", 2024, p); err == nil {
		t.Error("ImportAs must refuse a 0-byte source")
	}
}
