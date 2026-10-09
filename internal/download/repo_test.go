package download

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func newTestRepo(t *testing.T) *Repo {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewRepo(st.DB())
}

// Editing a client keeps the stored password when the form leaves it blank (the browser
// never holds it), replaces it when one is typed, and leaves kind and category alone.
func TestRepoUpdateKeepsBlankPassword(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	c, err := r.Create(ctx, Client{Name: "qb", Kind: KindQbittorrent, URL: "http://qb:8080", Username: "admin", Password: "old-secret", Category: "arrmada", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Update(ctx, Client{ID: c.ID, Name: "qb home", URL: "http://qb:9090", Username: "me", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get(ctx, c.ID)
	if got.Name != "qb home" || got.URL != "http://qb:9090" || got.Username != "me" || got.Enabled {
		t.Errorf("after edit: %+v", got)
	}
	if got.Password != "old-secret" {
		t.Errorf("blank password replaced the stored one: %q", got.Password)
	}
	if got.Kind != KindQbittorrent || got.Category != "arrmada" {
		t.Errorf("kind/category changed: %+v", got)
	}

	if err := r.Update(ctx, Client{ID: c.ID, Name: "qb home", URL: "http://qb:9090", Password: "new-secret", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get(ctx, c.ID); got.Password != "new-secret" || !got.Enabled {
		t.Errorf("after password change: password %q enabled %v", got.Password, got.Enabled)
	}

	if err := r.Update(ctx, Client{ID: c.ID + 99, Name: "x", URL: "http://x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing id: %v, want ErrNotFound", err)
	}
}

// A disabled client is out of every place a client is chosen: no grab goes to it and it
// isn't asked for the queue — and that survives a restart (it's the stored flag).
func TestDisabledClientGetsNoDownloads(t *testing.T) {
	var adds atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/add":
			adds.Add(1)
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			_, _ = io.WriteString(w, "[]")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewService(st.DB(), log)
	ctx := context.Background()
	c, err := svc.Create(ctx, Client{Name: "qb", Kind: KindQbittorrent, URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, Client{ID: c.ID, Name: "qb", URL: srv.URL, Enabled: false}); err != nil {
		t.Fatal(err)
	}

	// A fresh service over the same database stands in for a restart.
	svc = NewService(st.DB(), log)
	if err := svc.Add(ctx, AddRequest{Name: "x", URL: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"}); err == nil {
		t.Error("Add succeeded with the only client switched off")
	}
	if adds.Load() != 0 {
		t.Errorf("the disabled client was sent %d downloads", adds.Load())
	}
	if states, _ := svc.ClientStates(ctx); len(states) != 0 {
		t.Errorf("health asked a disabled client: %+v", states)
	}
}
