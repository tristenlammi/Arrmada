package automation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/audiobook"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// mergeFixture is a three-chapter audiobook of synthetic files in a temp library, with
// ffmpeg and ffprobe replaced by fakes. Nothing here touches real media.
type mergeFixture struct {
	c       *Coordinator
	svc     *books.Service
	id      int64
	root    string // audiobooks root
	dir     string // the book's folder
	sources []string
	outSecs float64 // what the fake probe says the merged file lasts
}

func newMergeFixture(t *testing.T) *mergeFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := books.NewService(st.DB(), nil, log)
	ctx := context.Background()
	added, _ := svc.AddWorks(ctx, []metadata.BookResult{{Key: "hc:9", Title: "The Tone Book", Author: "A. Sine"}}, "", true)
	if len(added) != 1 {
		t.Fatal("book not added")
	}
	root := t.TempDir()
	f := &mergeFixture{svc: svc, id: added[0].ID, root: root, dir: filepath.Join(root, "A. Sine", "The Tone Book"), outSecs: 300}
	for _, n := range []string{"Chapter 1.mp3", "Chapter 2.mp3", "Chapter 10.mp3"} {
		p := filepath.Join(f.dir, n)
		if err := os.MkdirAll(f.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("chapter audio "+n), 0o644); err != nil {
			t.Fatal(err)
		}
		f.sources = append(f.sources, p)
	}
	if err := svc.MarkImported(ctx, f.id, books.KindAudiobook, f.dir, "MP3", 100, 3); err != nil {
		t.Fatal(err)
	}
	imp := library.NewImporter(t.TempDir(), log)
	imp.SetRoots("", "", "", root)
	f.c = &Coordinator{books: svc, log: log, imp: imp}
	f.c.mergeFn = func(_ context.Context, files []string, out string, _ audiobook.MergeOptions) (audiobook.MergeResult, error) {
		if err := os.WriteFile(out, []byte("merged"), 0o644); err != nil {
			return audiobook.MergeResult{}, err
		}
		return audiobook.MergeResult{}, nil
	}
	f.c.durationFn = func(_ context.Context, p string) (float64, error) {
		if strings.HasSuffix(p, ".mp3") {
			return 100, nil
		}
		return f.outSecs, nil
	}
	return f
}

func (f *mergeFixture) sourcesIntact(t *testing.T) {
	t.Helper()
	for _, p := range f.sources {
		b, err := os.ReadFile(p)
		if err != nil || !strings.HasPrefix(string(b), "chapter audio") {
			t.Fatalf("source %s was touched: %v", p, err)
		}
	}
}

