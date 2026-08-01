package config

import (
	"path/filepath"
	"testing"
)

// Each library defaults to its OWN subfolder of the shared library dir. They used to all
// default to the library root, which put movies, TV, books and music in one directory and
// had every module's scan walking the others' files.
func TestPerLibraryDirDefaults(t *testing.T) {
	t.Setenv("ARRMADA_LIBRARY_DIR", "/storage/media")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		got, want string
		name      string
	}{
		{c.MoviesDir, filepath.Join("/storage/media", "movies"), "movies"},
		{c.TVDir, filepath.Join("/storage/media", "tvshows"), "tv"},
		{c.EbooksDir, filepath.Join("/storage/media", "ebooks"), "ebooks"},
		{c.AudiobooksDir, filepath.Join("/storage/media", "audiobooks"), "audiobooks"},
		{c.MusicDir, filepath.Join("/storage/media", "music"), "music"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s dir = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// An explicit env var still wins over the derived default.
func TestExplicitDirOverridesDefault(t *testing.T) {
	t.Setenv("ARRMADA_LIBRARY_DIR", "/storage/media")
	t.Setenv("ARRMADA_MUSIC_DIR", "/mnt/flac")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.MusicDir != "/mnt/flac" {
		t.Errorf("MusicDir = %q, want the env override", c.MusicDir)
	}
}
