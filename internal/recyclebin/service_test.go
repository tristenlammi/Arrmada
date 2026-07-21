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

	"github.com/tristenlammi/arrmada/internal/library"
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

func TestListRestoreDelete(t *testing.T) {
	svc, _, binDir := newTestSvc(t)
	ctx := context.Background()

	// A real file in a "library", recycled through the shared RecycleFile (records origin).
	lib := t.TempDir()
	orig := filepath.Join(lib, "Movie (2024)", "movie.mkv")
	if err := os.MkdirAll(filepath.Dir(orig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orig, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := library.RecycleFile(binDir, orig); err != nil {
		t.Fatal(err)
	}

	items := svc.List(ctx)
	if len(items) != 1 || items[0].Name != "movie.mkv" || items[0].OrigPath != orig || !items[0].Restorable {
		t.Fatalf("List = %+v; want one restorable movie.mkv with origin", items)
	}
	// The sidecar must not be counted as a file.
	if st := svc.Stats(ctx); st.Files != 1 {
		t.Fatalf("Stats.Files = %d, want 1 (sidecar excluded)", st.Files)
	}

	// Restore puts it back and clears the bin.
	if err := svc.Restore(ctx, items[0].ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(orig); err != nil {
		t.Error("file should be back at its original path")
	}
	if len(svc.List(ctx)) != 0 {
		t.Error("bin should be empty after restore")
	}

	// Restore refuses when the origin is occupied.
	library.RecycleFile(binDir, orig) // re-delete (orig still exists from restore)
	os.WriteFile(orig, []byte("new"), 0o644)
	if err := svc.Restore(ctx, svc.List(ctx)[0].ID); err == nil {
		t.Error("restore should refuse when a file already occupies the origin")
	}
	// DeleteItem removes it (and its sidecar).
	id := svc.List(ctx)[0].ID
	if err := svc.DeleteItem(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(svc.List(ctx)) != 0 {
		t.Error("bin should be empty after delete")
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

// TestEnforcePrefersSidecarDeleted pins the retention fix: the .arrmeta sidecar's
// Deleted timestamp is the authoritative age — file mtime is only a fallback for
// legacy items with no sidecar. A file whose content mtime is ancient but that was
// recycled recently must survive; one recycled long ago must be purged even if its
// mtime was refreshed (e.g. a failed Chtimes at recycle time, or a later touch).
func TestEnforcePrefersSidecarDeleted(t *testing.T) {
	svc, set, dir := newTestSvc(t)
	ctx := context.Background()
	_ = set.Set(ctx, keyRetention, "7")

	writeSidecar := func(p string, deletedAgoDays int) {
		t.Helper()
		m := library.RecycleMeta{Orig: "/orig/" + filepath.Base(p), Deleted: time.Now().AddDate(0, 0, -deletedAgoDays).Unix()}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p+library.RecycleMetaExt, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	oldMtimeRecentDelete := writeFile(t, dir, "a", "keep.mkv", 100, 30) // mtime 30d ago…
	writeSidecar(oldMtimeRecentDelete, 1)                               // …but deleted yesterday
	newMtimeOldDelete := writeFile(t, dir, "b", "purge.mkv", 100, 0)    // fresh mtime…
	writeSidecar(newMtimeOldDelete, 30)                                 // …but deleted 30d ago
	legacyOld := writeFile(t, dir, "c", "legacy.mkv", 100, 30)          // no sidecar → mtime rules

	svc.Enforce(ctx)
	if _, err := os.Stat(oldMtimeRecentDelete); err != nil {
		t.Error("recently-deleted file must survive despite its old content mtime")
	}
	if _, err := os.Stat(newMtimeOldDelete); !os.IsNotExist(err) {
		t.Error("long-ago-deleted file must be purged despite its fresh mtime")
	}
	if _, err := os.Stat(legacyOld); !os.IsNotExist(err) {
		t.Error("legacy sidecar-less file must still age by mtime")
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