// noM4B asserts no merged file or temp file was left in the book folder.
func (f *mergeFixture) noM4B(t *testing.T) {
	t.Helper()
	ents, _ := os.ReadDir(f.dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".m4b") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

func (f *mergeFixture) lastEvent(t *testing.T) (string, string) {
	t.Helper()
	evs, err := f.svc.Events(context.Background(), f.id, 5)
	if err != nil || len(evs) == 0 {
		t.Fatalf("no book events: %v", err)
	}
	return evs[0].Event, evs[0].Detail
}

func TestMergeKeepsSourcesWhenOutputIsShort(t *testing.T) {
	f := newMergeFixture(t)
	f.outSecs = 150 // half the 300 s the chapters add up to
	if err := f.c.MergeAudiobook(context.Background(), f.id); err == nil {
		t.Fatal("a short merge must fail")
	}
	f.sourcesIntact(t)
	f.noM4B(t)
	if ev, detail := f.lastEvent(t); ev != "merge-failed" || !strings.Contains(detail, "add up to") {
		t.Errorf("event = %s %q, want merge-failed naming the lengths", ev, detail)
	}

	// Within max(1%, 5 s) passes.
	f.outSecs = 296
	if err := f.c.MergeAudiobook(context.Background(), f.id); err != nil {
		t.Fatalf("a merge 4 s short of 300 s should pass: %v", err)
	}
}

func TestMergeFailureLeavesNoPartialFile(t *testing.T) {
	f := newMergeFixture(t)
	f.c.mergeFn = func(_ context.Context, _ []string, out string, _ audiobook.MergeOptions) (audiobook.MergeResult, error) {
		_ = os.WriteFile(out, []byte("half a file"), 0o644)
		return audiobook.MergeResult{}, errors.New("ffmpeg exited 1")
	}
	if err := f.c.MergeAudiobook(context.Background(), f.id); err == nil {
		t.Fatal("expected the merge to fail")
	}
	f.sourcesIntact(t)
	f.noM4B(t)
	if ev, detail := f.lastEvent(t); ev != "merge-failed" || !strings.Contains(detail, "ffmpeg exited 1") {
		t.Errorf("event = %s %q", ev, detail)
	}
	// An unmeasurable source also fails safe.
	f.c.mergeFn = newMergeFixture(t).c.mergeFn
	f.c.durationFn = func(context.Context, string) (float64, error) { return 0, errors.New("no ffprobe") }
	if err := f.c.MergeAudiobook(context.Background(), f.id); err == nil {
		t.Fatal("a merge that can't be checked must fail")
	}
	f.sourcesIntact(t)
	f.noM4B(t)
}

func TestMergeMovesSourcesNotDeletes(t *testing.T) {
	t.Run("bin on", func(t *testing.T) {
		f := newMergeFixture(t)
		bin := filepath.Join(t.TempDir(), "bin")
		f.c.recycle = bin
		if err := f.c.MergeAudiobook(context.Background(), f.id); err != nil {
			t.Fatal(err)
		}
		final := filepath.Join(f.dir, "The Tone Book.m4b")
		if _, err := os.Stat(final); err != nil {
			t.Fatalf("merged file missing: %v", err)
		}
		for _, p := range f.sources {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("%s still in the book folder", p)
			}
		}
		n := 0
		_ = filepath.WalkDir(bin, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && filepath.Ext(p) == ".mp3" {
				n++
				if library.ReadRecycleMeta(p).Orig == "" {
					t.Errorf("%s isn't restorable", p)
				}
			}
			return nil
		})
		if n != 3 {
			t.Errorf("bin holds %d sources, want 3", n)
		}
		b, _ := f.svc.Get(context.Background(), f.id)
		if b.Audiobook == nil || b.Audiobook.Path != final || b.Audiobook.FileCount != 1 {
			t.Errorf("edition = %+v, want the single m4b", b.Audiobook)
		}
		if ev, detail := f.lastEvent(t); ev != "merged" || !strings.Contains(detail, "recycle bin") || !strings.Contains(detail, "download it again") {
			t.Errorf("event = %s %q", ev, detail)
		}
	})
	t.Run("bin off keeps a backup", func(t *testing.T) {
		f := newMergeFixture(t)
		if err := f.c.MergeAudiobook(context.Background(), f.id); err != nil {
			t.Fatal(err)
		}
		backups, _ := filepath.Glob(filepath.Join(f.root, mergeBackupDir, "*", "*.mp3"))
		if len(backups) != 3 {
			t.Fatalf("backup holds %v, want the 3 sources", backups)
		}
		for _, p := range f.sources {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("%s still in the book folder", p)
			}
		}
		// A later scan of the book doesn't see the backup: it's outside the book folder.
		if got := library.FindBookFiles(f.dir); len(got) != 1 {
			t.Errorf("book folder holds %d book files, want just the m4b", len(got))
		}
		if _, detail := f.lastEvent(t); !strings.Contains(detail, "14 days") {
			t.Errorf("event detail = %q, want it to say the originals are kept for 14 days", detail)
		}
	})
}

// A source already named like the output is never overwritten.
func TestMergeTargetAvoidsSourceName(t *testing.T) {
	dir := t.TempDir()
	if got := mergeTarget(dir, "Book"); filepath.Base(got) != "Book.m4b" {
		t.Errorf("free name: %s", got)
	}
	_ = os.WriteFile(filepath.Join(dir, "Book.m4b"), nil, 0o644)
	if got := mergeTarget(dir, "Book"); filepath.Base(got) != "Book (merged).m4b" {
		t.Errorf("taken name: %s", got)
	}
}

func TestSecondMergeIsRejectedWhileRunning(t *testing.T) {
	f := newMergeFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	write := f.c.mergeFn
	f.c.mergeFn = func(ctx context.Context, files []string, out string, o audiobook.MergeOptions) (audiobook.MergeResult, error) {
		close(started)
		<-release
		return write(ctx, files, out, o)
	}
	done := make(chan error, 1)
	go func() { done <- f.c.MergeAudiobook(context.Background(), f.id) }()
	<-started
	if !f.c.MergingAudiobook(f.id) {
		t.Error("MergingAudiobook should report the running merge")
	}
	if err := f.c.MergeAudiobook(context.Background(), f.id); !errors.Is(err, ErrAlreadyMerging) {
		t.Errorf("second merge: %v, want ErrAlreadyMerging", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if f.c.MergingAudiobook(f.id) {
		t.Error("the guard must clear when the merge ends")
	}
}

func TestMergeBackupPrune(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, mergeBackupDir)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	old := filepath.Join(dir, "7-"+now.Add(-15*24*time.Hour).Format("20060102T150405Z"))
	fresh := filepath.Join(dir, "8-"+now.Add(-13*24*time.Hour).Format("20060102T150405Z"))
	for _, d := range []string{old, fresh} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(d, "01.mp3"), []byte("x"), 0o644)
	}
	if n := pruneMergeBackups(dir, now, mergeBackupKeep); n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("a 15-day-old backup should be gone")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a 13-day-old backup must be kept")
	}
}
