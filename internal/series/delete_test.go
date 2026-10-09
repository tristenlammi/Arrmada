package series

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/store"
)

// deleteFixture is a show with real (tiny, synthetic) files in a temp library and a temp
// recycle bin. Nothing here ever touches a real library.
type deleteFixture struct {
	svc  *Service
	root string
	bin  string
	id   int64
	show string // the show's folder
}

func newDeleteFixture(t *testing.T) *deleteFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	f := &deleteFixture{
		svc:  NewService(st.DB(), nil, root, slog.New(slog.NewTextHandler(io.Discard, nil))),
		root: root,
		bin:  filepath.Join(t.TempDir(), "bin"),
		show: filepath.Join(root, "The Bear (2022)"),
	}
	f.svc.SetRecycleDir(f.bin)
	ctx := context.Background()
	res, err := st.DB().ExecContext(ctx, `INSERT INTO series (tmdb_id, title, monitored) VALUES (7, 'The Bear', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	f.id, _ = res.LastInsertId()
	for _, e := range []int{1, 2, 3} {
		if _, err := st.DB().ExecContext(ctx, `INSERT INTO episodes (series_id, season_number, episode_number, monitored) VALUES (?, 1, ?, 1)`, f.id, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO seasons (series_id, season_number, monitored) VALUES (?, 1, 1)`, f.id); err != nil {
		t.Fatal(err)
	}
	return f
}

