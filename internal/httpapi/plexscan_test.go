package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/plexscan"
	"github.com/tristenlammi/arrmada/internal/settings"
)

// fakePMS is a Plex server with one movie and one TV section, recording scan requests.
type fakePMS struct {
	mu    sync.Mutex
	scans []string
}

func (f *fakePMS) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "plex-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/library/sections":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[
				{"key":"1","title":"Movies","type":"movie","Location":[{"path":"/data/media/movies"}]},
				{"key":"2","title":"TV Shows","type":"show","Location":[{"path":"/srv/shows"}]}]}}`))
		case strings.HasSuffix(r.URL.Path, "/refresh"):
			f.mu.Lock()
			f.scans = append(f.scans, r.URL.Path+"?"+r.URL.Query().Get("path"))
			f.mu.Unlock()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func plexScanAPI(t *testing.T, pmsURL string) *api {
	t.Helper()
	a := dashAPI(t)
	set := settings.NewService(a.deps.Store.DB())
	ins := insights.NewService(a.deps.Store.DB(), set, nil, nil, a.deps.Log)
	if pmsURL != "" {
		tok := "plex-secret"
		if err := ins.SetConfig(context.Background(), pmsURL, &tok, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	a.deps.Insights = ins
	a.deps.PlexScan = plexscan.New(plexscan.Options{
		Client:     func(ctx context.Context) plexscan.Client { return ins.PlexClient(ctx) },
		Configured: ins.Configured,
		Settings:   set,
		Roots: func(context.Context) map[string]string {
			return map[string]string{plexscan.KindMovie: "/movies", plexscan.KindShow: "/tv"}
		},
		Log: a.deps.Log,
	})
	return a
}

func TestPlexScanViewResolvesRootsWithoutLeakingTheToken(t *testing.T) {
	pms := &fakePMS{}
	srv := pms.server(t)
	a := plexScanAPI(t, srv.URL)

	rec := httptest.NewRecorder()
	a.handlePlexScanView(rec, httptest.NewRequest("GET", "/api/v1/insights/plex/scan", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, "plex-secret") || strings.Contains(body, srv.URL) {
		t.Fatalf("the view leaks the token or the server address: %s", body)
	}
	var v plexscan.View
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !v.Enabled || !v.Configured || len(v.Roots) != 2 {
		t.Fatalf("view = %+v", v)
	}
	// /movies ↔ /data/media/movies is guessed; /tv has nothing in common with /srv/shows.
	if r := v.Roots[0]; r.How != plexscan.HowGuessed || r.PlexPath != "/data/media/movies" {
		t.Errorf("movies root = %+v", r)
	}
	if r := v.Roots[1]; r.How != plexscan.HowSection || len(r.Sections) != 1 || r.Sections[0] != "TV Shows" {
		t.Errorf("tv root = %+v", r)
	}

	// A mapping fixes TV; a relative path is refused.
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		a.handlePlexScanSave(rec, httptest.NewRequest("PUT", "/api/v1/insights/plex/scan", strings.NewReader(body)))
		return rec
	}
	if rec := put(`{"enabled":true,"path_map":[{"from":"tv","to":"/srv/shows"}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("relative mapping: status %d", rec.Code)
	}
	rec = put(`{"enabled":true,"path_map":[{"from":"/tv","to":"/srv/shows"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if r := v.Roots[1]; r.How != plexscan.HowMapped || r.PlexPath != "/srv/shows" {
		t.Errorf("tv root after mapping = %+v", r)
	}
	// A save that names one field leaves the other as it was.
	rec = put(`{"enabled":false}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.Enabled || len(v.PathMap) != 1 {
		t.Fatalf("toggle-only save: %s", rec.Body)
	}
	rec = put(`{"enabled":true}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || !v.Enabled || len(v.PathMap) != 1 {
		t.Fatalf("toggle back on: %s", rec.Body)
	}

	// Scan now sends one partial scan of the Plex-side folder.
	rec = httptest.NewRecorder()
	a.handlePlexScanTest(rec, httptest.NewRequest("POST", "/api/v1/insights/plex/scan/test", strings.NewReader(`{"kind":"show","run":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("scan now: %d %s", rec.Code, rec.Body)
	}
	pms.mu.Lock()
	defer pms.mu.Unlock()
	if len(pms.scans) != 1 || pms.scans[0] != "/library/sections/2/refresh?/srv/shows" {
		t.Fatalf("scans = %v", pms.scans)
	}
}

func TestPlexScanUnconfigured(t *testing.T) {
	a := plexScanAPI(t, "")
	rec := httptest.NewRecorder()
	a.handlePlexScanView(rec, httptest.NewRequest("GET", "/api/v1/insights/plex/scan", nil))
	var v plexscan.View
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.Configured || v.Error != "" {
		t.Fatalf("view = %+v (%v)", v, err)
	}
	rec = httptest.NewRecorder()
	a.handlePlexScanTest(rec, httptest.NewRequest("POST", "/api/v1/insights/plex/scan/test", strings.NewReader(`{"kind":"movie","run":true}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("scan now without Plex: %d", rec.Code)
	}
}
