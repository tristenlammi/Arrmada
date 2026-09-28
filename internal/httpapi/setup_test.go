package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/config"
)

// Folders chosen in the app replace the environment's for the whole app; an empty or
// unset choice leaves the environment's folder alone.
func TestApplySavedLibraryDirs(t *testing.T) {
	cfg := config.Config{MoviesDir: "/media/library/movies", TVDir: "/media/library/tvshows", DownloadsDir: "/media/downloads"}
	saved := map[string]string{keyLibMovies: "/storage/media/movies", keyLibTV: "  ", keyLibDownloads: "/storage/torrents"}
	get := func(_ context.Context, key, def string) string {
		if v, ok := saved[key]; ok {
			return v
		}
		return def
	}
	ApplySavedLibraryDirs(context.Background(), get, &cfg, nil)
	if cfg.MoviesDir != "/storage/media/movies" || cfg.DownloadsDir != "/storage/torrents" {
		t.Errorf("chosen folders not applied: movies=%q downloads=%q", cfg.MoviesDir, cfg.DownloadsDir)
	}
	if cfg.TVDir != "/media/library/tvshows" {
		t.Errorf("blank choice must keep the environment's folder, got %q", cfg.TVDir)
	}
}

// The wizard pre-fills folders from what's actually on the mount, preferring the most
// specific name and the shallowest match.
func TestSuggestFolders(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"media/movies", "media/tvshows", "media/ebooks", "media/audiobooks", "torrents", "books-old", "downloads/films"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := suggestFolders([]string{root})
	want := map[string]string{
		"movies":     "media/movies",
		"tv":         "media/tvshows",
		"ebooks":     "media/ebooks",
		"audiobooks": "media/audiobooks",
		"downloads":  "torrents",
	}
	for lib, rel := range want {
		if got[lib] != filepath.ToSlash(filepath.Join(root, rel)) {
			t.Errorf("%s: got %q, want %s", lib, got[lib], rel)
		}
	}
	if _, ok := got["music"]; ok {
		t.Errorf("no music folder exists, but one was suggested: %q", got["music"])
	}
}
