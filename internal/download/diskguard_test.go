package download

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"testing"

	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// fakeClient records what the guard asked it to do.
type fakeClient struct {
	items   []Item
	paused  []string
	resumed []string
}

func (f *fakeClient) List(context.Context, Client) ([]Item, error) { return f.items, nil }
func (f *fakeClient) Pause(_ context.Context, _ Client, hash string) error {
	f.paused = append(f.paused, hash)
	for i := range f.items {
		if f.items[i].Hash == hash {
			f.items[i].State = "paused"
		}
	}
	return nil
}
func (f *fakeClient) Resume(_ context.Context, _ Client, hash string) error {
	f.resumed = append(f.resumed, hash)
	return nil
}
func (f *fakeClient) Add(context.Context, Client, AddRequest) error               { return nil }
func (f *fakeClient) Remove(context.Context, Client, string, bool) error          { return nil }
func (f *fakeClient) TorrentAction(context.Context, Client, string, string) error { return nil }
func (f *fakeClient) Test(context.Context, Client) error                          { return nil }

func guardFixture(t *testing.T, items []Item) (*DiskGuard, *fakeClient, *settings.Service, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewService(st.DB(), log)
	fake := &fakeClient{items: items}
	svc.registry.impls[KindQbittorrent] = fake

	ctx := context.Background()
	if _, err := svc.repo.Create(ctx, Client{
		Name: "test", Kind: KindQbittorrent, URL: "http://x", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	set := settings.NewService(st.DB())
	return NewDiskGuard(svc, set, log, t.TempDir()), fake, set, ctx
}

// The rule that matters most: the guard resumes only what IT paused. A torrent the
// user paused by hand must still be paused after the disk drains.
func TestGuardResumesOnlyWhatItPaused(t *testing.T) {
	g, fake, _, ctx := guardFixture(t, []Item{
		{Hash: "aaa", Name: "Active", State: "downloading"},
		{Hash: "bbb", Name: "User paused this", State: "paused"},
		{Hash: "ccc", Name: "Seeding", State: "seeding"},
	})

	g.pauseActive(ctx, nil, 91, 85)
	if len(fake.paused) != 1 || fake.paused[0] != "aaa" {
		t.Fatalf("paused %v, want only the active download", fake.paused)
	}

	g.resume(ctx, g.held(ctx))
	if len(fake.resumed) != 1 || fake.resumed[0] != "aaa" {
		t.Fatalf("resumed %v, want only the torrent the guard paused", fake.resumed)
	}
	if len(g.held(ctx)) != 0 {
		t.Error("the held set survived a resume — the guard would never release it")
	}
}

// Seeding torrents write nothing, and pausing them would stall seed goals and put the
// hit-and-run clocks at risk over a problem they aren't causing.
func TestGuardLeavesSeedingTorrentsAlone(t *testing.T) {
	g, fake, _, ctx := guardFixture(t, []Item{
		{Hash: "seed1", State: "seeding"},
		{Hash: "seed2", State: "seeding"},
	})
	g.pauseActive(ctx, nil, 99, 85)
	if len(fake.paused) != 0 {
		t.Errorf("paused %v, want nothing — seeding doesn't fill the disk", fake.paused)
	}
}

// A second pass while still over the line must not re-pause what's already held, or
// the held set would grow a duplicate on every tick.
func TestGuardDoesNotRePauseWhatItHolds(t *testing.T) {
	g, fake, _, ctx := guardFixture(t, []Item{{Hash: "aaa", State: "downloading"}})

	g.pauseActive(ctx, nil, 91, 85)
	g.pauseActive(ctx, g.held(ctx), 91, 85)

	if len(fake.paused) != 1 {
		t.Errorf("paused %v, want one call — the second pass should be a no-op", fake.paused)
	}
	if held := g.held(ctx); len(held) != 1 {
		t.Errorf("held = %v, want one entry", held)
	}
}

// Turning the guard off while it holds torrents must release them. Leaving them
// paused would look exactly like the guard still being on.
func TestDisablingTheGuardReleasesItsHold(t *testing.T) {
	g, fake, set, ctx := guardFixture(t, []Item{{Hash: "aaa", State: "downloading"}})
	g.pauseActive(ctx, nil, 91, 85)

	if err := set.SetBool(ctx, KeyDiskGuard, false); err != nil {
		t.Fatal(err)
	}
	if err := g.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.resumed) != 1 {
		t.Errorf("resumed %v, want the held torrent released when the guard was turned off", fake.resumed)
	}
}

// A resume point at or above the pause point would pause and resume on alternate
// passes forever. The guard must correct that rather than obey it.
func TestThresholdsRefuseToFlap(t *testing.T) {
	g, _, set, ctx := guardFixture(t, nil)

	if pause, resume := g.thresholds(ctx); pause != DefaultDiskGuardPause || resume != DefaultDiskGuardResum {
		t.Errorf("defaults = %d/%d, want %d/%d", pause, resume, DefaultDiskGuardPause, DefaultDiskGuardResum)
	}

	for _, tc := range []struct {
		pause, resume         string
		wantPause, wantResume int
	}{
		{"85", "90", 85, 84}, // resume above pause
		{"85", "85", 85, 84}, // equal
		{"0", "0", 1, 0},     // nonsense low
		{"250", "10", 99, 10},
		{"abc", "xyz", DefaultDiskGuardPause, DefaultDiskGuardResum}, // unparseable
	} {
		_ = set.Set(ctx, KeyDiskGuardPause, tc.pause)
		_ = set.Set(ctx, KeyDiskGuardResum, tc.resume)
		p, r := g.thresholds(ctx)
		if p != tc.wantPause || r != tc.wantResume {
			t.Errorf("thresholds(%s/%s) = %d/%d, want %d/%d",
				tc.pause, tc.resume, p, r, tc.wantPause, tc.wantResume)
		}
		if r >= p {
			t.Errorf("resume %d is not below pause %d — this flaps", r, p)
		}
	}
}

// The held set is persisted so a restart mid-hold doesn't strand the queue.
func TestHeldSetSurvivesAReload(t *testing.T) {
	g, _, set, ctx := guardFixture(t, []Item{
		{Hash: "bbb", State: "downloading"},
		{Hash: "aaa", State: "downloading"},
	})
	g.pauseActive(ctx, nil, 91, 85)

	fresh := NewDiskGuard(g.svc, set, g.log, g.dir)
	got := fresh.held(ctx)
	sort.Strings(got)
	if len(got) != 2 || got[0] != "aaa" || got[1] != "bbb" {
		t.Errorf("a new guard read back %v, want both hashes", got)
	}
}

// usageAt makes the guard read the volume as pct full, without filling a real disk.
func usageAt(g *DiskGuard, pct float64) {
	g.usage = func(string) (diskspace.Usage, bool) { return diskspace.Usage{UsedPct: pct}, true }
}

// A torrent the guard paused and someone resumed (Resume all, or by hand in the client)
// is paused again on the next pass while the volume is still over the line. Skipping
// held hashes is how the cache pool filled with the guard switched on.
func TestGuardRePausesAHeldTorrentResumedBehindItsBack(t *testing.T) {
	g, fake, _, ctx := guardFixture(t, []Item{{Hash: "AAA", State: "downloading"}})
	usageAt(g, 91)
	if err := g.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.paused) != 1 {
		t.Fatalf("first pass paused %v, want the active download", fake.paused)
	}

	fake.items[0].State = "downloading" // resumed outside the guard
	if err := g.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.paused) != 2 || fake.paused[1] != "AAA" {
		t.Fatalf("paused %v, want the resumed torrent paused again", fake.paused)
	}
	if held := g.held(ctx); len(held) != 1 || held[0] != "aaa" {
		t.Errorf("held = %v, want one lowercased entry, no duplicate", held)
	}
}

