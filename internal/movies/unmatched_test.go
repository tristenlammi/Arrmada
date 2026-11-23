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
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// The needs-review list is kept in the database: a new Service over the same database (a
// restart) still lists the folders, a manual pick takes its folder off, and the next scan
// replaces the list rather than adding to it.
func TestUnmatchedPersisted(t *testing.T) {
	svc, _, root := scanFixture(t, "Mystery Film (2001)/Mystery.Film.2001.mkv", "Odd Thing (1999)/Odd.Thing.1999.mkv", "Movie (2010)/Movie.2010.mkv")
	ctx := context.Background()
	res, err := svc.ScanLibrary(ctx, "", ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 || len(res.Unmatched) != 2 {
		t.Fatalf("scan = %+v, want 1 imported and 2 unmatched", res)
	}
	for _, u := range res.Unmatched {
		if u.Candidates == nil {
			t.Errorf("scan result: %q has candidates nil, want [] (#136)", u.Folder)
		}
	}

	restarted := NewService(svc.repo.db, svc.meta, nil, root, "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	list, err := restarted.LastUnmatched(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Folder != "Mystery Film (2001)" || list[0].Title != "Mystery Film" || list[0].Year != 2001 || list[1].Folder != "Odd Thing (1999)" {
		t.Fatalf("after a restart = %+v", list)
	}
	if list[0].Candidates == nil {
		// Candidates decode as an empty list, never null, so the page can range over them.
		t.Errorf("candidates = nil, want []")
	}

	// A manual pick settles a folder and takes it off the list.
	restarted.meta = fakeMeta{films: append(svc.meta.(fakeMeta).films, metadata.MovieResult{TMDBID: 30, Title: "Odd Thing", Year: 1999})}
	if err := restarted.ImportFolderAs(ctx, "", "Odd Thing (1999)", 30); err != nil {
		t.Fatal(err)
	}
	list, _ = restarted.LastUnmatched(ctx)
	if len(list) != 1 || list[0].Folder != "Mystery Film (2001)" {
		t.Fatalf("after the manual pick = %+v", list)
	}

	// The next scan replaces the list: a folder that's gone from disk is gone from it.
	if err := os.RemoveAll(filepath.Join(root, "Mystery Film (2001)")); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ScanLibrary(ctx, "", ScanOptions{}); err != nil {
		t.Fatal(err)
	}
	if list, _ = restarted.LastUnmatched(ctx); len(list) != 0 {
		t.Fatalf("after a rescan = %+v, want none", list)
	}
}

// A scan reports its progress folder by folder, ending at total/total; the recycle bin and
// hidden folders aren't counted.
func TestScanProgressCallback(t *testing.T) {
	svc, _, root := scanFixture(t, "Movie (2010)/Movie.2010.mkv", "Other (2020)/Other.2020.mkv", "Nope (2001)/Nope.mkv")
	writeFixture(t, filepath.Join(root, ".arrmada-recycle", "old.mkv"))
	var calls [][2]int
	_, err := svc.ScanLibrary(context.Background(), "", ScanOptions{Progress: func(done, total int) {
		calls = append(calls, [2]int{done, total})
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]int{{0, 3}, {1, 3}, {2, 3}, {3, 3}}
	if len(calls) != len(want) {
		t.Fatalf("progress calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("progress calls = %v, want %v", calls, want)
		}
	}
}

// slowMeta holds every search until released, so a scan can be caught mid-run.
type slowMeta struct {
	fakeMeta
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *slowMeta) SearchMovie(ctx context.Context, q string) ([]metadata.MovieResult, error) {
	m.once.Do(func() { close(m.started) })
	select {
	case <-m.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return m.fakeMeta.SearchMovie(ctx, q)
}

// Only one scan runs at a time: a second one while the first is still going is refused
// with ErrScanRunning instead of racing it.
func TestScanRejectsConcurrent(t *testing.T) {
	svc, _, _ := scanFixture(t, "Movie (2010)/Movie.2010.mkv")
	sm := &slowMeta{fakeMeta: svc.meta.(fakeMeta), started: make(chan struct{}), release: make(chan struct{})}
	svc.meta = sm
	done := make(chan error, 1)
	go func() {
		_, err := svc.ScanLibrary(context.Background(), "", ScanOptions{})
		done <- err
	}()
	select {
	case <-sm.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first scan never started")
	}
	if _, err := svc.ScanLibrary(context.Background(), "", ScanOptions{}); !errors.Is(err, ErrScanRunning) {
		t.Fatalf("second scan err = %v, want ErrScanRunning", err)
	}
	close(sm.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Once it's finished, scanning works again.
	if _, err := svc.ScanLibrary(context.Background(), "", ScanOptions{}); err != nil {
		t.Fatalf("scan after the first finished: %v", err)
	}
}

// A scan stopped part-way keeps the review rows of folders it never reached, and says how
// far it got.
func TestStoppedScanKeepsReviewRows(t *testing.T) {
	svc, _, root := scanFixture(t, "Mystery Film (2001)/Mystery.Film.2001.mkv")
	ctx := context.Background()
	if _, err := svc.ScanLibrary(ctx, "", ScanOptions{}); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.ScanLibrary(cctx, root, ScanOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan err = %v", err)
	}
	if list, _ := svc.LastUnmatched(ctx); len(list) != 1 {
		t.Fatalf("after a stopped scan = %+v, want the earlier row kept", list)
	}
}

func TestScanBudget(t *testing.T) {
	if got := ScanBudget(10); got != 15*time.Minute {
		t.Errorf("ScanBudget(10) = %v, want the 15m floor", got)
	}
	if got := ScanBudget(1200); got != 40*time.Minute {
		t.Errorf("ScanBudget(1200) = %v, want 40m", got)
	}
}
