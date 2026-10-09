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

// The bundled client is flagged in the list and its URL can't be edited: startup re-adds
// a row for that URL whenever none has it, so an edit would spawn a duplicate.
func TestBundledClientURLLocked(t *testing.T) {
	s, dl := newClientServer(t)
	ctx := context.Background()
	if err := dl.EnsureBundled(ctx, bundledQbitURL); err != nil {
		t.Fatal(err)
	}
	if _, err := dl.Create(ctx, download.Client{Name: "other", Kind: download.KindQbittorrent, URL: "http://qb:8080", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)

	rec := s.do("GET", "/api/v1/downloadclients", mgr)
	var list struct {
		Clients []struct {
			ID      int64  `json:"id"`
			URL     string `json:"url"`
			Bundled bool   `json:"bundled"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Clients) != 2 {
		t.Fatalf("list: %v %s", err, rec.Body)
	}
	if !list.Clients[0].Bundled || list.Clients[1].Bundled {
		t.Errorf("bundled flags = %v/%v, want true/false", list.Clients[0].Bundled, list.Clients[1].Bundled)
	}

	path := fmt.Sprintf("/api/v1/downloadclients/%d", list.Clients[0].ID)
	if rec := s.doBody("PUT", path, `{"name":"qb","kind":"qbittorrent","url":"http://elsewhere:8080"}`, mgr); rec.Code != http.StatusBadRequest {
		t.Errorf("bundled URL edit: HTTP %d, want 400", rec.Code)
	}
	// Renaming it or switching it off is fine.
	body := fmt.Sprintf(`{"name":"Bundled","kind":"qbittorrent","url":%q,"enabled":false}`, bundledQbitURL)
	if rec := s.doBody("PUT", path, body, mgr); rec.Code != http.StatusOK {
		t.Errorf("bundled rename/disable: HTTP %d %s", rec.Code, rec.Body)
	}
}
