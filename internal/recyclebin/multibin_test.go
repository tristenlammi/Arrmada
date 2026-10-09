package recyclebin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// multiFixture is a manager over a legacy bin and two per-library bins, all temp dirs.
type multiFixture struct {
	svc                *Service
	set                *settings.Service
	legacy, movies, tv string // bin dirs
	moviesRoot, tvRoot string // the libraries they serve
	bins               []library.BinDir
}

func newMulti(t *testing.T) *multiFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	base := t.TempDir()
	f := &multiFixture{
		set:        settings.NewService(st.DB()),
		legacy:     filepath.Join(base, "volume", ".recycle"),
		moviesRoot: filepath.Join(base, "movies"),
		tvRoot:     filepath.Join(base, "tv"),
	}
	f.movies = filepath.Join(f.moviesRoot, ".arrmada-recycle")
	f.tv = filepath.Join(f.tvRoot, ".arrmada-recycle")
	for _, d := range []string{f.legacy, f.movies, f.tv} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.bins = []library.BinDir{
		{Dir: f.legacy, Label: "Old shared bin", Legacy: true, Serves: []string{f.moviesRoot, f.tvRoot}},
		{Dir: f.movies, Label: "Movies", Serves: []string{f.moviesRoot}},
		{Dir: f.tv, Label: "TV", Serves: []string{f.tvRoot}},
	}
	f.svc = NewBins(func() []library.BinDir { return f.bins }, f.set, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.svc.free = func(string) (diskspace.Usage, bool) { return diskspace.Usage{}, false }
	f.svc.same = func(a, b string) (bool, bool) { return true, true }
	return f
}

// recycle puts a real library file through library.RecycleFile into bin, so it has a
// sidecar with its origin, and backdates its deletion.
func recycle(t *testing.T, bin, orig string, size int, agoDays int) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(orig), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(orig)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Truncate(int64(size))
	_ = f.Close()
	dst, err := library.RecycleFile(bin, orig)
	if err != nil {
		t.Fatal(err)
	}
	m := library.ReadRecycleMeta(dst)
	m.Deleted = time.Now().AddDate(0, 0, -agoDays).Unix()
	writeMeta(t, dst, m)
	return dst
}

func writeMeta(t *testing.T, dst string, m library.RecycleMeta) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst+library.RecycleMetaExt, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRecycleListAcrossBinsAndRestore(t *testing.T) {
	f := newMulti(t)
	ctx := context.Background()
	m := recycle(t, f.movies, filepath.Join(f.moviesRoot, "Film (2020)", "film.mkv"), 10, 1)
	e := recycle(t, f.tv, filepath.Join(f.tvRoot, "Show", "Season 01", "ep.mkv"), 20, 2)
	old := recycle(t, f.legacy, filepath.Join(f.moviesRoot, "Old (1999)", "old.mkv"), 30, 3)

	items := f.svc.List(ctx)
	if len(items) != 3 {
		t.Fatalf("List = %+v, want three items across the bins", items)
	}
	byName := map[string]Item{}
	for _, it := range items {
		byName[it.Name] = it
	}
	if it := byName["film.mkv"]; it.BinLabel != "Movies" || it.Legacy || !it.Restorable {
		t.Errorf("movie item = %+v", it)
	}
	if it := byName["old.mkv"]; it.BinLabel != "Old shared bin" || !it.Legacy {
		t.Errorf("legacy item = %+v, want labelled legacy", it)
	}
	if items[0].Name != "film.mkv" {
		t.Errorf("newest first: got %s", items[0].Name)
	}

	// Restore from each bin puts the file back where it was.
	for _, name := range []string{"film.mkv", "ep.mkv", "old.mkv"} {
		if err := f.svc.Restore(ctx, byName[name].ID); err != nil {
			t.Fatalf("restore %s: %v", name, err)
		}
		if _, err := os.Stat(byName[name].OrigPath); err != nil {
			t.Errorf("%s is not back at %s", name, byName[name].OrigPath)
		}
	}
	for _, p := range []string{m, e, old} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still in its bin", p)
		}
	}

	// Delete-forever works per bin too.
	tvAgain := recycle(t, f.tv, filepath.Join(f.tvRoot, "Show", "Season 01", "ep2.mkv"), 5, 0)
	if err := f.svc.DeleteItem(ctx, f.svc.List(ctx)[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tvAgain); !os.IsNotExist(err) {
		t.Error("delete forever left the file")
	}
}

