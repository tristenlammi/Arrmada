package movies

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/store"
)

// deleteFixture is a movie with real (tiny, synthetic) files in a temp library: a default
// file with subtitles and an extra version. Nothing here touches a real library.
type deleteFixture struct {
	svc      *Service
	bus      *eventbus.Bus
	root     string
	id       int64
	vid      int64
	main     string // default file
	extra    string // the extra version's file
	subs     []string
	extraSub string
}

func newDeleteFixture(t *testing.T, binDir string) *deleteFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New(log)
	f := &deleteFixture{svc: NewService(st.DB(), nil, nil, root, binDir, bus, log), bus: bus, root: root}
	ctx := context.Background()
	m, err := f.svc.repo.Create(ctx, Movie{TMDBID: 42, Title: "Heat", Year: 1995, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	f.id = m.ID
	dir := filepath.Join(root, "Heat (1995)")
	f.main = writeFixture(t, filepath.Join(dir, "Heat (1995) Bluray-1080p.mkv"))
	f.subs = []string{
		writeFixture(t, filepath.Join(dir, "Heat (1995) Bluray-1080p.en.srt")),
		writeFixture(t, filepath.Join(dir, "Heat (1995) Bluray-1080p.en.forced.srt")),
	}
	// The extra version's name starts with the default file's, so a plain prefix match
	// would sweep its subtitle up with the default file's.
	f.extra = writeFixture(t, filepath.Join(dir, "Heat (1995) Bluray-1080p.Directors.Cut.mkv"))
	f.extraSub = writeFixture(t, filepath.Join(dir, "Heat (1995) Bluray-1080p.Directors.Cut.en.srt"))
	if err := f.svc.repo.SetFile(ctx, f.id, f.main); err != nil {
		t.Fatal(err)
	}
	v, err := f.svc.repo.CreateVersion(ctx, f.id, Version{Label: "Director's Cut", QualityProfile: "hd"})
	if err != nil {
		t.Fatal(err)
	}
	f.vid = v.ID
	if err := f.svc.repo.SetVersionFile(ctx, f.vid, f.extra, 1, ""); err != nil {
		t.Fatal(err)
	}
	return f
}

func writeFixture(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// brokenBin is a bin path that is a regular file, so creating anything under it fails on
// every OS — no chmod tricks, no Windows skips.
func brokenBin(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("a file, not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// With a bin that refuses files, deleting the movie with its files fails visibly and
// leaves every file and every row exactly as it was.
func TestDeleteWithBrokenBinKeepsFilesAndRows(t *testing.T) {
	f := newDeleteFixture(t, brokenBin(t))
	ctx := context.Background()
	err := f.svc.Delete(ctx, f.id, true)
	if !errors.Is(err, ErrFilesNotRemoved) || !errors.Is(err, library.ErrBinRefused) {
		t.Fatalf("err = %v, want ErrFilesNotRemoved wrapping ErrBinRefused", err)
	}
	for _, p := range append([]string{f.main, f.extra, f.extraSub}, f.subs...) {
		if !exists(p) {
			t.Errorf("%s was removed although the bin refused it", filepath.Base(p))
		}
	}
	m, err := f.svc.repo.Get(ctx, f.id)
	if err != nil {
		t.Fatalf("the movie row is gone: %v", err)
	}
	if m.MovieFilePath != f.main {
		t.Errorf("default file record changed to %q", m.MovieFilePath)
	}
	if vs, _ := f.svc.repo.ListVersions(ctx, f.id); len(vs) != 1 || vs[0].FilePath != f.extra {
		t.Errorf("versions = %+v, want the extra version intact", vs)
	}
}

// The single-file deletes refuse the same way and change nothing.
func TestDeleteFileAndVersionRefuseWithBrokenBin(t *testing.T) {
	f := newDeleteFixture(t, brokenBin(t))
	ctx := context.Background()
	if err := f.svc.DeleteFile(ctx, f.id); !errors.Is(err, library.ErrBinRefused) {
		t.Errorf("DeleteFile err = %v", err)
	}
	if err := f.svc.DeleteVersionFile(ctx, f.id, f.vid); !errors.Is(err, library.ErrBinRefused) {
		t.Errorf("DeleteVersionFile err = %v", err)
	}
	if err := f.svc.DeleteVersion(ctx, f.vid); !errors.Is(err, library.ErrBinRefused) {
		t.Errorf("DeleteVersion err = %v", err)
	}
	if !exists(f.main) || !exists(f.extra) {
		t.Error("a file was removed although the bin refused it")
	}
	if m, _ := f.svc.repo.Get(ctx, f.id); !m.HasFile || m.MovieFilePath != f.main {
		t.Errorf("default file record cleared: %+v", m)
	}
	if v, _, err := f.svc.repo.GetVersion(ctx, f.vid); err != nil || v.FilePath != f.extra {
		t.Errorf("version changed: %+v, %v", v, err)
	}
}

// With the bin deliberately off, a delete still removes the files for good.
func TestDeleteBinOffRemoves(t *testing.T) {
	f := newDeleteFixture(t, "")
	ctx := context.Background()
	if err := f.svc.Delete(ctx, f.id, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range append([]string{f.main, f.extra, f.extraSub}, f.subs...) {
		if exists(p) {
			t.Errorf("%s should be deleted", filepath.Base(p))
		}
	}
	if _, err := f.svc.repo.Get(ctx, f.id); !errors.Is(err, ErrNotFound) {
		t.Errorf("movie row should be gone, got %v", err)
	}
}

// With the bin on, every file and its subtitles go to the bin, and each removal is
// announced so the import pipeline doesn't import the seeding torrent straight back.
func TestDeleteBinOnRecyclesWithSidecarsAndPublishes(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	f := newDeleteFixture(t, bin)
	events, cancel := f.bus.Subscribe("file.removed")
	defer cancel()
	ctx := context.Background()
	if err := f.svc.Delete(ctx, f.id, true); err != nil {
		t.Fatal(err)
	}
	all := append([]string{f.main, f.extra, f.extraSub}, f.subs...)
	for _, p := range all {
		if exists(p) {
			t.Errorf("%s is still in the library", filepath.Base(p))
		}
		if !exists(filepath.Join(bin, "Heat (1995)", filepath.Base(p))) {
			t.Errorf("%s is not in the bin", filepath.Base(p))
		}
	}
	if exists(filepath.Join(f.root, "Heat (1995)")) {
		t.Error("the emptied movie folder should be pruned")
	}
	if !exists(f.root) {
		t.Error("the library root must never be pruned")
	}
	got := map[string]bool{}
	for len(events) > 0 {
		ev := <-events
		got[ev.Data.(map[string]any)["path"].(string)] = true
	}
	for _, p := range all {
		if !got[p] {
			t.Errorf("no file.removed for %s", filepath.Base(p))
		}
	}
}

// Deleting just the default file leaves the other version's subtitle alone, even though
// its name starts with the default file's.
func TestDeleteFileKeepsOtherVersionsSubtitles(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	f := newDeleteFixture(t, bin)
	if err := f.svc.DeleteFile(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	if exists(f.main) || exists(f.subs[0]) || exists(f.subs[1]) {
		t.Error("the default file and its subtitles should be in the bin")
	}
	if !exists(f.extra) || !exists(f.extraSub) {
		t.Error("the other version's file or subtitle was swept up")
	}
}

// An upgrade whose old file the bin refuses keeps the old file on disk, still records the
// new one, and says so in the movie's history.
func TestUpgradeKeepsOldFileWhenBinRefuses(t *testing.T) {
	f := newDeleteFixture(t, brokenBin(t))
	ctx := context.Background()
	newer := writeFixture(t, filepath.Join(f.root, "Heat (1995)", "Heat (1995) Bluray-2160p.mkv"))
	if err := f.svc.MarkImportedManual(ctx, f.id, newer, ""); err != nil {
		t.Fatalf("the import must not fail: %v", err)
	}
	if !exists(f.main) {
		t.Error("the old file was deleted although the bin refused it")
	}
	if m, _ := f.svc.repo.Get(ctx, f.id); m.MovieFilePath != newer {
		t.Errorf("default file = %q, want the new file", m.MovieFilePath)
	}
	evs, _ := f.svc.repo.Events(ctx, f.id, 10)
	found := false
	for _, e := range evs {
		if e.Event == "file.kept" {
			found = true
		}
	}
	if !found {
		t.Errorf("no file.kept event in %+v", evs)
	}
}

// recordRemoved installs the import pipeline's forget hook and collects what it's told.
func (f *deleteFixture) recordRemoved() *[]string {
	var mu sync.Mutex
	got := &[]string{}
	f.svc.SetOnFileRemoved(func(_ context.Context, path string) {
		mu.Lock()
		defer mu.Unlock()
		*got = append(*got, path)
	})
	return got
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, p := range got {
		seen[p]++
	}
	for _, p := range want {
		if seen[p] == 0 {
			return false
		}
		seen[p]--
	}
	return true
}

func (f *deleteFixture) inBin(bin, path string) bool {
	return bin != "" && exists(filepath.Join(bin, "Heat (1995)", filepath.Base(path)))
}

// With the bin on, deleting the movie with its files moves every file and subtitle to
// the bin, removes the movie, its versions and its history, and tells the import
// pipeline about each file directly (so a still-seeding torrent isn't imported back).
func TestDeleteRecycleOnForgetsEveryFileAndRow(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	f := newDeleteFixture(t, bin)
	removed := f.recordRemoved()
	ctx := context.Background()
	if err := f.svc.Delete(ctx, f.id, true); err != nil {
		t.Fatal(err)
	}
	all := append([]string{f.main, f.extra, f.extraSub}, f.subs...)
	for _, p := range all {
		if exists(p) || !f.inBin(bin, p) {
			t.Errorf("%s should have moved to the bin", filepath.Base(p))
		}
	}
	if !sameSet(*removed, all) {
		t.Errorf("forget hook got %q, want every file and subtitle once: %q", *removed, all)
	}
	if _, err := f.svc.repo.Get(ctx, f.id); !errors.Is(err, ErrNotFound) {
		t.Errorf("movie row should be gone, got %v", err)
	}
	var versions, events int
	_ = f.svc.repo.db.QueryRow(`SELECT COUNT(*) FROM movie_versions WHERE movie_id = ?`, f.id).Scan(&versions)
	_ = f.svc.repo.db.QueryRow(`SELECT COUNT(*) FROM movie_events WHERE movie_id = ?`, f.id).Scan(&events)
	if versions != 0 || events != 0 {
		t.Errorf("left behind %d version and %d history row(s)", versions, events)
	}
}

// Deleting just the default file, bin on and bin off: the file and its subtitles go, the
// movie reads as missing, the other version is untouched.
func TestDeleteFileRecycleOnAndOff(t *testing.T) {
	for _, binOn := range []bool{true, false} {
		t.Run(map[bool]string{true: "bin on", false: "bin off"}[binOn], func(t *testing.T) {
			bin := ""
			if binOn {
				bin = filepath.Join(t.TempDir(), "bin")
			}
			f := newDeleteFixture(t, bin)
			removed := f.recordRemoved()
			ctx := context.Background()
			if err := f.svc.DeleteFile(ctx, f.id); err != nil {
				t.Fatal(err)
			}
			gone := append([]string{f.main}, f.subs...)
			for _, p := range gone {
				if exists(p) {
					t.Errorf("%s is still in the library", filepath.Base(p))
				}
				if binOn != f.inBin(bin, p) {
					t.Errorf("%s in bin = %v, want %v", filepath.Base(p), !binOn, binOn)
				}
			}
			if !exists(f.extra) || !exists(f.extraSub) {
				t.Error("the other version's file or subtitle was touched")
			}
			if !sameSet(*removed, gone) {
				t.Errorf("forget hook got %q, want %q", *removed, gone)
			}
			m, err := f.svc.repo.Get(ctx, f.id)
			if err != nil || m.HasFile || m.MovieFilePath != "" {
				t.Errorf("movie after DeleteFile = %+v, %v; want it missing", m, err)
			}
			if v, _, err := f.svc.repo.GetVersion(ctx, f.vid); err != nil || v.FilePath != f.extra {
				t.Errorf("the extra version changed: %+v, %v", v, err)
			}
		})
	}
}

// Deleting one extra version's file, bin on and bin off: that file and its subtitle go,
// the version track stays (now missing), and the default file is untouched.
func TestDeleteVersionFileRecycleOnAndOff(t *testing.T) {
	for _, binOn := range []bool{true, false} {
		t.Run(map[bool]string{true: "bin on", false: "bin off"}[binOn], func(t *testing.T) {
			bin := ""
			if binOn {
				bin = filepath.Join(t.TempDir(), "bin")
			}
			f := newDeleteFixture(t, bin)
			removed := f.recordRemoved()
			ctx := context.Background()
			if err := f.svc.DeleteVersionFile(ctx, f.id, f.vid); err != nil {
				t.Fatal(err)
			}
			gone := []string{f.extra, f.extraSub}
			for _, p := range gone {
				if exists(p) {
					t.Errorf("%s is still in the library", filepath.Base(p))
				}
				if binOn != f.inBin(bin, p) {
					t.Errorf("%s in bin = %v, want %v", filepath.Base(p), !binOn, binOn)
				}
			}
			for _, p := range append([]string{f.main}, f.subs...) {
				if !exists(p) {
					t.Errorf("%s belongs to the default file and was removed", filepath.Base(p))
				}
			}
			if !sameSet(*removed, gone) {
				t.Errorf("forget hook got %q, want %q", *removed, gone)
			}
			v, _, err := f.svc.repo.GetVersion(ctx, f.vid)
			if err != nil || v.FilePath != "" {
				t.Errorf("version after DeleteVersionFile = %+v, %v; want the track kept without a file", v, err)
			}
			if m, _ := f.svc.repo.Get(ctx, f.id); m.MovieFilePath != f.main {
				t.Errorf("the default file record changed to %q", m.MovieFilePath)
			}
		})
	}
}

// The movie's rows go together or not at all: a failure on the second statement of the
// delete leaves the movie and its versions in place, rather than version rows orphaned
// from a movie that no longer exists.
func TestDeleteRowsAreAtomic(t *testing.T) {
	f := newDeleteFixture(t, filepath.Join(t.TempDir(), "bin"))
	ctx := context.Background()
	if _, err := f.svc.repo.db.Exec(`CREATE TRIGGER fail_version_delete BEFORE DELETE ON movie_versions
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(ctx, f.id, false); err == nil {
		t.Fatal("delete reported success although the version rows couldn't go")
	}
	if m, err := f.svc.repo.Get(ctx, f.id); err != nil || m.MovieFilePath != f.main {
		t.Fatalf("the movie row didn't survive the failed delete: %+v, %v", m, err)
	}
	if vs, _ := f.svc.repo.ListVersions(ctx, f.id); len(vs) != 1 {
		t.Errorf("versions = %+v, want the extra version intact", vs)
	}
	if !exists(f.main) || !exists(f.extra) {
		t.Error("a file was touched by a delete that kept its files")
	}
}