// A held torrent deleted from the client is dropped from the held set once a complete
// queue proves it's gone, so "holding N" stays true.
func TestGuardPrunesHeldTorrentsThatAreGone(t *testing.T) {
	g, fake, _, ctx := guardFixture(t, []Item{
		{Hash: "aaa", State: "downloading"},
		{Hash: "bbb", State: "downloading"},
	})
	usageAt(g, 91)
	_ = g.Check(ctx)
	if len(g.held(ctx)) != 2 {
		t.Fatalf("held = %v, want both", g.held(ctx))
	}

	fake.items = fake.items[:1] // bbb deleted while held
	usageAt(g, 83)              // between the lines: still holding, nothing to pause
	_ = g.Check(ctx)
	if len(g.held(ctx)) != 2 {
		t.Fatalf("held = %v; one missed pass shouldn't forget a torrent", g.held(ctx))
	}
	_ = g.Check(ctx)
	if held := g.held(ctx); len(held) != 1 || held[0] != "aaa" {
		t.Errorf("held = %v, want only the torrent the client still has", held)
	}
}

// With a client unreachable its torrents are merely missing from the list, not gone —
// forgetting them would leave them paused for good.
func TestGuardKeepsHeldTorrentsWhenTheQueueIsPartial(t *testing.T) {
	held := []string{"aaa", "bbb"}
	if got, _ := prunedHeld(held, []Item{{Hash: "aaa"}}, false, nil); len(got) != 2 {
		t.Errorf("partial queue pruned %v → %v; nothing should be dropped", held, got)
	}
	got, missed := prunedHeld(held, []Item{{Hash: "AAA"}}, true, nil)
	if len(got) != 2 {
		t.Errorf("first miss pruned %v → %v; a hash must be missing twice in a row", held, got)
	}
	if got, _ = prunedHeld(held, []Item{{Hash: "AAA"}}, true, missed); len(got) != 1 || got[0] != "aaa" {
		t.Errorf("second miss: %v, want only aaa (hashes compared case-insensitively)", got)
	}
}

// A qBittorrent that has just restarted lists nothing while it loads its torrents. An
// empty queue must not read as "every held torrent was deleted" — they'd stay paused
// for good once forgotten.
func TestGuardKeepsHeldTorrentsWhenTheQueueIsEmpty(t *testing.T) {
	held := []string{"aaa", "bbb"}
	missed := map[string]int{"aaa": 1, "bbb": 1} // already missed once
	if got, _ := prunedHeld(held, nil, true, missed); len(got) != 2 {
		t.Errorf("empty queue pruned %v → %v; nothing should be dropped", held, got)
	}
}

// Engaged is what the API uses to refuse a resume: on, measurable, above the resume
// point, and holding something.
func TestGuardEngaged(t *testing.T) {
	g, _, set, ctx := guardFixture(t, []Item{{Hash: "aaa", State: "downloading"}})
	usageAt(g, 91)
	if g.Engaged(ctx) {
		t.Error("nothing held yet — not engaged")
	}
	_ = g.Check(ctx)
	if !g.Engaged(ctx) || !g.Held(ctx)["aaa"] {
		t.Error("holding a torrent over the line should be engaged")
	}
	usageAt(g, 83)
	if !g.Engaged(ctx) {
		t.Error("between the lines and still holding should stay engaged")
	}
	usageAt(g, 70)
	if g.Engaged(ctx) {
		t.Error("below the resume point the guard is about to release — not engaged")
	}
	usageAt(g, 91)
	_ = set.SetBool(ctx, KeyDiskGuard, false)
	if g.Engaged(ctx) {
		t.Error("a disabled guard is never engaged")
	}
}
