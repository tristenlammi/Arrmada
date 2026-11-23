package jellyfinsvc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/jellyfin"
	"github.com/tristenlammi/arrmada/internal/mediaserver"
	"github.com/tristenlammi/arrmada/internal/plexscan"
)

// The Jellyfin connection set up for an Emby server. The fake answers only under /emby and
// only to X-Emby-Token, with response shapes taken from Emby's REST API reference
// (internal/jellyfin/testdata/emby): there's no real Emby to test against.

type fakeEmby struct {
	*httptest.Server
	key     string
	mu      sync.Mutex
	updates [][]jellyfin.MediaUpdate
	paths   []string
}

func newFakeEmby(t *testing.T, key string) *fakeEmby {
	t.Helper()
	f := &fakeEmby{key: key}
	pub, info := fixture(t, "emby/system_info_public.json"), fixture(t, "emby/system_info.json")
	vfs, items := fixture(t, "emby/virtual_folders_query.json"), fixture(t, "emby/items_index.json")
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		p, ok := strings.CutPrefix(r.URL.Path, "/emby")
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected Authorization header", http.StatusBadRequest)
			return
		}
		if p == "/System/Info/Public" {
			_, _ = w.Write(pub)
			return
		}
		if r.Header.Get("X-Emby-Token") != f.key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case p == "/System/Info":
			_, _ = w.Write(info)
		case p == "/Library/VirtualFolders/Query":
			_, _ = w.Write(vfs)
		case p == "/Items":
			_, _ = w.Write(items)
		case p == "/Library/Media/Updated" && r.Method == http.MethodPost:
			var body struct{ Updates []jellyfin.MediaUpdate }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.updates = append(f.updates, body.Updates)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func newEmbyHarness(t *testing.T) (*Service, *memSettings, *fakeEmby) {
	t.Helper()
	t.Setenv("ARRMADA_JELLYFIN_API_KEY", "")
	set := &memSettings{m: map[string]string{}}
	s := New(Options{
		Settings: set, Keys: apikeys.NewStore(set), Version: "0.9-test", Log: quiet(),
		Roots: func(context.Context) map[string]string {
			return map[string]string{plexscan.KindMovie: "/movies", plexscan.KindShow: "/tv"}
		},
	})
	return s, set, newFakeEmby(t, "emby-key-1")
}

