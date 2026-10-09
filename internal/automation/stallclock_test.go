package automation

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/store"
)

// newQbitCoord is a store-backed coordinator whose one download client is qb.
func newQbitCoord(t *testing.T, qb *fakeQbit) *Coordinator {
	t.Helper()
	c := stallCoord(t)
	srv := httptest.NewServer(qb.handler())
	t.Cleanup(srv.Close)
	c.downloads = download.NewService(c.db, c.log)
	if _, err := c.downloads.Create(context.Background(), download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: srv.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return c
}

// stallCoord is a coordinator over a fresh store holding a grab row for each id, for the
// tests of the stall clock (which lives on the grab row).
func stallCoord(t *testing.T, ids ...int64) *Coordinator {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c := &Coordinator{db: st.DB(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, id := range ids {
		addStallGrab(t, c, id)
	}
	return c
}

func addStallGrab(t *testing.T, c *Coordinator, id int64) {
	t.Helper()
	if _, err := c.db.Exec(`INSERT INTO grabs (id, movie_id, title, media_type) VALUES (?, 1, 'Some.Film.2001.1080p', 'movie')`, id); err != nil {
		t.Fatal(err)
	}
}

// expireStall backdates grab id's stall clock by ago, at the given progress — what the
// two-minute check sees once that much time has really passed without progress.
func expireStall(t *testing.T, c *Coordinator, id int64, progress float64, ago time.Duration) {
	t.Helper()
	if _, err := c.db.Exec(`UPDATE grabs SET progress = ?, progress_at = ? WHERE id = ?`,
		progress, time.Now().Add(-ago).UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
}

// stallClockAt is when grab id's stall clock last restarted (zero: never observed).
func stallClockAt(t *testing.T, c *Coordinator, id int64) time.Time {
	t.Helper()
	var at int64
	if err := c.db.QueryRow(`SELECT progress_at FROM grabs WHERE id = ?`, id).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return msTime(at)
}

// A restart doesn't reset the stall clock: a torrent idle for five hours before it is
// failed over an hour after, on a six-hour window — not six hours after.
func TestStallClockSurvivesRestart(t *testing.T) {
	c := stallCoord(t, 1)
	ctx := context.Background()
	const window = 6 * time.Hour
	if c.noProgressFor(ctx, 1, 0.3, window) {
		t.Fatal("the first observation must never be a stall")
	}
	expireStall(t, c, 1, 0.3, 5*time.Hour)

	// A new coordinator over the same database: what a restart is.
	restarted := &Coordinator{db: c.db, log: c.log}
	if restarted.noProgressFor(ctx, 1, 0.3, window) {
		t.Fatal("5h idle on a 6h window is not a stall yet")
	}
	future := time.Now().Add(61 * time.Minute)
	restarted.now = func() time.Time { return future }
	if !restarted.noProgressFor(ctx, 1, 0.3, window) {
		t.Error("an hour after the restart, the 6h window has run out — the clock was reset by the restart")
	}
	// Forward progress restarts it.
	if restarted.noProgressFor(ctx, 1, 0.31, window) {
		t.Error("forward progress must restart the clock")
	}
}

// "Still waiting" (no replacement found) holds the next attempt off a window, across a
// restart too.
func TestStillWaitingPersists(t *testing.T) {
	c := stallCoord(t, 1)
	ctx := context.Background()
	if c.waitingOut(ctx, 1, time.Hour) {
		t.Fatal("a grab that never waited is not waiting")
	}
	c.markStillWaiting(ctx, 1)
	restarted := &Coordinator{db: c.db, log: c.log}
	if !restarted.waitingOut(ctx, 1, time.Hour) {
		t.Error("the wait was lost across a restart")
	}
	later := time.Now().Add(2 * time.Hour)
	restarted.now = func() time.Time { return later }
	if restarted.waitingOut(ctx, 1, time.Hour) {
		t.Error("a window later it should try again")
	}
}

// The reconciler moves a grab's phase as its torrent goes queued → downloading → complete
// → missing, matched by info hash even when the torrent's name is nothing like the
// listing title, and writes nothing when nothing changed.
func TestReconcileAcquisitions(t *testing.T) {
	qb := &fakeQbit{}
	h := newQbitCoord(t, qb)
	ctx := context.Background()
	const hash = "abcdef0123456789abcdef0123456789abcdef01"
	if _, err := h.db.Exec(`INSERT INTO grabs (id, movie_id, version_id, title, media_type, info_hash)
		VALUES (1, 7, 3, 'Pokemon Heroes (2002) 1080p BDRip x265 10bit AC3 5 1 DUAL - Goki', 'movie', ?)`, strings.ToUpper(hash)); err != nil {
		t.Fatal(err)
	}
	set := func(state string, progress float64, left int64) {
		qb.mu.Lock()
		qb.torrents = []map[string]any{{
			"hash": hash, "name": "Pokemon.Heroes.2002.1080p.BDRip.x265-Goki", "state": state,
			"progress": progress, "size": 1000, "amount_left": left, "category": "",
		}}
		qb.mu.Unlock()
		h.downloads.InvalidateSnapshot()
	}
	acq := func() Acquisition {
		t.Helper()
		got, ok, err := h.ByHash(ctx, hash)
		if err != nil || !ok {
			t.Fatalf("ByHash: ok=%v err=%v", ok, err)
		}
		return got
	}
	reconcile := func() int {
		t.Helper()
		n, err := h.ReconcileAcquisitions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	set("queuedDL", 0, 1000)
	if n := reconcile(); n != 1 {
		t.Fatalf("first sight wrote %d rows, want 1", n)
	}
	a := acq()
	if a.Phase != "queued" || a.Scope != "v3" || a.ClientID == 0 || a.LastSeenAt.IsZero() || a.ProgressAt.IsZero() {
		t.Fatalf("queued: %+v", a)
	}
	if n := reconcile(); n != 0 {
		t.Errorf("nothing changed, but %d rows were written", n)
	}
	set("downloading", 0.004, 996) // under a percent: not worth a write
	if n := reconcile(); n != 1 || acq().Phase != "downloading" {
		t.Errorf("phase change: wrote %d, phase %q", n, acq().Phase)
	}
	set("downloading", 0.005, 995)
	if n := reconcile(); n != 0 {
		t.Errorf("a sub-percent move wrote %d rows", n)
	}
	set("downloading", 0.5, 500)
	if n := reconcile(); n != 1 || acq().Progress != 0.5 {
		t.Errorf("a big move: wrote %d, progress %v", n, acq().Progress)
	}
	set("uploading", 1, 0)
	reconcile()
	if a := acq(); a.Phase != "complete" || a.CompletedAt.IsZero() {
		t.Errorf("finished: %+v", a)
	}

	// Gone from a complete read of the client: missing — and still in flight for a while.
	set("uploading", 1, 0)
	qb.mu.Lock()
	qb.torrents = nil
	qb.mu.Unlock()
	h.downloads.InvalidateSnapshot()
	reconcile()
	if a := acq(); a.Phase != "missing" {
		t.Errorf("vanished: phase %q", a.Phase)
	}
	if act, _ := h.ActiveForVersion(ctx, 7, 3); len(act) != 1 {
		t.Error("a torrent gone moments ago must still count as in flight")
	}
	later := time.Now().Add(missingGrace + time.Minute)
	h.now = func() time.Time { return later }
	if act, _ := h.ActiveForVersion(ctx, 7, 3); len(act) != 0 {
		t.Error("a torrent gone for longer than the grace must stop holding its version")
	}
}

// A hashless grab whose torrent is in the client gets the hash adopted by the reconciler.
func TestReconcileAdoptsLegacyHashes(t *testing.T) {
	qb := &fakeQbit{}
	h := newQbitCoord(t, qb)
	ctx := context.Background()
	if _, err := h.db.Exec(`INSERT INTO grabs (id, movie_id, title, media_type, info_hash)
		VALUES (1, 7, 'Pokemon Heroes (2002) 1080p BDRip x265 10bit AC3 5 1 DUAL - Goki', 'movie', '')`); err != nil {
		t.Fatal(err)
	}
	qb.torrents = []map[string]any{{"hash": "abc123", "name": "Pokemon Heroes (2002) 1080p BDRip x265 10bit AC3 5.1 - Goki", "state": "downloading", "progress": 0.2, "size": 10, "amount_left": 8}}
	if _, err := h.ReconcileAcquisitions(ctx); err != nil {
		t.Fatal(err)
	}
	a, ok, _ := h.ByHash(ctx, "abc123")
	if !ok || a.Phase != "downloading" {
		t.Fatalf("legacy grab: ok=%v %+v", ok, a)
	}
}
