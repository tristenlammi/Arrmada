package libroots

import (
	"os"
	"path/filepath"
	"testing"
)

// A media folder may sit beside the data folder, never in it or above it. Everything
// here is a temp dir; nothing real is touched.
func TestUnderDataDir(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "appdata", "arrmada")
	media := filepath.Join(base, "media")
	for _, d := range []string{data, filepath.Join(data, "movies"), media} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		p    string
		want bool
	}{
		{"the data dir itself", data, true},
		{"a child of the data dir", filepath.Join(data, "movies"), true},
		{"a missing child of the data dir", filepath.Join(data, "tv", "new"), true},
		{"dotdot back into the data dir", filepath.Join(media, "..", "appdata", "arrmada", "x"), true},
		{"the parent of the data dir", filepath.Join(base, "appdata"), true},
		{"an ancestor of the data dir", base, true},
		{"the filesystem root", string(filepath.Separator), true},
		{"the container data dir", "/data/movies", true},
		{"a sibling", media, false},
		{"a sibling with a shared prefix", data + "-media", false},
		{"a child of a sibling", filepath.Join(media, "movies"), false},
	}
	for _, c := range cases {
		if got := UnderDataDir(c.p, data); got != c.want {
			t.Errorf("%s (%s): got %v, want %v", c.name, c.p, got, c.want)
		}
	}

	// Reading from above the data dir is a different refusal (outside the roots), so
	// InDataDir only answers "in or at".
	if InDataDir(base, data) {
		t.Error("InDataDir must not flag an ancestor")
	}
	if !InDataDir(filepath.Join(data, "arrmada.db"), data) {
		t.Error("InDataDir must flag a file in the data dir")
	}
}

// A symlink from the media mount into the data folder is still the data folder.
func TestUnderDataDirFollowsSymlinks(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	media := filepath.Join(base, "media")
	for _, d := range []string{data, media} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(media, "movies")
	if err := os.Symlink(data, link); err != nil {
		t.Skipf("symlinks not permitted here: %v", err)
	}
	if !UnderDataDir(link, data) {
		t.Error("a symlink into the data dir must be refused")
	}
}
