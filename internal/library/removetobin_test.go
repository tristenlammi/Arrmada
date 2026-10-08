package library_test

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
)

func writeTemp(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The bin takes the file and records where it came from.
func TestRemoveToBinMovesAndRecordsOrigin(t *testing.T) {
	lib, bin := t.TempDir(), filepath.Join(t.TempDir(), "bin")
	src := filepath.Join(lib, "Show", "ep.mkv")
	writeTemp(t, src)
	dst, err := library.RemoveToBin(library.SingleBin(bin), src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source should be gone")
	}
	if library.ReadRecycleMeta(dst).Orig != src {
		t.Error("recycled file must record its origin")
	}
}

// A bin that can't take the file deletes nothing and says so; it never falls back to a
// permanent delete.
func TestRemoveToBinRefusesWhenBinBroken(t *testing.T) {
	lib, tmp := t.TempDir(), t.TempDir()
	bin := filepath.Join(tmp, "bin")
	if err := os.WriteFile(bin, []byte("a file, not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(lib, "Show", "ep.mkv")
	writeTemp(t, src)
	_, err := library.RemoveToBin(library.SingleBin(bin), src)
	if !errors.Is(err, library.ErrBinRefused) {
		t.Fatalf("err = %v, want ErrBinRefused", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("the file was deleted although the bin refused it")
	}
}

// Only a bin that is deliberately off deletes for good; a missing file is not an error.
func TestRemoveToBinOffDeletes(t *testing.T) {
	src := filepath.Join(t.TempDir(), "ep.mkv")
	writeTemp(t, src)
	if _, err := library.RemoveToBin(library.SingleBin(""), src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("bin off: the file should be deleted")
	}
	if _, err := library.RemoveToBin(library.SingleBin(""), src); err != nil {
		t.Errorf("already gone: %v", err)
	}
	if _, err := library.RemoveToBin(nil, src); err != nil {
		t.Errorf("nil bin (off) on a missing file: %v", err)
	}
}

// Sidecars finds the video's own subtitles — exact name or name plus a language/forced
// suffix — and nothing else.
func TestSidecars(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Show - S01E01.mkv")
	for _, n := range []string{"Show - S01E01.mkv", "Show - S01E01.srt", "Show - S01E01.en.srt", "Show - S01E01.en.forced.ass",
		"Show - S01E010.srt", "Show - S01E02.srt", "notes.srt", "Show - S01E01.nfo"} {
		writeTemp(t, filepath.Join(dir, n))
	}
	var got []string
	for _, p := range library.Sidecars(video) {
		got = append(got, filepath.Base(p))
	}
	sort.Strings(got)
	want := []string{"Show - S01E01.en.forced.ass", "Show - S01E01.en.srt", "Show - S01E01.srt"}
	if len(got) != len(want) {
		t.Fatalf("Sidecars = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Sidecars = %v, want %v", got, want)
		}
	}
}