// file writes a synthetic file under the show folder and returns its path.
func (f *deleteFixture) file(t *testing.T, rel, body string) string {
	t.Helper()
	p := filepath.Join(f.show, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// episode gives episode e of season 1 a video file (plus the named subtitle suffixes).
func (f *deleteFixture) episode(t *testing.T, e int, subs ...string) string {
	t.Helper()
	base := filepath.Join("Season 1", "The Bear - S01E0"+string(rune('0'+e)))
	v := f.file(t, base+".mkv", "video")
	for _, s := range subs {
		f.file(t, base+s, "subs")
	}
	if err := f.svc.MarkEpisodeImported(context.Background(), f.id, 1, e, v, 5); err != nil {
		t.Fatal(err)
	}
	return v
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func binFiles(t *testing.T, bin string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(bin, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(p) != library.RecycleMetaExt {
			out = append(out, filepath.Base(p))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func (f *deleteFixture) seriesExists(t *testing.T) bool {
	t.Helper()
	_, err := f.svc.repo.Get(context.Background(), f.id)
	return err == nil
}

// Videos and their subtitles (language and forced suffixes included) go to the bin,
// restorable; emptied folders go; the show's rows go; the library root stays.
func TestDeleteSeriesRecyclesVideosAndSidecars(t *testing.T) {
	f := newDeleteFixture(t)
	f.episode(t, 1, ".en.srt", ".en.forced.srt")
	f.episode(t, 2, ".srt")
	unrelated := f.file(t, filepath.Join("Season 1", "notes.srt"), "not a sidecar of anything")

	plan, err := f.svc.DeletePlan(context.Background(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Files != 2 || plan.Sidecars != 3 || plan.Bytes != 2*5+3*4 {
		t.Fatalf("plan = %+v, want 2 files, 3 sidecars, 22 bytes", plan)
	}

	sum, err := f.svc.Delete(context.Background(), f.id, true)
	if err != nil {
		t.Fatalf("delete: %v (summary %+v)", err, sum)
	}
	want := []string{"The Bear - S01E01.en.forced.srt", "The Bear - S01E01.en.srt", "The Bear - S01E01.mkv", "The Bear - S01E02.mkv", "The Bear - S01E02.srt"}
	got := binFiles(t, f.bin)
	if len(got) != len(want) {
		t.Fatalf("bin holds %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bin holds %v, want %v", got, want)
		}
	}
	if len(sum.Failed) != 0 || len(sum.Moved) != 5 {
		t.Errorf("summary = %+v", sum)
	}
	if f.seriesExists(t) {
		t.Error("series row should be gone once every file moved")
	}
	if !exists(unrelated) {
		t.Error("an unrelated subtitle next to the episodes must be left alone")
	}
	if !exists(f.root) {
		t.Fatal("the library root must never be pruned")
	}

	// With the unrelated file gone too, a second show's delete empties the folders.
	f2 := newDeleteFixture(t)
	f2.episode(t, 1)
	if _, err := f2.svc.Delete(context.Background(), f2.id, true); err != nil {
		t.Fatal(err)
	}
	if exists(f2.show) {
		t.Error("emptied season and show folders should be removed")
	}
	if !exists(f2.root) {
		t.Error("the library root must survive its last show being deleted")
	}
	// Every recycled file is restorable: its sidecar records where it came from.
	_ = filepath.WalkDir(f2.bin, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(p) != library.RecycleMetaExt && library.ReadRecycleMeta(p).Orig == "" {
			t.Errorf("%s has no recorded origin", p)
		}
		return nil
	})
}

// A bin that can't be created (here: a regular file where the folder should be) stops
// the delete before anything moves, and the show stays.
func TestDeleteSeriesAbortsOnRecycleFailure(t *testing.T) {
	f := newDeleteFixture(t)
	v1 := f.episode(t, 1, ".srt")
	if err := os.WriteFile(f.bin, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := f.svc.Delete(context.Background(), f.id, true)
	if !errors.Is(err, ErrFilesNotRemoved) {
		t.Fatalf("err = %v, want ErrFilesNotRemoved", err)
	}
	if len(sum.Failed) == 0 || len(sum.Moved) != 0 {
		t.Errorf("summary = %+v, want failures and nothing moved", sum)
	}
	if !f.seriesExists(t) || !exists(v1) {
		t.Fatal("the series and its file must be untouched")
	}
}

// stingyBin takes the first file and refuses everything after it.
type stingyBin struct {
	dir   string
	taken int
}

func (b *stingyBin) For(string) (string, error) {
	b.taken++
	if b.taken > 2 { // the pre-flight check and the first file
		return "", errors.New("disk full")
	}
	return b.dir, nil
}

// A failure part-way through keeps the show, reports what moved and what didn't, and the
// episodes already moved read as missing — the library tells the truth.
func TestDeleteSeriesPartialFailureMarksMovedMissing(t *testing.T) {
	f := newDeleteFixture(t)
	v1 := f.episode(t, 1)
	v2 := f.episode(t, 2)
	f.svc.bin = &stingyBin{dir: f.bin}

	sum, err := f.svc.Delete(context.Background(), f.id, true)
	if !errors.Is(err, ErrFilesNotRemoved) {
		t.Fatalf("err = %v, want ErrFilesNotRemoved", err)
	}
	if len(sum.Moved) != 1 || len(sum.Failed) != 1 {
		t.Fatalf("summary = %+v, want one moved, one failed", sum)
	}
	if exists(v1) || !exists(v2) {
		t.Fatalf("v1 should be in the bin and v2 untouched (v1 exists=%v, v2 exists=%v)", exists(v1), exists(v2))
	}
	if !f.seriesExists(t) {
		t.Fatal("the series must be kept after a partial failure")
	}
	if p, _ := f.svc.repo.EpisodeFilePath(context.Background(), f.id, 1, 1); p != "" {
		t.Errorf("E01 was moved but still claims %s", p)
	}
	if p, _ := f.svc.repo.EpisodeFilePath(context.Background(), f.id, 1, 2); p != v2 {
		t.Errorf("E02 wasn't moved but reads %q", p)
	}
}

// A double-episode file serves two rows; it's counted and moved once.
func TestDeleteSeriesSharedDoubleEpisodeFileOnce(t *testing.T) {
	f := newDeleteFixture(t)
	v := f.file(t, filepath.Join("Season 1", "The Bear - S01E01-E02.mkv"), "video")
	for _, e := range []int{1, 2} {
		if err := f.svc.MarkEpisodeImported(context.Background(), f.id, 1, e, v, 5); err != nil {
			t.Fatal(err)
		}
	}
	plan, _ := f.svc.DeletePlan(context.Background(), f.id)
	if plan.Files != 1 || plan.Bytes != 5 {
		t.Fatalf("plan = %+v, want the shared file once", plan)
	}
	sum, err := f.svc.Delete(context.Background(), f.id, true)
	if err != nil || len(sum.Moved) != 1 {
		t.Fatalf("delete: %v, summary %+v", err, sum)
	}
}

// Each deleted file is announced, so the import pipeline marks its record removed.
func TestDeleteSeriesPublishesFileRemoved(t *testing.T) {
	f := newDeleteFixture(t)
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.svc.SetBus(bus)
	events, cancel := bus.Subscribe("file.removed")
	defer cancel()
	v1 := f.episode(t, 1, ".srt")
	v2 := f.episode(t, 2)

	if _, err := f.svc.Delete(context.Background(), f.id, true); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for len(events) > 0 {
		ev := <-events
		if m, ok := ev.Data.(map[string]any); ok {
			got[m["path"].(string)] = true
		}
	}
	for _, p := range []string{v1, v2} {
		if !got[p] {
			t.Errorf("no file.removed for %s (got %v)", p, got)
		}
	}
}

// With the bin deliberately off, deleting files really deletes them.
func TestDeleteSeriesBinOffHardDeletes(t *testing.T) {
	f := newDeleteFixture(t)
	f.svc.SetRecycleDir("")
	v1 := f.episode(t, 1, ".srt")
	if _, err := f.svc.Delete(context.Background(), f.id, true); err != nil {
		t.Fatal(err)
	}
	if exists(v1) || exists(f.bin) {
		t.Error("bin off: the file should be gone and no bin created")
	}
	if f.seriesExists(t) {
		t.Error("series should be deleted")
	}
}

// Without delete_files nothing on disk changes.
func TestDeleteSeriesKeepsFilesByDefault(t *testing.T) {
	f := newDeleteFixture(t)
	v1 := f.episode(t, 1, ".srt")
	if _, err := f.svc.Delete(context.Background(), f.id, false); err != nil {
		t.Fatal(err)
	}
	if !exists(v1) || f.seriesExists(t) {
		t.Error("rows-only delete must keep the file and remove the series")
	}
}

// A single-episode delete refuses rather than hard-deleting when the bin fails, and
// recycles the subtitles with the video when it works.
func TestDeleteEpisodeFileRefusesOnRecycleFailure(t *testing.T) {
	f := newDeleteFixture(t)
	v1 := f.episode(t, 1, ".en.srt")
	if err := os.WriteFile(f.bin, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := f.svc.DeleteEpisodeFile(context.Background(), f.id, 1, 1)
	if !errors.Is(err, library.ErrBinRefused) {
		t.Fatalf("err = %v, want a bin refusal", err)
	}
	if !exists(v1) {
		t.Fatal("the episode file was deleted even though the bin refused it")
	}
	if p, _ := f.svc.repo.EpisodeFilePath(context.Background(), f.id, 1, 1); p != v1 {
		t.Error("the episode must still have its file after a refusal")
	}

	_ = os.Remove(f.bin)
	if err := f.svc.DeleteEpisodeFile(context.Background(), f.id, 1, 1); err != nil {
		t.Fatal(err)
	}
	if got := binFiles(t, f.bin); len(got) != 2 {
		t.Errorf("bin = %v, want the video and its subtitle", got)
	}
}

// A single-episode delete with the bin on and with it off: the video and its subtitles
// go (to the bin, or for good), each is announced so the import pipeline forgets it,
// the episode reads as missing, and the show's other episodes and rows are untouched.
func TestDeleteEpisodeFileRecycleOnAndOff(t *testing.T) {
	for _, binOn := range []bool{true, false} {
		t.Run(map[bool]string{true: "bin on", false: "bin off"}[binOn], func(t *testing.T) {
			f := newDeleteFixture(t)
			if !binOn {
				f.svc.SetRecycleDir("")
			}
			bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
			f.svc.SetBus(bus)
			events, cancel := bus.Subscribe("file.removed")
			defer cancel()
			ctx := context.Background()
			v1 := f.episode(t, 1, ".en.srt", ".en.forced.srt")
			v2 := f.episode(t, 2, ".en.srt")
			subs1 := []string{
				filepath.Join(filepath.Dir(v1), "The Bear - S01E01.en.srt"),
				filepath.Join(filepath.Dir(v1), "The Bear - S01E01.en.forced.srt"),
			}

			if err := f.svc.DeleteEpisodeFile(ctx, f.id, 1, 1); err != nil {
				t.Fatal(err)
			}
			for _, p := range append([]string{v1}, subs1...) {
				if exists(p) {
					t.Errorf("%s is still in the library", filepath.Base(p))
				}
			}
			want := []string{}
			if binOn {
				want = []string{"The Bear - S01E01.en.forced.srt", "The Bear - S01E01.en.srt", "The Bear - S01E01.mkv"}
			} else if exists(f.bin) {
				t.Error("bin off, but a bin was created")
			}
			if got := binFiles(t, f.bin); len(got) != len(want) || (len(want) > 0 && strings.Join(got, "|") != strings.Join(want, "|")) {
				t.Errorf("bin = %v, want %v", got, want)
			}
			got := map[string]bool{}
			for len(events) > 0 {
				if m, ok := (<-events).Data.(map[string]any); ok {
					got[m["path"].(string)] = true
				}
			}
			for _, p := range append([]string{v1}, subs1...) {
				if !got[p] {
					t.Errorf("no file.removed for %s (got %v)", filepath.Base(p), got)
				}
			}
			if p, _ := f.svc.repo.EpisodeFilePath(ctx, f.id, 1, 1); p != "" {
				t.Errorf("episode 1 still points at %q", p)
			}
			if p, _ := f.svc.repo.EpisodeFilePath(ctx, f.id, 1, 2); p != v2 || !exists(v2) {
				t.Errorf("episode 2 was touched: %q", p)
			}
			if !f.seriesExists(t) {
				t.Error("deleting one episode's file removed the show")
			}
		})
	}
}

// An upgrade whose old file the bin refuses keeps the old file on disk instead of deleting
// it for good, still records the new file, and says so in the show's history.
func TestSupersedeKeepsOldOnRecycleFailure(t *testing.T) {
	f := newDeleteFixture(t)
	old := f.episode(t, 1)
	if err := os.WriteFile(f.bin, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	newer := f.file(t, filepath.Join("Season 1", "The Bear - S01E01 Bluray-2160p.mkv"), "better video")
	ctx := context.Background()
	if err := f.svc.SupersedeEpisodeFile(ctx, f.id, 1, 1, newer, 12, "The.Bear.S01E01.2160p"); err != nil {
		t.Fatalf("the import must not fail: %v", err)
	}
	if !exists(old) {
		t.Fatal("the old file was deleted although the bin refused it")
	}
	if p, _ := f.svc.repo.EpisodeFilePath(ctx, f.id, 1, 1); p != newer {
		t.Errorf("episode file = %q, want the new file", p)
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
