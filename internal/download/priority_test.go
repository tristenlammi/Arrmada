package download

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// memFlags is an in-memory stand-in for the shared settings service.
type memFlags struct {
	mu sync.Mutex
	m  map[string]string
}

func (f *memFlags) Get(_ context.Context, key, def string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.m[key]; ok {
		return v
	}
	return def
}

func (f *memFlags) Set(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]string{}
	}
	f.m[key] = value
	return nil
}

func newTestService(t *testing.T) (*Service, *memFlags) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := NewService(st.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	flags := &memFlags{}
	svc.SetFlags(flags)
	return svc, flags
}

// ListEnabled is the order new downloads try: the lowest priority number, then the oldest.
func TestListEnabledOrdersByPriority(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	for _, c := range []Client{
		{Name: "a", Kind: KindQbittorrent, URL: "http://a", Enabled: true}, // the default, 25
		{Name: "b", Kind: KindQbittorrent, URL: "http://b", Enabled: true, Priority: 5},
		{Name: "c", Kind: KindQbittorrent, URL: "http://c", Enabled: false, Priority: 1}, // off
		{Name: "d", Kind: KindQbittorrent, URL: "http://d", Enabled: true, Priority: 25}, // ties with a: older first
		{Name: "e", Kind: KindQbittorrent, URL: "http://e", Enabled: true, Priority: 50},
	} {
		if _, err := r.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.ListEnabled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names string
	for _, c := range got {
		names += c.Name
	}
	if names != "bade" {
		t.Errorf("order = %q, want bade", names)
	}
	// An edit that leaves priority at 0 keeps it.
	if err := r.Update(ctx, Client{ID: got[0].ID, Name: "b", URL: "http://b", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if c, _ := r.Get(ctx, got[0].ID); c.Priority != 5 {
		t.Errorf("priority after an edit without one = %d, want 5", c.Priority)
	}
}

// Startup marks an existing row with the bundled URL instead of adding one, and once a row
// is marked it does nothing more — whether that row is switched off or its URL edited.
// A deleted bundled client stays deleted until restored.
func TestEnsureBundled(t *testing.T) {
	svc, flags := newTestService(t)
	ctx := context.Background()
	const url = "http://arrmada-qbittorrent:8080"

	// An install from before the flag: its row is recognised by URL and marked.
	old, err := svc.Create(ctx, Client{Name: "qBittorrent (bundled)", Kind: KindQbittorrent, URL: url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureBundled(ctx, url); err != nil {
		t.Fatal(err)
	}
	all, _ := svc.List(ctx)
	if len(all) != 1 || !all[0].Bundled || all[0].ID != old.ID {
		t.Fatalf("after the first start: %+v", all)
	}

	// Switched off and its URL edited: still the bundled one, and nothing is re-added.
	if _, err := svc.Update(ctx, Client{ID: old.ID, Name: "qb", URL: "http://elsewhere:8080", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureBundled(ctx, url); err != nil {
		t.Fatal(err)
	}
	all, _ = svc.List(ctx)
	if len(all) != 1 || all[0].Enabled || !all[0].Bundled {
		t.Fatalf("a disabled, edited bundled client came back as another row: %+v", all)
	}

	// Its tuning is skipped while it's switched off.
	if err := svc.SetBundledPort(ctx, 6881); err != ErrBundledInactive {
		t.Errorf("SetBundledPort on a disabled bundled client = %v, want ErrBundledInactive", err)
	}
	if err := svc.SetBundledSavePath(ctx, "/downloads"); err != ErrBundledInactive {
		t.Errorf("SetBundledSavePath = %v, want ErrBundledInactive", err)
	}
	if err := svc.EnsureBundledQueue(ctx); err != ErrBundledInactive {
		t.Errorf("EnsureBundledQueue = %v, want ErrBundledInactive", err)
	}

	// Deleted: the flag is set and a restart adds nothing.
	if err := svc.Delete(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	if flags.Get(ctx, KeyBundledRemoved, "") != "1" {
		t.Error("deleting the bundled client didn't record it")
	}
	if err := svc.EnsureBundled(ctx, url); err != nil {
		t.Fatal(err)
	}
	if all, _ = svc.List(ctx); len(all) != 0 {
		t.Fatalf("a deleted bundled client came back: %+v", all)
	}
	if ok, _ := svc.HasBundled(ctx); ok {
		t.Error("HasBundled after delete")
	}

	// Restore brings it back, flagged and on.
	if err := svc.RestoreBundled(ctx, url); err != nil {
		t.Fatal(err)
	}
	all, _ = svc.List(ctx)
	if len(all) != 1 || !all[0].Bundled || !all[0].Enabled || all[0].URL != url {
		t.Fatalf("after restore: %+v", all)
	}
	if flags.Get(ctx, KeyBundledRemoved, "") == "1" {
		t.Error("restore left the removed flag set")
	}

	// Deleting a client that isn't the bundled one sets nothing.
	other, _ := svc.Create(ctx, Client{Name: "seedbox", Kind: KindQbittorrent, URL: "http://seedbox", Enabled: true})
	_ = flags.Set(ctx, KeyBundledRemoved, "")
	if err := svc.Delete(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if flags.Get(ctx, KeyBundledRemoved, "") != "" {
		t.Error("deleting a plain client marked the bundled one removed")
	}
}

// fakeAddQbit is a qBittorrent stub that counts adds and answers them with addBody.
func fakeAddQbit(t *testing.T, adds *int32, addBody string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "Ok.") })
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(adds, 1)
		fmt.Fprint(w, addBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// Adds go to the lowest priority number. An unreachable first client hands the add to the
// next; a first client that answers with a rejection does not — it may have the torrent.
func TestAddFailsOverOnlyWhenUnreachable(t *testing.T) {
	ctx := context.Background()
	req := AddRequest{Name: "Film.2020.1080p", URL: "magnet:?xt=urn:btih:abc", Category: DefaultMovieCategory}

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close() // nothing listens there now

	t.Run("goes to the first by priority", func(t *testing.T) {
		svc, _ := newTestService(t)
		var first, second int32
		s1, s2 := fakeAddQbit(t, &first, "Ok."), fakeAddQbit(t, &second, "Ok.")
		// Created in the opposite order, so id order would pick the wrong one.
		_, _ = svc.Create(ctx, Client{Name: "second", Kind: KindQbittorrent, URL: s2.URL, Enabled: true, Priority: 20})
		_, _ = svc.Create(ctx, Client{Name: "first", Kind: KindQbittorrent, URL: s1.URL, Enabled: true, Priority: 10})
		if err := svc.Add(ctx, req); err != nil {
			t.Fatal(err)
		}
		if first != 1 || second != 0 {
			t.Errorf("adds = first %d, second %d", first, second)
		}
	})

	t.Run("unreachable first fails over", func(t *testing.T) {
		svc, _ := newTestService(t)
		var second int32
		s2 := fakeAddQbit(t, &second, "Ok.")
		_, _ = svc.Create(ctx, Client{Name: "dead", Kind: KindQbittorrent, URL: deadURL, Enabled: true, Priority: 1})
		_, _ = svc.Create(ctx, Client{Name: "alive", Kind: KindQbittorrent, URL: s2.URL, Enabled: true, Priority: 2})
		if err := svc.Add(ctx, req); err != nil {
			t.Fatalf("add with the first client down: %v", err)
		}
		if second != 1 {
			t.Errorf("second client adds = %d, want 1", second)
		}
	})

	t.Run("a rejection is not failed over", func(t *testing.T) {
		svc, _ := newTestService(t)
		var first, second int32
		s1, s2 := fakeAddQbit(t, &first, "Fails."), fakeAddQbit(t, &second, "Ok.")
		_, _ = svc.Create(ctx, Client{Name: "rejects", Kind: KindQbittorrent, URL: s1.URL, Enabled: true, Priority: 1})
		_, _ = svc.Create(ctx, Client{Name: "other", Kind: KindQbittorrent, URL: s2.URL, Enabled: true, Priority: 2})
		if err := svc.Add(ctx, req); err == nil {
			t.Fatal("a rejected add reported success")
		}
		if first != 1 || second != 0 {
			t.Errorf("adds = first %d, second %d; a rejection must not be retried elsewhere", first, second)
		}
	})

	t.Run("every client unreachable is an error", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, _ = svc.Create(ctx, Client{Name: "dead", Kind: KindQbittorrent, URL: deadURL, Enabled: true})
		err := svc.Add(ctx, req)
		if err == nil || !NeverReached(err) {
			t.Errorf("err = %v, want an unreachable error", err)
		}
	})
}

// An HTTP error on the add itself (the client answered) is not "never reached".
func TestNeverReachedOnlyForConnectFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			fmt.Fprint(w, "Ok.")
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	q := NewQBittorrent()
	err := q.Add(context.Background(), Client{ID: 1, URL: srv.URL}, AddRequest{URL: "magnet:?xt=urn:btih:abc"})
	if err == nil || NeverReached(err) {
		t.Errorf("HTTP 500 add: err = %v, NeverReached = %v", err, NeverReached(err))
	}
}