// The old shared bin keeps its files listed, restorable, aged and capped until emptied.
func TestLegacyBinListedAndEnforced(t *testing.T) {
	f := newMulti(t)
	ctx := context.Background()
	_ = f.set.Set(ctx, keyRetention, "7")
	_ = f.set.Set(ctx, keyMaxGB, "0")
	stale := recycle(t, f.legacy, filepath.Join(f.moviesRoot, "A", "a.mkv"), 10, 30)
	fresh := recycle(t, f.legacy, filepath.Join(f.moviesRoot, "B", "b.mkv"), 10, 1)

	if p := f.svc.Problems(); len(p) != 1 || !p[0].LegacyFull {
		t.Fatalf("problems = %+v, want the legacy bin flagged as still holding files", p)
	}
	f.svc.Enforce(ctx)
	if !gone(stale) || gone(fresh) {
		t.Fatalf("retention in the legacy bin: stale gone=%v fresh gone=%v", gone(stale), gone(fresh))
	}
	st := f.svc.Stats(ctx)
	if len(st.Bins) != 3 || !st.Bins[0].Legacy || st.Bins[0].Files != 1 || st.Files != 1 {
		t.Fatalf("stats = %+v, want the legacy bin first holding one file", st)
	}
	if st.Dir != f.movies {
		t.Errorf("stats dir = %q, want the first non-legacy bin", st.Dir)
	}

	// Emptying just the legacy bin leaves the others alone.
	keep := recycle(t, f.movies, filepath.Join(f.moviesRoot, "C", "c.mkv"), 10, 0)
	if _, err := f.svc.Empty(ctx, st.Bins[0].Key); err != nil {
		t.Fatal(err)
	}
	if !gone(fresh) || gone(keep) {
		t.Errorf("empty one bin: legacy item gone=%v, movies item gone=%v", gone(fresh), gone(keep))
	}
	if p := f.svc.Problems(); len(p) != 0 {
		t.Errorf("an emptied legacy bin is no longer a problem: %+v", p)
	}
	if _, err := f.svc.Empty(ctx, "0123456789"); err == nil {
		t.Error("an unknown bin key must be refused")
	}
}

// The cap applies to the total across bins: the oldest unprotected item goes first,
// whichever bin it's in.
func TestEnforceCapAcrossBins(t *testing.T) {
	f := newMulti(t)
	ctx := context.Background()
	_ = f.set.Set(ctx, keyMaxGB, "1")
	_ = f.set.Set(ctx, keyRetention, "0")
	gib := 1 << 30
	oldestTV := recycle(t, f.tv, filepath.Join(f.tvRoot, "S", "e1.mkv"), gib/2, 20)
	olderLegacy := recycle(t, f.legacy, filepath.Join(f.moviesRoot, "L", "l.mkv"), gib/2, 10)
	newMovie := recycle(t, f.movies, filepath.Join(f.moviesRoot, "M", "m.mkv"), gib/2, 5)

	f.svc.Enforce(ctx) // 1.5 GiB over a 1 GiB cap: one half-GiB item must go
	if !gone(oldestTV) {
		t.Error("the oldest item (in the TV bin) should be purged first")
	}
	if gone(olderLegacy) || gone(newMovie) {
		t.Error("only as much as needed across the bins")
	}
	if free, capped, _ := f.svc.Headroom(ctx); !capped || free != 0 {
		t.Errorf("headroom = %d, want 0 (1 GiB held of 1 GiB)", free)
	}
}

