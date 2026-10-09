package library_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/libroots"
	"github.com/tristenlammi/arrmada/internal/recyclebin"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

func rootsOf(rs ...libroots.Root) func() []libroots.Root {
	return func() []libroots.Root { return rs }
}

// Nested library folders: a file goes to the bin of the deepest folder holding it.
func TestRootBinsLongestPrefix(t *testing.T) {
	base := t.TempDir()
	media, tv := filepath.Join(base, "media"), filepath.Join(base, "media", "tv")
	if err := os.MkdirAll(tv, 0o755); err != nil {
		t.Fatal(err)
	}
	b := &library.RootBins{Roots: rootsOf(libroots.Root{Name: "movies", Path: media}, libroots.Root{Name: "tv", Path: tv + "/"})}
	cases := map[string]string{
		filepath.Join(tv, "Show", "S01E01.mkv"):      filepath.Join(tv, library.RecycleDirName),
		filepath.Join(media, "Film (2020)", "f.mkv"): filepath.Join(media, library.RecycleDirName),
		filepath.Join(base, "media-old", "x.mkv"):    "", // a prefix of the name, not a folder inside it
	}
	for p, want := range cases {
		got, err := b.For(p)
		if want == "" {
			if err == nil {
				t.Errorf("%s: got bin %q, want refused (no library folder and no legacy bin)", p, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%s: bin = %q (%v), want %q", p, got, err, want)
		}
	}
}

// A library folder that isn't there (a share that didn't mount) gets no bin: making one
// would build the folder on the container's own disk. The delete is refused instead.
func TestRootBinsRefuseMissingRoot(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "unmounted", "movies")
	b := &library.RootBins{Roots: rootsOf(libroots.Root{Name: "movies", Path: gone}), Legacy: filepath.Join(t.TempDir(), ".recycle")}
	if got, err := b.For(filepath.Join(gone, "F", "f.mkv")); err == nil {
		t.Fatalf("bin = %q for a folder that isn't there", got)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Error("asking for a bin created the missing library folder")
	}
	// A file that's already gone needs no bin at all.
	if dst, err := library.RemoveToBin(b, filepath.Join(gone, "F", "f.mkv")); err != nil || dst != "" {
		t.Errorf("RemoveToBin of a missing file = %q, %v; want nothing to do", dst, err)
	}
}

// ARRMADA_RECYCLE_DIR set: one bin for everything, as before.
func TestRootBinsExplicitOverride(t *testing.T) {
	lib, explicit := t.TempDir(), filepath.Join(t.TempDir(), "bin")
	b := &library.RootBins{Explicit: explicit, Roots: rootsOf(libroots.Root{Name: "movies", Path: lib}), Legacy: filepath.Join(t.TempDir(), ".recycle")}
	got, err := b.For(filepath.Join(lib, "F", "f.mkv"))
	if err != nil || got != explicit {
		t.Fatalf("bin = %q (%v), want the explicit one", got, err)
	}
	if _, err := os.Stat(filepath.Join(lib, library.RecycleDirName)); !os.IsNotExist(err) {
		t.Error("an explicit bin must not create per-library bins")
	}
	all := b.All()
	if len(all) != 1 || all[0].Dir != explicit {
		t.Errorf("All = %+v, want only the explicit bin (the legacy one is empty)", all)
	}
}

// ARRMADA_RECYCLE_DIR=off: deletes are permanent, and there are no bins to manage.
func TestRootBinsOff(t *testing.T) {
	lib := t.TempDir()
	b := &library.RootBins{Off: true, Roots: rootsOf(libroots.Root{Name: "movies", Path: lib})}
	if _, err := b.For(filepath.Join(lib, "f.mkv")); !errors.Is(err, library.ErrRecycleDisabled) {
		t.Fatalf("err = %v, want ErrRecycleDisabled", err)
	}
	if all := b.All(); len(all) != 0 {
		t.Errorf("All = %+v, want none", all)
	}
	src := filepath.Join(lib, "Film", "f.mkv")
	writeTemp(t, src)
	if dst, err := library.RemoveToBin(b, src); err != nil || dst != "" {
		t.Fatalf("RemoveToBin = %q, %v", dst, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("with the bin off the file should be deleted")
	}
}

// A file outside every library folder (left in a folder the library moved away from)
// goes to the old shared bin, with one warning per folder.
func TestRootBinsUnmatchedFallsBackToLegacy(t *testing.T) {
	lib, legacy := t.TempDir(), filepath.Join(t.TempDir(), ".recycle")
	var logged strings.Builder
	b := &library.RootBins{Roots: rootsOf(libroots.Root{Name: "tv", Path: lib}), Legacy: legacy, Log: slog.New(slog.NewTextHandler(&logged, nil))}
	stray := t.TempDir()
	for _, name := range []string{"a.mkv", "b.mkv"} {
		got, err := b.For(filepath.Join(stray, "Old Show", name))
		if err != nil || got != legacy {
			t.Fatalf("bin = %q (%v), want the legacy bin", got, err)
		}
	}
	if n := strings.Count(logged.String(), "outside every library folder"); n != 1 {
		t.Errorf("warned %d times, want once per folder:\n%s", n, logged.String())
	}
	// Listed (legacy first) only while it holds something.
	if all := b.All(); len(all) != 1 || all[0].Legacy {
		t.Fatalf("All = %+v, want just the TV bin while the legacy one is empty", all)
	}
	writeTemp(t, filepath.Join(legacy, "Old Show", "a.mkv"))
	if all := b.All(); len(all) != 2 || !all[0].Legacy || all[0].Dir != legacy {
		t.Fatalf("All = %+v, want the legacy bin first", all)
	}
}

// A library folder's bin is made on first use with a .plexignore of "*", so Plex never
// lists what's in it; one shared by two libraries is one bin named after both.
func TestRootBinWritesPlexignore(t *testing.T) {
	books := t.TempDir()
	b := &library.RootBins{Roots: rootsOf(libroots.Root{Name: "ebooks", Path: books}, libroots.Root{Name: "audiobooks", Path: books})}
	dir, err := b.For(filepath.Join(books, "Author", "Title", "t.epub"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".plexignore"))
	if err != nil || strings.TrimSpace(string(got)) != "*" {
		t.Fatalf(".plexignore = %q (%v), want *", got, err)
	}
	all := b.All()
	if len(all) != 1 || all[0].Label != "Ebooks & Audiobooks" || all[0].Dir != dir {
		t.Errorf("All = %+v, want one bin for the shared folder", all)
	}
}

// A folder changed in the app routes the next delete to the new folder's bin.
func TestRootBinsFollowLiveRoots(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	var cur atomic.Value
	cur.Store(first)
	b := &library.RootBins{Roots: func() []libroots.Root { return []libroots.Root{{Name: "movies", Path: cur.Load().(string)}} }}
	cur.Store(second)
	got, err := b.For(filepath.Join(second, "F", "f.mkv"))
	if err != nil || got != filepath.Join(second, library.RecycleDirName) {
		t.Fatalf("bin = %q (%v), want the new folder's", got, err)
	}
}

// Deleting a movie on its own mount is a rename into that mount's bin: the copy path
// must never run.
func TestRemoveToBinSameFSNeverCopies(t *testing.T) {
	library.SwapRecycleOps(t, nil, func(src, dst string) (int64, error) {
		t.Fatalf("copied %s → %s: a delete within one library folder must be a rename", src, dst)
		return 0, nil
	}, nil)
	lib := t.TempDir()
	b := &library.RootBins{Roots: rootsOf(libroots.Root{Name: "movies", Path: lib})}
	src := filepath.Join(lib, "Film (2020)", "film.mkv")
	writeTemp(t, src)
	dst, err := library.RemoveToBin(b, src)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(lib, library.RecycleDirName, "Film (2020)", "film.mkv"); dst != want {
		t.Errorf("dst = %q, want %q", dst, want)
	}
	if library.ReadRecycleMeta(dst).Orig != src {
		t.Error("the recycled file must record where it came from")
	}
}

// exdev makes every rename fail the way it does across filesystems.
func exdev(oldpath, newpath string) error {
	return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
}

// binManager is the recycle bin manager over one bin, for checking what it counts.
func binManager(t *testing.T, bin string) *recyclebin.Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return recyclebin.New(bin, settings.NewService(st.DB()), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// When the OS reports a cross-device move, the file is copied, checked and only then
// removed; the bin holds a restorable item.
func TestRecycleCrossDeviceCopiesThenRemoves(t *testing.T) {
	library.SwapRecycleOps(t, exdev, nil, nil)
	lib, bin := t.TempDir(), filepath.Join(t.TempDir(), "bin")
	src := filepath.Join(lib, "Film", "film.mkv")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte(strings.Repeat("frame", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	dst, err := library.RemoveToBin(library.SingleBin(bin), src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the original should be gone once the copy is in place")
	}
	if b, _ := os.ReadFile(dst); string(b) != strings.Repeat("frame", 1000) {
		t.Error("the bin copy doesn't match the original")
	}
	items := binManager(t, bin).List(context.Background())
	if len(items) != 1 || !items[0].Restorable || items[0].OrigPath != src {
		t.Errorf("bin = %+v, want one restorable item", items)
	}
}

// A copy that fails part-way leaves the original where it was and nothing counted as
// recycled in the bin.
func TestRecycleCrossDeviceFailedCopyLeavesNothing(t *testing.T) {
	library.SwapRecycleOps(t, exdev, func(src, dst string) (int64, error) {
		// Half a copy on disk, as a crash or a full disk would leave it.
		_ = os.WriteFile(dst+".123"+library.PartialSuffix, []byte("half"), 0o644)
		return 0, errors.New("no space left on device")
	}, nil)
	lib, bin := t.TempDir(), filepath.Join(t.TempDir(), "bin")
	src := filepath.Join(lib, "Film", "film.mkv")
	writeTemp(t, src)
	if _, err := library.RemoveToBin(library.SingleBin(bin), src); !errors.Is(err, library.ErrBinRefused) {
		t.Fatalf("err = %v, want the bin's refusal", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("the original was lost although the copy failed")
	}
	if items := binManager(t, bin).List(context.Background()); len(items) != 0 {
		t.Errorf("a half copy was counted as recycled: %+v", items)
	}
}

// If the original can't be removed after the copy, the copy is taken back out: a
// duplicate the bin can't restore helps nobody.
func TestRecycleCrossDeviceRemoveFailsTakesCopyBack(t *testing.T) {
	library.SwapRecycleOps(t, exdev, nil, func(string) error { return os.ErrPermission })
	lib, bin := t.TempDir(), filepath.Join(t.TempDir(), "bin")
	src := filepath.Join(lib, "Film", "film.mkv")
	writeTemp(t, src)
	if _, err := library.RemoveToBin(library.SingleBin(bin), src); err == nil {
		t.Fatal("want an error when the original can't be removed")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("the original must still be there")
	}
	if items := binManager(t, bin).List(context.Background()); len(items) != 0 {
		t.Errorf("the copy stayed in the bin: %+v", items)
	}
}

// Only EXDEV means "copy instead": any other rename failure refuses the delete.
func TestRecycleOtherRenameErrorRefuses(t *testing.T) {
	library.SwapRecycleOps(t, func(o, n string) error {
		return &os.LinkError{Op: "rename", Old: o, New: n, Err: syscall.EACCES}
	}, func(src, dst string) (int64, error) {
		t.Fatal("copied after a rename failure that wasn't cross-device")
		return 0, nil
	}, nil)
	lib, bin := t.TempDir(), filepath.Join(t.TempDir(), "bin")
	src := filepath.Join(lib, "F", "f.mkv")
	writeTemp(t, src)
	if _, err := library.RemoveToBin(library.SingleBin(bin), src); !errors.Is(err, library.ErrBinRefused) {
		t.Fatalf("err = %v, want refused", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("the original was touched")
	}
}

// copyVerified leaves nothing behind when it can't finish.
func TestCopyVerifiedLeavesNothingOnError(t *testing.T) {
	dir := t.TempDir()
	if _, err := library.CopyVerified(filepath.Join(dir, "missing.mkv"), filepath.Join(dir, "out.mkv")); err == nil {
		t.Fatal("copying a missing file succeeded")
	}
	src := filepath.Join(dir, "a.mkv")
	_ = os.WriteFile(src, []byte("abc"), 0o644)
	if _, err := library.CopyVerified(src, filepath.Join(dir, "no-such-dir", "out.mkv")); err == nil {
		t.Fatal("copying into a missing folder succeeded")
	}
	n, err := library.CopyVerified(src, filepath.Join(dir, "b.mkv"))
	if err != nil || n != 3 {
		t.Fatalf("copy = %d, %v", n, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), library.PartialSuffix) {
			t.Errorf("a temp file was left behind: %s", e.Name())
		}
	}
}
