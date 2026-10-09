package library_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
)

func baseNames(paths []string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	sort.Strings(out)
	return out
}

func TestPairedSidecars(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Movie.mkv")
	for _, n := range []string{"Movie.mkv", "Movie.en.srt", "MOVIE.EN.FORCED.SRT", "movie.idx", "Movie.sub",
		"Movie 2.srt", "Movie2.en.srt", "Movie.nfo", "Other.en.srt"} {
		writeTemp(t, filepath.Join(dir, n))
	}
	got := baseNames(library.PairedSidecars(video))
	want := []string{"MOVIE.EN.FORCED.SRT", "Movie.en.srt", "Movie.sub", "movie.idx"}
	if len(got) != len(want) {
		t.Fatalf("PairedSidecars = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PairedSidecars = %v, want %v", got, want)
		}
	}
}

// A video whose name extends another's owns its own sidecars: deleting "Movie.mkv" must not
// take "Movie.Proper.en.srt", which belongs to "Movie.Proper.mkv".
func TestPairedSidecarsLeavesLongerNamedVideosSubs(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"Movie.mkv", "Movie.en.srt", "Movie.Proper.mkv", "Movie.Proper.en.srt"} {
		writeTemp(t, filepath.Join(dir, n))
	}
	if got := baseNames(library.PairedSidecars(filepath.Join(dir, "Movie.mkv"))); len(got) != 1 || got[0] != "Movie.en.srt" {
		t.Errorf("PairedSidecars(Movie.mkv) = %v, want only Movie.en.srt", got)
	}
	if got := library.OrphanSidecars(dir); len(got) != 0 {
		t.Errorf("orphans = %v, want none", got)
	}
}

func TestSharesBase(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"X.mp4", "X.mkv", "Y.mkv", "Y.en.srt", "Z.mkv", "Z.nfo"} {
		writeTemp(t, filepath.Join(dir, n))
	}
	if !library.SharesBase(filepath.Join(dir, "X.mp4")) {
		t.Error("X.mp4 next to X.mkv: want true (container swap)")
	}
	if library.SharesBase(filepath.Join(dir, "Y.mkv")) {
		t.Error("Y.mkv alone: want false (a subtitle isn't a video)")
	}
	if library.SharesBase(filepath.Join(dir, "Z.mkv")) {
		t.Error("Z.mkv next to Z.nfo: want false")
	}
}

// A rename carries the sidecars' suffix over, whatever case the old files used.
func TestMoveEpisodeSubsCarriesSuffix(t *testing.T) {
	dir := t.TempDir()
	oldV := filepath.Join(dir, "Show S01E01.mkv")
	newV := filepath.Join(dir, "Show - S01E01 - Pilot.mkv")
	for _, n := range []string{"Show S01E01.en.srt", "SHOW S01E01.EN.FORCED.SRT", "Show S01E02.en.srt"} {
		writeTemp(t, filepath.Join(dir, n))
	}
	im := library.NewImporter(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	im.MoveEpisodeSubs(oldV, newV)
	for _, n := range []string{"Show - S01E01 - Pilot.en.srt", "Show - S01E01 - Pilot.EN.FORCED.SRT", "Show S01E02.en.srt"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s missing after rename: %v", n, err)
		}
	}
}