// Item IDs name a bin by key; an unknown key, a climb out of the bin, or a bin's own
// marker are refused.
func TestResolveRejectsUnknownBinKeyAndTraversal(t *testing.T) {
	f := newMulti(t)
	ctx := context.Background()
	recycle(t, f.movies, filepath.Join(f.moviesRoot, "F", "f.mkv"), 1, 0)
	if err := os.WriteFile(filepath.Join(f.movies, ".plexignore"), []byte("*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(f.moviesRoot), "secret.txt")
	_ = os.WriteFile(outside, []byte("x"), 0o644)
	key := f.svc.List(ctx)[0].Bin
	for _, id := range []string{
		"",
		"F/f.mkv",                      // no bin key
		"0123456789/F/f.mkv",           // unknown key
		key + "/",                      // the bin itself
		key + "/../../secret.txt",      // out of the bin
		key + "/F/../../../secret.txt", // out by a longer road
		key + "/.plexignore",           // the bin's marker
	} {
		if err := f.svc.DeleteItem(ctx, id); err == nil {
			t.Errorf("DeleteItem(%q) was accepted", id)
		}
		if err := f.svc.Restore(ctx, id); err == nil {
			t.Errorf("Restore(%q) was accepted", id)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("a crafted ID reached a file outside the bins")
	}
	if _, err := os.Stat(filepath.Join(f.movies, ".plexignore")); err != nil {
		t.Fatal("the bin's .plexignore was removed")
	}
	// The marker isn't an item, and an emptied bin keeps it.
	if items := f.svc.List(ctx); len(items) != 1 {
		t.Fatalf("List = %+v, want only the recycled file", items)
	}
	if _, err := f.svc.Empty(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.movies, ".plexignore")); err != nil {
		t.Error("emptying the bins removed .plexignore, so Plex would list the next delete")
	}
}

// A bin on another drive from the library it serves is flagged in the stats and for
// the health panel; per-bin numbers add up to the totals.
func TestStatsPerBinAndOtherDrive(t *testing.T) {
	f := newMulti(t)
	ctx := context.Background()
	recycle(t, f.movies, filepath.Join(f.moviesRoot, "A", "a.mkv"), 100, 0)
	recycle(t, f.tv, filepath.Join(f.tvRoot, "B", "b.mkv"), 50, 0)
	f.svc.same = func(a, b string) (bool, bool) {
		return !(a == f.legacy && b == f.tvRoot), true // the legacy bin is off the TV drive
	}
	f.svc.free = func(p string) (diskspace.Usage, bool) { return diskspace.Usage{FreeBytes: 7}, true }
	st := f.svc.Stats(ctx)
	if st.Files != 2 || st.Bytes != 150 || st.Bins[1].Bytes != 100 || st.Bins[2].Bytes != 50 {
		t.Fatalf("stats = %+v", st)
	}
	if !st.Bins[0].OtherDrive || st.Bins[1].OtherDrive || st.Bins[2].OtherDrive {
		t.Errorf("other_drive = %v/%v/%v, want only the legacy bin", st.Bins[0].OtherDrive, st.Bins[1].OtherDrive, st.Bins[2].OtherDrive)
	}
	if !st.Bins[1].FreeKnown || st.Bins[1].FreeBytes != 7 {
		t.Errorf("free space per bin: %+v", st.Bins[1])
	}
	p := f.svc.Problems()
	if len(p) != 1 || !p[0].OtherDrive || p[0].Dir != f.legacy {
		t.Errorf("problems = %+v, want the legacy bin on another drive", p)
	}
	if m := f.svc.Mode(ctx); !m.Enabled || len(m.Dirs) != 3 || m.Dirs[0] != f.legacy {
		t.Errorf("mode = %+v, want all three bins, legacy first", m)
	}
}

// A copy still being written (or abandoned by a crash) is never counted as recycled; an
// abandoned one is cleared after a day.
func TestPartialCopiesAreNotItems(t *testing.T) {
	f := newMulti(t)
	ctx := context.Background()
	partial := filepath.Join(f.movies, "Film (2020)", "film.mkv.123"+library.PartialSuffix)
	if err := os.MkdirAll(filepath.Dir(partial), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(partial, []byte("half"), 0o644)
	if items := f.svc.List(ctx); len(items) != 0 {
		t.Fatalf("a partial copy was listed: %+v", items)
	}
	f.svc.Enforce(ctx)
	if gone(partial) {
		t.Fatal("a fresh partial copy may still be in flight; it must be left alone")
	}
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(partial, old, old)
	f.svc.Enforce(ctx)
	if !gone(partial) {
		t.Error("an abandoned partial copy should be cleared")
	}
}
