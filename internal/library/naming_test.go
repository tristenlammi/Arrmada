package library

import (
	"log/slog"
	"path/filepath"
	"testing"
)

type fixedNaming struct{ n Naming }

func (f fixedNaming) Naming() Naming { return f.n }

func TestMovieTargetDefault(t *testing.T) {
	im := NewImporter("/lib", slog.Default())
	got := im.MovieTarget("Blade Runner 2049", 2017, "Blade.Runner.2049.2017.2160p.BluRay.x265-GRP", ".mkv")
	want := filepath.Join("/lib", "Blade Runner 2049 (2017)", "Blade Runner 2049 (2017) - 2160p BluRay.mkv")
	if got != want {
		t.Errorf("default naming:\n got  %q\n want %q", got, want)
	}
}

func TestMovieTargetCustomFormat(t *testing.T) {
	im := NewImporter("/lib", slog.Default())
	im.SetNaming(fixedNaming{Naming{Folder: "{title}", File: "{title} {resolution} {source}"}})
	got := im.MovieTarget("Dune", 2021, "Dune.2021.1080p.WEB-DL.x264-GRP", ".mkv")
	want := filepath.Join("/lib", "Dune", "Dune 1080p WEB-DL.mkv")
	if got != want {
		t.Errorf("custom naming:\n got  %q\n want %q", got, want)
	}
}

func TestMovieTargetEmptyTokenTidied(t *testing.T) {
	// An unknown quality leaves "{quality}" empty; the trailing " - " must be trimmed.
	im := NewImporter("/lib", slog.Default())
	got := im.MovieTarget("Some Movie", 2000, "Some Movie", ".mp4")
	want := filepath.Join("/lib", "Some Movie (2000)", "Some Movie (2000).mp4")
	if got != want {
		t.Errorf("empty-token tidy:\n got  %q\n want %q", got, want)
	}
}
