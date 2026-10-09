package movies

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
)

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// Upgrading WEBDL-1080p to Bluray-2160p recycles the old release's subtitles next to the
// old video (each with its .arrmeta, so restore works), and leaves the new file's alone.
func TestRemoveFileRecyclesPairedSidecars(t *testing.T) {
	svc, ctx := testService(t)
	lib := filepath.Join(t.TempDir(), "Dune (2021)")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(t.TempDir(), "bin")
	svc.bin = library.SingleBin(binDir)
	m, err := svc.repo.Create(ctx, Movie{TMDBID: 21, Title: "Dune", Year: 2021, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, lib, "Dune (2021) - WEBDL-1080p.mkv", "Dune (2021) - WEBDL-1080p.en.srt", "Dune (2021) - WEBDL-1080p.es.forced.srt")
	oldF := filepath.Join(lib, "Dune (2021) - WEBDL-1080p.mkv")
	if err := svc.MarkImported(ctx, m.ID, oldF, "Dune.2021.1080p.WEB-DL.x264-GRP"); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, lib, "Dune (2021) - Bluray-2160p.mkv", "Dune (2021) - Bluray-2160p.en.srt")
	if err := svc.MarkImported(ctx, m.ID, filepath.Join(lib, "Dune (2021) - Bluray-2160p.mkv"), "Dune.2021.2160p.BluRay.x265-GRP"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"Dune (2021) - WEBDL-1080p.mkv", "Dune (2021) - WEBDL-1080p.en.srt", "Dune (2021) - WEBDL-1080p.es.forced.srt"} {
		if fileExists(filepath.Join(lib, n)) {
			t.Errorf("%s left in the library", n)
		}
		binned := filepath.Join(binDir, "Dune (2021)", n)
		if !fileExists(binned) || !fileExists(binned+library.RecycleMetaExt) {
			t.Errorf("%s not in the bin next to the old video with its .arrmeta", n)
		}
	}
	for _, n := range []string{"Dune (2021) - Bluray-2160p.mkv", "Dune (2021) - Bluray-2160p.en.srt"} {
		if !fileExists(filepath.Join(lib, n)) {
			t.Errorf("%s was removed", n)
		}
	}
}

// Deleting the file with the bin off deletes its sidecars too, and the emptied folder goes.
func TestRemoveFileDeletesSidecarsAndPrunesFolder(t *testing.T) {
	svc, ctx := testService(t)
	lib := filepath.Join(t.TempDir(), "Heat (1995)")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := svc.repo.Create(ctx, Movie{TMDBID: 949, Title: "Heat", Year: 1995})
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, lib, "Heat (1995).mkv", "Heat (1995).en.srt", "Heat (1995).idx", "Heat (1995).sub")
	if err := svc.MarkImported(ctx, m.ID, filepath.Join(lib, "Heat (1995).mkv"), ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteFile(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if fileExists(lib) {
		entries, _ := os.ReadDir(lib)
		t.Errorf("folder not pruned; still holds %d entries", len(entries))
	}
}

// MovieDetail lists only the file's own subtitles; the folder's unpaired ones come apart,
// and another version's sidecars are neither.
func TestSidecarSubtitlesPairedOnly(t *testing.T) {
	lib := t.TempDir()
	writeFiles(t, lib, "Dune - Bluray-1080p.mkv", "Dune - Bluray-1080p.en.srt", "Dune - Bluray-2160p.mkv",
		"Dune - Bluray-2160p.es.srt", "Dune.eng.srt", "notes.txt")
	video := filepath.Join(lib, "Dune - Bluray-1080p.mkv")
	if got := sidecarSubtitles(video); len(got) != 1 || got[0] != "Dune - Bluray-1080p.en.srt" {
		t.Errorf("paired = %v, want only the 1080p file's own subtitle", got)
	}
	if got := orphanSubtitles(video); len(got) != 1 || got[0] != "Dune.eng.srt" {
		t.Errorf("orphans = %v, want Dune.eng.srt", got)
	}
}

// A same-name container swap (X.mp4 replaced by X.mkv) keeps X.en.srt in place.
func TestRemoveFileKeepsSidecarsOnSameBaseSwap(t *testing.T) {
	svc, _ := testService(t)
	lib := t.TempDir()
	writeFiles(t, lib, "X.mp4", "X.mkv", "X.en.srt")
	if err := svc.removeFile(filepath.Join(lib, "X.mp4"), nil); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(lib, "X.mp4")) {
		t.Error("X.mp4 not removed")
	}
	if !fileExists(filepath.Join(lib, "X.en.srt")) {
		t.Error("X.en.srt removed, but it pairs with X.mkv")
	}
}
