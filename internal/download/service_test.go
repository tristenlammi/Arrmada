package download

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
