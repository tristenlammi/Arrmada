package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// fakeQbit is a qBittorrent stub that records every torrent-scoped call it receives.
type fakeQbit struct {
	mu    sync.Mutex
	calls []url.Values // form of each /api/v2/torrents/* call
	paths []string
}

func (f *fakeQbit) server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "sid", Path: "/"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.calls = append(f.calls, r.PostForm)
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		fmt.Fprint(w, "Ok.")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeQbit) snapshot() ([]string, []url.Values) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...), append([]url.Values(nil), f.calls...)
}

// downloadServer is the real router with a real download service pointed at fakeQbit.
func downloadServer(t *testing.T) (*routeServer, *fakeQbit, *http.Cookie) {
	t.Helper()
	q := &fakeQbit{}
	srv := q.server(t)
	s := newRouteServer(t, func(d *Deps) {
		dl := download.NewService(d.Store.DB(), d.Log)
		if _, err := dl.Create(context.Background(), download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: srv.URL, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		d.Downloads = dl
		mv := movies.NewService(d.Store.DB(), nil, nil, t.TempDir(), "", nil, d.Log)
		d.Movies = mv
		d.Automation = automation.New(mv, nil, dl, nil, d.Store.DB(), nil, d.Log, "")
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	return s, q, mgr
}

const testHash = "0123456789abcdef0123456789abcdef01234567"

// "all", an empty hash, a pipe list or anything not one hash never reaches the client —
// qBittorrent reads those as "every torrent" / "several".
func TestDeleteDownloadRejectsNonHash(t *testing.T) {
	s, q, mgr := downloadServer(t)
	for _, p := range []string{
		"/api/v1/queue/all",
		"/api/v1/queue/all?delete_data=true",
		"/api/v1/queue/%7C",
		"/api/v1/queue/" + testHash + "%7C" + testHash,
		"/api/v1/queue/" + testHash[:39],
		"/api/v1/queue/xyz",
	} {
		if rec := s.do("DELETE", p, mgr); rec.Code != http.StatusBadRequest {
			t.Errorf("DELETE %s: HTTP %d, want 400", p, rec.Code)
		}
	}
	for _, p := range []string{"/api/v1/queue/all/pause", "/api/v1/queue/all/resume"} {
		if rec := s.do("POST", p, mgr); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s: HTTP %d, want 400", p, rec.Code)
		}
	}
	if paths, _ := q.snapshot(); len(paths) != 0 {
		t.Fatalf("the client was called: %v", paths)
	}
}

// With no mode the files are kept; the old delete_data=true still means delete files.
func TestDeleteDownloadModes(t *testing.T) {
	cases := []struct {
		query, wantDelete string
	}{
		{"", "false"},
		{"?mode=keep_files", "false"},
		{"?mode=delete_files", "true"},
		{"?delete_data=true", "true"},
	}
	for _, tc := range cases {
		s, q, mgr := downloadServer(t)
		rec := s.do("DELETE", "/api/v1/queue/"+testHash+tc.query, mgr)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: HTTP %d: %s", tc.query, rec.Code, rec.Body)
		}
		paths, forms := q.snapshot()
		if len(paths) != 1 || paths[0] != "/api/v2/torrents/delete" {
			t.Fatalf("%q: client calls = %v", tc.query, paths)
		}
		if got := forms[0].Get("hashes"); got != testHash {
			t.Errorf("%q: hashes = %q", tc.query, got)
		}
		if got := forms[0].Get("deleteFiles"); got != tc.wantDelete {
			t.Errorf("%q: deleteFiles = %q, want %s", tc.query, got, tc.wantDelete)
		}
	}

	s, _, mgr := downloadServer(t)
	if rec := s.do("DELETE", "/api/v1/queue/"+testHash+"?mode=wipe", mgr); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown mode: HTTP %d, want 400", rec.Code)
	}
	if rec := s.do("DELETE", "/api/v1/queue/"+testHash+"?mode=block&unmonitor=true", mgr); rec.Code != http.StatusBadRequest {
		t.Errorf("block + stop wanting: HTTP %d, want 400", rec.Code)
	}
}
