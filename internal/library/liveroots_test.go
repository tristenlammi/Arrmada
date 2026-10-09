package library

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// A folder changed in Settings → Library is where the very next import lands: the
// importer resolves its roots on every call, with no restart.
func TestImporterUsesLiveRoots(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	var movies atomic.Value
	movies.Store(first)
	im := NewImporter("/never/the/managed/volume", quiet())
	im.SetRootFuncs(RootFuncs{Movie: func() string { return movies.Load().(string) }})

	if got := im.MovieTarget("Dune", 2021, "", ".mkv"); !strings.HasPrefix(got, first+string(filepath.Separator)) {
		t.Fatalf("first target %q isn't under the first folder %q", got, first)
	}
	movies.Store(second)
	if got := im.MovieTarget("Dune", 2021, "", ".mkv"); !strings.HasPrefix(got, second+string(filepath.Separator)) {
		t.Fatalf("after the change, target %q isn't under the new folder %q", got, second)
	}

	// A real import follows the change too.
	src := t.TempDir()
	name := "Arrival.2016.1080p.BluRay.x264-GRP"
	writeFile(t, filepath.Join(src, name, name+".mkv"), 60<<20)
	res, err := im.Import(name, filepath.Join(src, name))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.TargetPath, second) {
		t.Errorf("imported to %q, want under %q", res.TargetPath, second)
	}
}

// A kind of media with no folder fails loudly. It used to fall back to
// ARRMADA_LIBRARY_DIR — the managed Docker volume — and the file seemed to vanish.
func TestImporterEmptyRootFails(t *testing.T) {
	lib := t.TempDir()
	im := NewImporter(lib, quiet())
	im.SetRootFuncs(RootFuncs{Movie: func() string { return "  " }}) // TV not set at all

	src := t.TempDir()
	name := "Arrival.2016.1080p.BluRay.x264-GRP"
	writeFile(t, filepath.Join(src, name, name+".mkv"), 60<<20)
	if _, err := im.Import(name, filepath.Join(src, name)); !errors.Is(err, ErrNoLibraryFolder) {
		t.Fatalf("movie import with a blank folder: err = %v, want ErrNoLibraryFolder", err)
	}
	ep := "Show.S01E01.1080p.WEB-DL-GRP"
	writeFile(t, filepath.Join(src, ep, ep+".mkv"), 60<<20)
	if _, _, err := im.ImportEpisode("Show", 2020, filepath.Join(src, ep, ep+".mkv")); !errors.Is(err, ErrNoLibraryFolder) {
		t.Fatalf("episode import with no TV folder: err = %v, want ErrNoLibraryFolder", err)
	}
	if got := im.FindBookFolders(); len(got) != 0 {
		t.Errorf("no book folders set, but the scan found %+v", got)
	}
}

// Root reads race with folder changes during imports; the funcs carry their own locking
// (settings does), and the importer adds no shared state of its own.
func TestImporterLiveRootsConcurrent(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	var cur atomic.Value
	cur.Store(a)
	im := NewImporter("", quiet())
	im.SetRootFuncs(RootFuncs{
		Movie: func() string { return cur.Load().(string) },
		TV:    func() string { return cur.Load().(string) },
		Music: func() string { return cur.Load().(string) },
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if i == 0 {
					if j%2 == 0 {
						cur.Store(b)
					} else {
						cur.Store(a)
					}
				}
				_ = im.MovieTarget("Film", 2000, "", ".mkv")
				_ = im.EpisodeTarget("Show", 2000, 1, 1, "", ".mkv")
				_ = im.MusicDir()
			}
		}(i)
	}
	wg.Wait()
}
