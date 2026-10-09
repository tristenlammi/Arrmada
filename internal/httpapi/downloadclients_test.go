package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/download"
)

const bundledQbitURL = "http://arrmada-qbittorrent:8080"

func newClientServer(t *testing.T) (*routeServer, *download.Service) {
	t.Helper()
	var dl *download.Service
	s := newRouteServer(t, func(d *Deps) {
		d.Config.QbittorrentURL = bundledQbitURL
		dl = download.NewService(d.Store.DB(), d.Log)
		d.Downloads = dl
	})
	return s, dl
}

// PUT edits a client in place: 200 with the saved client (never its password), 400 for a
// bad body, 404 for a missing id, 403 for a requester.
func TestUpdateDownloadClient(t *testing.T) {
	s, dl := newClientServer(t)
	ctx := context.Background()
	c, err := dl.Create(ctx, download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: "http://qb:8080", Username: "admin", Password: "old-secret", Category: "arrmada", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)
	path := fmt.Sprintf("/api/v1/downloadclients/%d", c.ID)

	rec := s.doBody("PUT", path, `{"name":"qb home","kind":"qbittorrent","url":"http://qb:9090","username":"me","password":"","enabled":false}`, mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: HTTP %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "password") {
		t.Errorf("response carries the password: %s", rec.Body)
	}
	var got struct {
		Name    string `json:"name"`
		URL     string `json:"url"`
		Enabled bool   `json:"enabled"`
		Bundled bool   `json:"bundled"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Name != "qb home" || got.URL != "http://qb:9090" || got.Enabled || got.Bundled {
		t.Errorf("response = %+v", got)
	}
	if stored, _ := dl.Get(ctx, c.ID); stored.Password != "old-secret" {
		t.Errorf("a blank password replaced the stored one: %q", stored.Password)
	}

	// Leaving enabled out keeps what's stored.
	if rec := s.doBody("PUT", path, `{"name":"qb home","kind":"qbittorrent","url":"http://qb:9090","password":"new-secret"}`, mgr); rec.Code != http.StatusOK {
		t.Fatalf("password change: HTTP %d %s", rec.Code, rec.Body)
	}
	if stored, _ := dl.Get(ctx, c.ID); stored.Password != "new-secret" || stored.Enabled {
		t.Errorf("after password change: %q enabled=%v", stored.Password, stored.Enabled)
	}

	for _, body := range []string{
		`{"name":"","kind":"qbittorrent","url":"http://qb:9090"}`,
		`{"name":"qb","kind":"qbittorrent","url":"  "}`,
		`{"name":"qb","kind":"sabnzbd","url":"http://qb:9090"}`,
	} {
		if rec := s.doBody("PUT", path, body, mgr); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", body, rec.Code)
		}
	}
	if rec := s.doBody("PUT", "/api/v1/downloadclients/9999", `{"name":"qb","kind":"qbittorrent","url":"http://qb:9090"}`, mgr); rec.Code != http.StatusNotFound {
		t.Errorf("missing id: HTTP %d, want 404", rec.Code)
	}
	if rec := s.doBody("PUT", path, `{"name":"qb","kind":"qbittorrent","url":"http://evil:9090"}`, kid); rec.Code != http.StatusForbidden {
		t.Errorf("requester: HTTP %d, want 403", rec.Code)
	}
}

// The bundled client is flagged in the list by its stored flag, and its URL can now be
// edited (startup finds it by the flag, not the URL). Deleted, it stays deleted and the
// list offers it back; restore-bundled brings it back. Priority is validated and saved.
func TestBundledClientRemoveAndRestore(t *testing.T) {
	s, dl := newClientServer(t)
	dl.SetFlags(s.deps.Settings)
	ctx := context.Background()
	if err := dl.EnsureBundled(ctx, bundledQbitURL); err != nil {
		t.Fatal(err)
	}
	if _, err := dl.Create(ctx, download.Client{Name: "other", Kind: download.KindQbittorrent, URL: "http://qb:8080", Enabled: true, Priority: 30}); err != nil {
		t.Fatal(err)
	}
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)

	type listed struct {
		Clients []struct {
			ID       int64  `json:"id"`
			URL      string `json:"url"`
			Bundled  bool   `json:"bundled"`
			Priority int    `json:"priority"`
		} `json:"clients"`
		CanRestore bool `json:"can_restore_bundled"`
	}
	list := func() listed {
		t.Helper()
		var l listed
		rec := s.do("GET", "/api/v1/downloadclients", mgr)
		if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
			t.Fatalf("list: %v %s", err, rec.Body)
		}
		return l
	}
	l := list()
	if len(l.Clients) != 2 || !l.Clients[0].Bundled || l.Clients[1].Bundled || l.CanRestore {
		t.Fatalf("list = %+v", l)
	}

	path := fmt.Sprintf("/api/v1/downloadclients/%d", l.Clients[0].ID)
	if rec := s.doBody("PUT", path, `{"name":"qb","kind":"qbittorrent","url":"http://elsewhere:8080","priority":40}`, mgr); rec.Code != http.StatusOK {
		t.Fatalf("bundled URL + order edit: HTTP %d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{
		`{"name":"qb","kind":"qbittorrent","url":"http://elsewhere:8080","priority":0}`,
		`{"name":"qb","kind":"qbittorrent","url":"http://elsewhere:8080","priority":100}`,
	} {
		if rec := s.doBody("PUT", path, body, mgr); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", body, rec.Code)
		}
	}
	// Startup with the edited URL adds nothing: the flagged row is the bundled one.
	if err := dl.EnsureBundled(ctx, bundledQbitURL); err != nil {
		t.Fatal(err)
	}
	l = list()
	if len(l.Clients) != 2 || l.Clients[0].URL != "http://qb:8080" || l.Clients[1].Priority != 40 || !l.Clients[1].Bundled {
		t.Fatalf("after edit + startup: %+v", l)
	}

	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/downloadclients/%d", l.Clients[1].ID), mgr); rec.Code != http.StatusNoContent {
		t.Fatalf("delete bundled: HTTP %d %s", rec.Code, rec.Body)
	}
	if err := dl.EnsureBundled(ctx, bundledQbitURL); err != nil { // a restart
		t.Fatal(err)
	}
	if l = list(); len(l.Clients) != 1 || !l.CanRestore {
		t.Fatalf("a deleted bundled client came back, or isn't offered: %+v", l)
	}

	if rec := s.do("POST", "/api/v1/downloadclients/restore-bundled", kid); rec.Code != http.StatusForbidden {
		t.Errorf("requester restore: HTTP %d, want 403", rec.Code)
	}
	if rec := s.do("POST", "/api/v1/downloadclients/restore-bundled", mgr); rec.Code != http.StatusOK {
		t.Fatalf("restore: HTTP %d %s", rec.Code, rec.Body)
	}
	l = list()
	if len(l.Clients) != 2 || l.CanRestore {
		t.Fatalf("after restore: %+v", l)
	}
	found := false
	for _, c := range l.Clients {
		found = found || (c.Bundled && c.URL == bundledQbitURL)
	}
	if !found {
		t.Errorf("restore didn't bring the bundled client back: %+v", l)
	}
}

// The list carries Arrmada's fixed categories (the movie one from the config), and a
// category sent on create is accepted but never stored or echoed back.
func TestDownloadClientCategoriesAreArrmadas(t *testing.T) {
	var dl *download.Service
	s := newRouteServer(t, func(d *Deps) {
		d.Config.DownloadCategory = "arrmada-films"
		dl = download.NewService(d.Store.DB(), d.Log)
		d.Downloads = dl
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)

	rec := s.doBody("POST", "/api/v1/downloadclients", `{"name":"qb","kind":"qbittorrent","url":"http://qb:8080","category":"movies"}`, mgr)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: HTTP %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "movies") {
		t.Errorf("create echoed the free-text category: %s", rec.Body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if stored, _ := dl.Get(context.Background(), created.ID); stored.Category != "" {
		t.Errorf("stored category = %q, want none", stored.Category)
	}

	rec = s.do("GET", "/api/v1/downloadclients", mgr)
	var list struct {
		Categories download.Categories `json:"categories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	want := download.Categories{Movies: "arrmada-films", TV: "arrmada-tv", Books: "arrmada-books", Music: "arrmada-music"}
	if list.Categories != want {
		t.Errorf("categories = %+v, want %+v", list.Categories, want)
	}
}
