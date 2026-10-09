package download

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// countingClient is a fake download client that counts List calls, can hold a List open
// until released, and can be switched to failing.
type countingClient struct {
	mu    sync.Mutex
	items []Item
	fail  map[string]error // by client URL
	gate  chan struct{}    // non-nil: List blocks until it's closed
	lists atomic.Int32
}

func (f *countingClient) List(_ context.Context, dc Client) ([]Item, error) {
	f.lists.Add(1)
	f.mu.Lock()
	gate := f.gate
	err := f.fail[dc.URL]
	items := append([]Item(nil), f.items...)
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if err != nil {
		return nil, err
	}
	return items, nil
}
func (f *countingClient) setFail(url string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail == nil {
		f.fail = map[string]error{}
	}
	f.fail[url] = err
}
func (f *countingClient) Pause(context.Context, Client, string) error                 { return nil }
func (f *countingClient) Resume(context.Context, Client, string) error                { return nil }
func (f *countingClient) Add(context.Context, Client, AddRequest) error               { return nil }
func (f *countingClient) Remove(context.Context, Client, string, bool) error          { return nil }
func (f *countingClient) TorrentAction(context.Context, Client, string, string) error { return nil }
func (f *countingClient) Test(context.Context, Client) error                          { return nil }

func snapshotFixture(t *testing.T, urls ...string) (*Service, *countingClient, *time.Time) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := NewService(st.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	fake := &countingClient{items: []Item{{Hash: "aaa", Name: "One", Progress: 0.5}}}
	svc.registry.impls[KindQbittorrent] = fake
	now := time.Date(2026, 10, 9, 14, 2, 0, 0, time.UTC)
	svc.snap.now = func() time.Time { return now }
	for i, u := range urls {
		if _, err := svc.repo.Create(context.Background(), Client{Name: "qb" + string(rune('a'+i)), Kind: KindQbittorrent, URL: u, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	return svc, fake, &now
}

// Ten callers at once share one read of the client.
func TestSnapshotSingleFlight(t *testing.T) {
	svc, fake, _ := snapshotFixture(t, "http://qb")
	fake.gate = make(chan struct{})
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make([]Snapshot, 10)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = svc.Snapshot(ctx)
		}(i)
	}
	// Let every caller reach the shared read before it answers.
	deadline := time.Now().Add(2 * time.Second)
	for fake.lists.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(fake.gate)
	wg.Wait()

	if n := fake.lists.Load(); n != 1 {
		t.Fatalf("10 concurrent callers made %d List calls, want 1", n)
	}
	for i, r := range results {
		if len(r.Items) != 1 || !r.Complete || r.Items[0].ClientID == 0 {
			t.Fatalf("caller %d got %+v", i, r)
		}
	}
	// Each caller gets its own slice: changing one can't change another's.
	results[0].Items[0].Name = "changed"
	if results[1].Items[0].Name != "One" {
		t.Error("callers share one Items slice")
	}
}

// A call within two seconds is served from the cache; after that the client is asked again.
func TestSnapshotTTL(t *testing.T) {
	svc, fake, now := snapshotFixture(t, "http://qb")
	ctx := context.Background()
	for range 3 {
		if _, err := svc.Snapshot(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := fake.lists.Load(); n != 1 {
		t.Fatalf("3 calls inside the TTL made %d List calls, want 1", n)
	}
	*now = now.Add(snapshotTTL)
	if _, err := svc.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if n := fake.lists.Load(); n != 2 {
		t.Fatalf("a call after the TTL made %d List calls in total, want 2", n)
	}
}

// Pausing (or anything else that changes a torrent) shows on the very next read.
func TestSnapshotInvalidatedByActions(t *testing.T) {
	svc, fake, _ := snapshotFixture(t, "http://qb")
	ctx := context.Background()
	if _, err := svc.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	actions := map[string]func() error{
		"pause":  func() error { return svc.Pause(ctx, "aaa") },
		"resume": func() error { return svc.Resume(ctx, "aaa") },
		"action": func() error { return svc.Action(ctx, "aaa", "recheck") },
		"remove": func() error { return svc.Remove(ctx, "0123456789abcdef0123456789abcdef01234567", false) },
		"add":    func() error { return svc.Add(ctx, AddRequest{Name: "x", URL: "magnet:?"}) },
	}
	for name, act := range actions {
		before := fake.lists.Load()
		if err := act(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := svc.Snapshot(ctx); err != nil {
			t.Fatal(err)
		}
		if fake.lists.Load() != before+1 {
			t.Errorf("%s didn't invalidate the cached queue", name)
		}
	}
}

// A failing client is unreachable with its last good read kept, and its outage start is
// the first failure — not pushed forward by every later one.
func TestSnapshotClientHealth(t *testing.T) {
	svc, fake, now := snapshotFixture(t, "http://up", "http://down")
	ctx := context.Background()

	snap, err := svc.Snapshot(ctx)
	if err != nil || !snap.Complete || len(snap.Health) != 2 {
		t.Fatalf("healthy read: %+v, %v", snap, err)
	}
	firstOK := *now

	*now = now.Add(time.Minute)
	fake.setFail("http://down", errors.New("connection refused"))
	svc.InvalidateSnapshot()
	snap, err = svc.Snapshot(ctx)
	if err != nil {
		t.Fatalf("one client up is a partial read, not an error: %v", err)
	}
	if snap.Complete {
		t.Error("a read missing one enabled client must not be complete")
	}
	down := healthOf(snap, "http://down", svc)
	if down.Reachable || down.LastErr == "" || !down.LastOK.Equal(firstOK) || !down.Since.Equal(*now) {
		t.Fatalf("down client after first failure: %+v", down)
	}
	outageStart := *now

	*now = now.Add(time.Minute)
	svc.InvalidateSnapshot()
	snap, _ = svc.Snapshot(ctx)
	down = healthOf(snap, "http://down", svc)
	if !down.Since.Equal(outageStart) || !down.LastOK.Equal(firstOK) {
		t.Fatalf("a later failure moved the outage start or lost LastOK: %+v", down)
	}
	if up := healthOf(snap, "http://up", svc); !up.Reachable || !up.Since.IsZero() {
		t.Errorf("up client: %+v", up)
	}

	// Every client down: an outage, not an empty queue.
	fake.setFail("http://up", errors.New("timeout"))
	svc.InvalidateSnapshot()
	if _, err := svc.Snapshot(ctx); err == nil {
		t.Fatal("every client failing must be an error")
	}

	// Back up: reachable again, outage cleared.
	fake.setFail("http://up", nil)
	fake.setFail("http://down", nil)
	*now = now.Add(time.Minute)
	svc.InvalidateSnapshot()
	snap, err = svc.Snapshot(ctx)
	if err != nil || !snap.Complete {
		t.Fatalf("recovered read: %+v, %v", snap, err)
	}
	if down := healthOf(snap, "http://down", svc); !down.Reachable || !down.Since.IsZero() || !down.LastOK.Equal(*now) {
		t.Errorf("recovered client: %+v", down)
	}
}

// No client at all is an empty, complete queue — nothing is down.
func TestSnapshotNoClients(t *testing.T) {
	svc, _, _ := snapshotFixture(t)
	snap, err := svc.Snapshot(context.Background())
	if err != nil || !snap.Complete || len(snap.Items) != 0 || len(snap.Health) != 0 {
		t.Fatalf("no clients: %+v, %v", snap, err)
	}
}

func healthOf(snap Snapshot, url string, svc *Service) ClientHealth {
	clients, _ := svc.repo.List(context.Background())
	for _, c := range clients {
		if c.URL != url {
			continue
		}
		for _, h := range snap.Health {
			if h.ID == c.ID {
				return h
			}
		}
	}
	return ClientHealth{}
}
