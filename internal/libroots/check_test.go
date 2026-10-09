package libroots

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// assertNoProbes fails if a probe file was left behind in any of dirs.
func assertNoProbes(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				t.Errorf("probe left behind in %s: %s", d, e.Name())
			}
		}
	}
}

func TestCheckFolderUnderDataDirRejected(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	movies := filepath.Join(data, "movies")
	mkdirs(t, movies)

	c := CheckFolder(movies, "", data)
	if !c.UnderDataDir {
		t.Error("a child of the data dir was not flagged")
	}
	// Nothing is ever written into the database folder, not even a probe.
	if c.Writable {
		t.Error("the write probe ran inside the data dir")
	}
	assertNoProbes(t, data, movies)
}

// Two folders in one temp dir share a filesystem, so the link works — and both probe
// files are gone afterwards.
func TestCheckFolderHardlinkProbe(t *testing.T) {
	base := t.TempDir()
	downloads := filepath.Join(base, "torrents")
	movies := filepath.Join(base, "media", "movies")
	mkdirs(t, downloads, movies, filepath.Join(movies, "Film (2020)"), filepath.Join(movies, ".hidden"))

	c := CheckFolder(movies, downloads, filepath.Join(base, "data"))
	if !c.Exists || !c.IsDir || !c.Writable || c.UnderDataDir {
		t.Fatalf("plain folder misjudged: %+v", c)
	}
	if c.HardlinkWithDownloads == nil || !*c.HardlinkWithDownloads {
		t.Errorf("same-filesystem link not reported: %+v", c.HardlinkWithDownloads)
	}
	if c.Entries != 1 || c.EntriesCapped {
		t.Errorf("entries = %d (capped %v), want 1 visible folder", c.Entries, c.EntriesCapped)
	}
	assertNoProbes(t, downloads, movies)

	// The downloads folder itself isn't linked into itself.
	if d := CheckFolder(downloads, downloads, ""); d.HardlinkWithDownloads != nil {
		t.Errorf("downloads checked against itself: %v", *d.HardlinkWithDownloads)
	}
	// No downloads folder to link from: unknown, not false.
	if d := CheckFolder(movies, filepath.Join(base, "missing"), ""); d.HardlinkWithDownloads != nil {
		t.Errorf("missing downloads gave %v, want unknown", *d.HardlinkWithDownloads)
	}
}

// A mistyped path is reported missing and is never created by looking at it.
func TestCheckFolderMissingIsNotCreated(t *testing.T) {
	base := t.TempDir()
	p := filepath.Join(base, "storage", "movise")
	c := CheckFolder(p, "", "")
	if c.Exists || c.IsDir || c.Writable {
		t.Errorf("missing folder misjudged: %+v", c)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("checking created the folder: %v", err)
	}

	f := filepath.Join(base, "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := CheckFolder(f, "", ""); !c.Exists || c.IsDir {
		t.Errorf("a file must exist but not be a folder: %+v", c)
	}
}

// A read-only share is reported as such. Root ignores mode bits (CI's Docker run is
// root), and Windows doesn't honour them on folders, so both skip.
func TestCheckFolderReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("mode bits don't stop this user from writing")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	mkdirs(t, dir)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if c := CheckFolder(dir, "", ""); c.Writable {
		t.Error("read-only folder reported writable")
	}
}
