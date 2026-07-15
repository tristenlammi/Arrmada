package library

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
