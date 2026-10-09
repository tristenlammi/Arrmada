package movies

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	if err := f.svc.repo.SetVersionFile(ctx, f.vid, f.extra, 1); err != nil {
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
