package download

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Each enabled client is asked on its own: one up and one down are told apart (Queue only
// fails when every client does), and a disabled client isn't asked at all.
func TestClientStates(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/app/version":
			_, _ = io.WriteString(w, "v5.0.0")
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close() // nothing listens there now

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := NewService(st.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	for _, c := range []Client{
		{Name: "bundled", Kind: KindQbittorrent, URL: up.URL, Enabled: true},
		{Name: "seedbox", Kind: KindQbittorrent, URL: downURL, Enabled: true},
		{Name: "old", Kind: KindQbittorrent, URL: downURL, Enabled: false},
	} {
		if _, err := svc.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	states, err := svc.ClientStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("want the 2 enabled clients, got %+v", states)
	}
	byName := map[string]ClientState{}
	for _, s := range states {
		byName[s.Name] = s
	}
	if s := byName["bundled"]; !s.OK || s.Err != "" {
		t.Errorf("up client: %+v", s)
	}
	if s := byName["seedbox"]; s.OK || s.Err == "" {
		t.Errorf("down client: %+v", s)
	}
}

// The import sweeps read their candidates from CompletedInCategory. A torrent qBittorrent
// is still moving out of its incomplete folder must not be one of them: it reports 100%
// with its content path still in a folder Arrmada can't see, and the series import used to
// read "no folder" as "no video" and blocklist the release. Once the move is done it is
// listed again, at its final path.
func TestCompletedInCategoryWaitsForTheMove(t *testing.T) {
	var mu sync.Mutex // the handler runs on the server's goroutine
	state, path := "moving", "/incomplete/Show.S01.1080p"
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/app/version":
			_, _ = io.WriteString(w, "v5.0.0")
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[
				{"hash":"aaaa","name":"Show.S01.1080p","state":"`+state+`","progress":1,"amount_left":0,
				 "category":"arrmada-tv","content_path":"`+path+`"},
				{"hash":"bbbb","name":"Other.Show.S02.1080p","state":"stalledUP","progress":1,"amount_left":0,
				 "category":"arrmada-tv","content_path":"/completed/Other.Show.S02.1080p"}
			]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer qb.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := NewService(st.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	if _, err := svc.Create(ctx, Client{Name: "qb", Kind: KindQbittorrent, URL: qb.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	hashes := func() map[string]string {
		t.Helper()
		svc.InvalidateSnapshot() // a fresh read, not the cached one
		items, err := svc.CompletedInCategory(ctx, "arrmada-tv")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, it := range items {
			out[it.Hash] = it.ContentPath
		}
		return out
	}

	got := hashes()
	if _, ok := got["aaaa"]; ok {
		t.Errorf("a torrent mid-move was offered for import: %v", got)
	}
	if got["bbbb"] == "" {
		t.Errorf("a seeding torrent wasn't offered for import: %v", got)
	}

	// qBittorrent finishes the move: the torrent seeds from its save folder.
	mu.Lock()
	state, path = "stalledUP", "/completed/Show.S01.1080p"
	mu.Unlock()
	if got := hashes(); got["aaaa"] != "/completed/Show.S01.1080p" {
		t.Errorf("after the move: want the torrent at its final path, got %v", got)
	}
}
