package recyclebin

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

func newTestSvc(t *testing.T) (*Service, *settings.Service, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	set := settings.NewService(st.DB())
	return New(dir, set, slog.New(slog.NewTextHandler(io.Discard, nil))), set, dir
}

// writeFile drops a file of n bytes into <dir>/<sub>/<name>, aged agoDays days.
func writeFile(t *testing.T, dir, sub, name string, n int, agoDays int) string {
	t.Helper()
	d := filepath.Join(dir, sub)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, name)
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().AddDate(0, 0, -agoDays)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStatsAndEmpty(t *testing.T) {
	svc, _, dir := newTestSvc(t)
	ctx := context.Background()
	writeFile(t, dir, "A (2020)", "a.mkv", 1000, 5)
	writeFile(t, dir, "B (2021)", "b.mkv", 2000, 1)

	st := svc.Stats(ctx)
	if !st.Enabled || st.Files != 2 || st.Bytes != 3000 {
		t.Fatalf("stats = %+v, want 2 files / 3000 bytes", st)
	}
	freed, err := svc.Empty(ctx)
	if err != nil || freed != 3000 {
		t.Fatalf("empty freed=%d err=%v, want 3000", freed, err)
	}
	if st := svc.Stats(ctx); st.Files != 0 || st.Bytes != 0 {
		t.Fatalf("after empty = %+v, want zero", st)
	}
}

func TestEnforceRetention(t *testing.T) {
	svc, set, dir := newTestSvc(t)
	ctx := context.Background()
	_ = set.Set(ctx, keyRetention, "7") // keep 7 days
	old := writeFile(t, dir, "old", "o.mkv", 100, 30)
	recent := writeFile(t, dir, "new", "n.mkv", 100, 2)

	svc.Enforce(ctx)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("expected the 30-day-old file to be purged")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("expected the 2-day-old file to survive retention")
	}
}

func TestEnforceMaxSize(t *testing.T) {
	svc, set, dir := newTestSvc(t)
	ctx := context.Background()
	_ = set.Set(ctx, keyMaxGB, "1") // 1 GiB cap
	gib := 1 << 30
	oldest := writeFile(t, dir, "x", "oldest.mkv", gib, 10) // over cap once combined
	mid := writeFile(t, dir, "x", "mid.mkv", gib/2, 5)
	newest := writeFile(t, dir, "x", "newest.mkv", gib/2, 1)

	svc.Enforce(ctx) // total 2 GiB > 1 GiB → drop oldest-first
	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Error("expected the oldest file to be purged to get under the cap")
	}
	if _, err := os.Stat(mid); err != nil {
		t.Error("mid file should remain")
	}
	if _, err := os.Stat(newest); err != nil {
		t.Error("newest file should remain")
	}
}
