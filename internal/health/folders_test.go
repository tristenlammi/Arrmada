package health

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tristenlammi/arrmada/internal/config"
)

// Probing a missing folder reports it and leaves it missing: the old check MkdirAll'd
// its way to "writable" and hid an unmounted share.
func TestProbeFolderDoesNotCreate(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "tv")
	st := ProbeFolder(missing)
	if st.Exists || !st.ParentExists || !st.ParentWritable {
		t.Errorf("missing folder with a writable parent: %+v", st)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("the probe created the folder: %v", err)
	}

	deep := filepath.Join(base, "unmounted", "movies")
	if st := ProbeFolder(deep); st.Exists || st.ParentExists {
		t.Errorf("missing parent: %+v", st)
	}
	if _, err := os.Stat(filepath.Dir(deep)); !os.IsNotExist(err) {
		t.Fatalf("the probe created the parent: %v", err)
	}

	st = ProbeFolder(base)
	if !st.Exists || !st.IsDir || !st.Writable {
		t.Errorf("plain temp dir: %+v", st)
	}
	entries, _ := os.ReadDir(base)
	if len(entries) != 0 {
		t.Errorf("probe file left behind: %v", entries)
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if st := ProbeFolder(file); !st.Exists || st.IsDir {
		t.Errorf("a file: %+v", st)
	}
}

// A read-only share is not writable. Root ignores mode bits (CI's Docker race run is
// root) and Windows doesn't apply them to folders, so both skip.
func TestFolderNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("mode bits don't stop this user from writing")
	}
	dir := filepath.Join(t.TempDir(), "movies")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if st := ProbeFolder(dir); !st.Exists || st.Writable || st.Err == nil {
		t.Errorf("read-only folder: %+v", st)
	}
}

func TestLibraryFoldersRespectsModuleToggles(t *testing.T) {
	cfg := config.Config{
		MoviesDir: "/storage/movies", TVDir: "/storage/tv",
		EbooksDir: "/storage/ebooks", AudiobooksDir: "/storage/audiobooks",
		MusicDir: "/storage/music", DownloadsDir: "/storage/torrents",
		LibraryDir: "/media/library", // the managed volume is never what's judged
	}
	roles := func(fs []Folder) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Role)
		}
		return out
	}
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	if got := roles(LibraryFolders(cfg, false, false)); !eq(got, []string{"movies", "tv", "downloads"}) {
		t.Errorf("books and music off: %v", got)
	}
	if got := roles(LibraryFolders(cfg, true, true)); !eq(got, []string{"movies", "tv", "ebooks", "audiobooks", "music", "downloads"}) {
		t.Errorf("everything on: %v", got)
	}
	cfg.AudiobooksDir = "/storage/ebooks/"
	got := LibraryFolders(cfg, true, false)
	if r := roles(got); !eq(r, []string{"movies", "tv", "books", "downloads"}) {
		t.Errorf("shared books folder not merged: %v", r)
	}
	if got[2].Label != "Books & audiobooks" {
		t.Errorf("merged label = %q", got[2].Label)
	}
	for _, f := range got {
		if f.Path == cfg.LibraryDir {
			t.Errorf("the managed library dir was listed: %+v", f)
		}
	}
}

// Two temp dirs share a filesystem; a folder that doesn't exist yet is measured where it
// would be created.
func TestSameFilesystem(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	if err := os.Mkdir(a, 0o755); err != nil {
		t.Fatal(err)
	}
	same, ok := SameFilesystem(a, b) // b doesn't exist: measured at base
	if !ok {
		t.Skip("disk usage can't be measured on this platform")
	}
	if !same {
		t.Error("two folders in one temp dir reported as different filesystems")
	}
	if _, ok := SameFilesystem("", a); ok {
		t.Error("an empty path can't be measured")
	}
}