func TestEmbyConnection(t *testing.T) {
	ctx := context.Background()
	s, set, em := newEmbyHarness(t)
	if s.ServerKind(ctx) != jellyfin.ServerJellyfin || s.Label(ctx) != "Jellyfin" {
		t.Fatalf("unset kind = %q / %q", s.ServerKind(ctx), s.Label(ctx))
	}
	if err := s.SaveFor(ctx, "plex", em.URL, "", "k"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind = %v", err)
	}

	// Test before saving: the typed kind decides the dialect.
	if r := s.TestFor(ctx, "emby", em.URL, ""); r.OK || !strings.Contains(r.Detail, "Emby answers") || r.ServerName != "emby-den" {
		t.Fatalf("public test = %+v", r)
	}
	if r := s.TestFor(ctx, "emby", em.URL, "wrong"); r.OK || !strings.Contains(r.Detail, "Emby rejected the API key") || strings.Contains(r.Detail, "wrong") {
		t.Fatalf("wrong key = %+v", r)
	}
	if r := s.TestFor(ctx, "emby", em.URL, "emby-key-1"); !r.OK || r.Version != "4.8.10.0" {
		t.Fatalf("good key = %+v", r)
	}
	// Spoken to as Jellyfin, Emby's root path isn't the API.
	if r := s.TestFor(ctx, "jellyfin", em.URL, "emby-key-1"); r.OK || !strings.Contains(r.Detail, "isn't a Jellyfin server") {
		t.Fatalf("as jellyfin = %+v", r)
	}

	if err := s.SaveFor(ctx, "emby", em.URL+"/", "https://emby.example.com", "emby-key-1"); err != nil {
		t.Fatal(err)
	}
	cfg := s.Config(ctx)
	if s.ServerKind(ctx) != "emby" || cfg.ServerName != "emby-den" || cfg.Version != "4.8.10.0" || !cfg.KeySet {
		t.Fatalf("config = %+v", cfg)
	}
	if set.m[KeyServerKind] != "emby" || s.Label(ctx) != "Emby" {
		t.Fatalf("kind setting = %q", set.m[KeyServerKind])
	}
	// The saved connection tests through the saved kind, and a blank kind keeps it.
	if r := s.Test(ctx, "", ""); !r.OK {
		t.Fatalf("saved test = %+v", r)
	}
	if err := s.Save(ctx, em.URL, "https://emby.example.com", ""); err != nil || s.ServerKind(ctx) != "emby" {
		t.Fatalf("save without a kind = %v, kind %q", err, s.ServerKind(ctx))
	}

	// The index reads Emby's libraries; Watch links open Emby's web app.
	if err := s.idx.rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	it, ok := s.Locate(ctx, "movie", mediaserver.ExternalIDs{TMDB: 603})
	if !ok || it.ID != "1201" || it.Server != mediaserver.Jellyfin {
		t.Fatalf("locate = %+v, %v", it, ok)
	}
	if _, ok := s.Locate(ctx, "series", mediaserver.ExternalIDs{TVDB: 371980}); !ok {
		t.Fatal("show by TVDB not found")
	}
	want := "https://emby.example.com/web/index.html#!/item?id=1201&serverId=5d6e0c7b8a9f4e2d9c1b0a3f7e6d5c4b"
	if u := s.WatchURL(ctx, "movie", mediaserver.ExternalIDs{TMDB: 603}); u != want {
		t.Fatalf("watch url = %s", u)
	}

	// Partial scans go to Emby's /Library/Media/Updated.
	libs, err := scanClient{s.Client(ctx)}.Libraries(ctx)
	if err != nil || len(libs) != 2 || libs[0].Type != "movie" || libs[0].Locations[0] != "/data/movies" {
		t.Fatalf("libraries = %+v, %v", libs, err)
	}
	if err := (scanClient{s.Client(ctx)}).RefreshPaths(ctx, []string{"/data/movies/Heat (1995)"}); err != nil {
		t.Fatal(err)
	}
	em.mu.Lock()
	got := em.updates
	em.mu.Unlock()
	if len(got) != 1 || got[0][0].Path != "/data/movies/Heat (1995)" {
		t.Fatalf("updates = %+v", got)
	}

	// The health record and its words name Emby.
	if d := DescribeFor("emby", jellyfin.ErrUnauthorized); !strings.Contains(d, "Emby rejected") || strings.Contains(d, "Jellyfin") {
		t.Errorf("describe = %q", d)
	}
	if d := DescribeFor("jellyfin", jellyfin.ErrUnauthorized); d != Describe(jellyfin.ErrUnauthorized) {
		t.Errorf("jellyfin describe changed: %q", d)
	}
	s.observe(jellyfin.ErrUnauthorized)
	if h := s.Health(ctx); !strings.Contains(h.LastErr, "Emby") {
		t.Errorf("health = %+v", h)
	}
}

// Switching an existing Jellyfin connection to Emby is a new server: what the old one said
// is forgotten, and no new key is needed for the same address.
func TestSwitchKindForgetsIdentity(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.connect(t)
	if h.s.Config(ctx).ServerName != "arrmada-it" {
		t.Fatalf("jellyfin identity = %+v", h.s.Config(ctx))
	}
	if _, ok := h.set.m[KeyServerKind]; ok {
		t.Fatal("saving a Jellyfin connection wrote a kind")
	}
	if err := h.s.SaveFor(ctx, "emby", h.jf.URL, "", ""); err != nil {
		t.Fatal(err)
	}
	cfg := h.s.Config(ctx)
	if h.s.ServerKind(ctx) != "emby" || cfg.ServerName != "" || cfg.ServerID != "" {
		t.Fatalf("after switching = %+v", cfg)
	}
	// The Jellyfin fake doesn't speak Emby, so the connection says so.
	if r := h.s.Test(ctx, "", ""); r.OK {
		t.Fatalf("jellyfin answered as emby: %+v", r)
	}
	// Choosing Emby for a server that says it's Jellyfin is caught at Test.
	if r := h.s.TestFor(ctx, "emby", "", ""); r.OK {
		t.Fatalf("test = %+v", r)
	}
	if err := h.s.SaveFor(ctx, "jellyfin", h.jf.URL, "", ""); err != nil || h.s.Config(ctx).ServerName != "arrmada-it" {
		t.Fatalf("back to jellyfin = %v, %+v", err, h.s.Config(ctx))
	}
}
