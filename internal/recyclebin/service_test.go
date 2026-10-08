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

func newTestSvc(t *testing.T) (*Service, *settings.Service, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	set := settings.NewService(st.DB())
	svc := New(dir, set, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// The test machine's real disk mustn't decide the outcome: free space reads as unknown
	// unless a test says otherwise.
	svc.free = func(string) (diskspace.Usage, bool) { return diskspace.Usage{}, false }
	return svc, set, dir
}

// writeFile drops a file of n bytes into <dir>/<sub>/<name>, aged agoDays days.
func writeFile(t *testing.T, dir, sub, name string, n int, agoDays int) string {
	t.Helper()
	d := filepath.Join(dir, sub)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, name)
	// Sized by truncation: the bin only reads sizes, and a sparse file means a GiB-sized
	// test item costs neither a GiB of memory nor of disk.
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(int64(n)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
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

func gone(p string) bool { _, err := os.Stat(p); return os.IsNotExist(err) }

// A 60 GB remux recycled today survives a 50 GB cap — it's the newest item and inside
// the three-day hold — while older items are still purged to make room.
func TestEnforceKeepsNewestEvenIfOverCap(t *testing.T) {
	svc, set, dir := newTestSvc(t)
	ctx := context.Background()
	_ = set.Set(ctx, keyMaxGB, "50")
	_ = set.Set(ctx, keyRetention, "0")
	gib := 1 << 30
	old := writeFile(t, dir, "a", "old.mkv", 10*gib, 20)
	remux := writeFile(t, dir, "b", "remux.mkv", 60*gib, 0)

	svc.Enforce(ctx)
	if gone(remux) {
		t.Fatal("the remux recycled today was purged by the size cap")
	}
	if !gone(old) {
		t.Error("the older, unprotected item should be purged first")
	}

	// Still the newest after the hold expires: the cap still leaves it alone.
	svc.now = func() time.Time { return time.Now().Add(10 * 24 * time.Hour) }
	svc.Enforce(ctx)
	if gone(remux) {
		t.Fatal("the single newest item must never be purged by the cap")
	}
	st := svc.Stats(ctx)
	if st.OverCapBytes != 10*int64(gib) || st.LargestItemBytes != 60*int64(gib) || st.ProtectedBytes != 60*int64(gib) {
		t.Errorf("stats = %+v, want over 10 GiB, largest 60 GiB, protected 60 GiB", st)
	}
}

// Anything deleted in the last 72 hours is held, even when it isn't the newest.
func TestEnforceSkipsItemsYoungerThan72h(t *testing.T) {
	svc, set, dir := newTestSvc(t)
	ctx := context.Background()
	_ = set.Set(ctx, keyMaxGB, "1")
	_ = set.Set(ctx, keyRetention, "0")
	gib := 1 << 30
	old := writeFile(t, dir, "a", "old.mkv", gib, 10)
	young1 := writeFile(t, dir, "b", "young1.mkv", gib, 2)
	young2 := writeFile(t, dir, "c", "young2.mkv", gib, 1)

	svc.Enforce(ctx)
	if !gone(old) {
		t.Error("the 10-day-old item should go")
	}
	if gone(young1) || gone(young2) {
		t.Fatal("items deleted within 72 h must survive the cap")
	}
	st := svc.Stats(ctx)
	if st.OverCapBytes != int64(gib) || st.ProtectedBytes != 2*int64(gib) || st.ProtectedUntil == 0 {
		t.Errorf("stats = %+v, want 1 GiB over, 2 GiB protected, a protected-until time", st)
	}

	// Four days on, the older of the two is fair game; the newest still isn't.
	svc.now = func() time.Time { return time.Now().Add(4 * 24 * time.Hour) }
	svc.Enforce(ctx)
	if !gone(young1) || gone(young2) {
		t.Errorf("after the hold: young1 gone=%v (want true), young2 gone=%v (want false)", gone(young1), gone(young2))
	}
}

// Retention still deletes on schedule; the hold only limits the size cap.
func TestEnforceRetentionStillApplies(t *testing.T) {
	svc, set, dir := newTestSvc(t)
	ctx := context.Background()
	_ = set.Set(ctx, keyMaxGB, "0")
	_ = set.Set(ctx, keyRetention, "7")
	old := writeFile(t, dir, "a", "old.mkv", 100, 30)
	older := writeFile(t, dir, "b", "older.mkv", 100, 40)
	svc.Enforce(ctx)
	if !gone(old) || !gone(older) {
		t.Error("retention must purge items past the window, the newest included")
	}
}

// Each item says when retention deletes it for good; with retention off it never does.
func TestItemsExpiresAt(t *testing.T) {
	svc, set, binDir := newTestSvc(t)
	ctx := context.Background()
	_ = set.Set(ctx, keyRetention, "30")
	orig := filepath.Join(t.TempDir(), "M (2024)", "m.mkv")
	if err := os.MkdirAll(filepath.Dir(orig), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(orig, []byte("x"), 0o644)
	if _, err := library.RecycleFile(binDir, orig); err != nil {
		t.Fatal(err)
	}
	it := svc.List(ctx)[0]
	if want := time.Unix(it.DeletedUnix, 0).AddDate(0, 0, 30).Unix(); it.ExpiresAt != want {
		t.Errorf("expires_at = %d, want %d", it.ExpiresAt, want)
	}
	_ = set.Set(ctx, keyRetention, "0")
	if it := svc.List(ctx)[0]; it.ExpiresAt != 0 {
		t.Errorf("retention off: expires_at = %d, want 0", it.ExpiresAt)
	}
}

// With the disk nearly full the hold gives way, oldest first, and only as far as needed.
func TestEnforceLowDiskLiftsProtection(t *testing.T) {
	svc, set, dir := newTestSvc(t)
	ctx := context.Background()
	_ = set.Set(ctx, keyMaxGB, "1")
	_ = set.Set(ctx, keyRetention, "0")
	gib := 1 << 30
	young1 := writeFile(t, dir, "a", "young1.mkv", gib, 2)
	young2 := writeFile(t, dir, "b", "young2.mkv", gib, 1)

	svc.free = func(string) (diskspace.Usage, bool) { return diskspace.Usage{UsedPct: 90}, true }
	svc.Enforce(ctx)
	if gone(young1) || gone(young2) {
		t.Fatal("10% free is not low: the hold stands")
	}

	svc.free = func(string) (diskspace.Usage, bool) { return diskspace.Usage{UsedPct: 97}, true }
	svc.Enforce(ctx)
	if !gone(young1) {
		t.Error("with 3% free the oldest protected item should be purged")
	}
	if gone(young2) {
		t.Error("only as much as needed: the bin is under the cap after one")
	}
}

// Mode is what every delete dialog asks before wording its warning. Off must say so
// (deletes are permanent); on must name the bin. It must not depend on the bin's
// contents — a bin that doesn't even exist yet still reports where files will go.
func TestRecycleModeOffAndOn(t *testing.T) {
	_, set, _ := newTestSvc(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	off := New("", set, log).Mode(ctx)
	if off.Enabled || len(off.Dirs) != 0 {
		t.Fatalf("bin off: mode = %+v, want disabled with no dirs", off)
	}
	if off.Dirs == nil {
		t.Error("dirs must be an empty list, not null, so the UI can iterate it")
	}

	missing := filepath.Join(t.TempDir(), "not-created-yet")
	_ = set.Set(ctx, keyMaxGB, "75")
	_ = set.Set(ctx, keyRetention, "14")
	on := New(missing, set, log).Mode(ctx)
	if !on.Enabled || len(on.Dirs) != 1 || on.Dirs[0] != missing {
		t.Fatalf("bin on: mode = %+v, want enabled at %s", on, missing)
	}
	if on.MaxGB != 75 || on.RetentionDays != 14 {
		t.Errorf("guard rails = %d GB / %d days, want 75 / 14", on.MaxGB, on.RetentionDays)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("Mode must not create or touch the bin")
	}
}
