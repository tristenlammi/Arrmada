package plex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const sectionsFixture = `{"MediaContainer":{"size":3,"Directory":[
 {"key":"1","title":"Movies","type":"movie","Location":[{"id":1,"path":"/data/media/movies"}]},
 {"key":"2","title":"TV Shows","type":"show","Location":[{"id":2,"path":"/data/media/tv"},{"id":3,"path":"/mnt/disk2/tv"}]},
 {"key":"3","title":"Music","type":"artist"}
]}}`

func TestLibrariesDecodeLocations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/sections" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(sectionsFixture))
	}))
	defer srv.Close()

	libs, err := New(srv.URL, "tok").Libraries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Library{
		{Key: "1", Title: "Movies", Type: "movie", Locations: []string{"/data/media/movies"}},
		{Key: "2", Title: "TV Shows", Type: "show", Locations: []string{"/data/media/tv", "/mnt/disk2/tv"}},
		{Key: "3", Title: "Music", Type: "artist"},
	}
	if !reflect.DeepEqual(libs, want) {
		t.Fatalf("libraries = %+v\nwant %+v", libs, want)
	}
}

// The folder reaches Plex exactly, whatever it's called: spaces, brackets, apostrophes,
// an ampersand that would otherwise start a new parameter, and a literal plus.
func TestRefreshPathEscaping(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotRaw, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotRaw, gotToken = r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Plex-Token")
		mu.Unlock()
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "secret-token")

	for _, dir := range []string{
		"/data/media/movies/Heat (1995)",
		"/data/media/movies/Ocean's Eleven (2001)",
		"/data/media/tv/Law & Order (1990)/Season 01",
		"/data/media/movies/C++ & You [2020]",
		`D:\Media\Movies\Amélie (2001)`,
	} {
		if err := c.RefreshPath(context.Background(), "7", dir); err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		mu.Lock()
		if gotPath != "/library/sections/7/refresh" {
			t.Errorf("path = %q", gotPath)
		}
		if strings.Contains(gotRaw, "+") {
			t.Errorf("query %q encodes something as '+'", gotRaw)
		}
		q, err := url.ParseQuery(gotRaw)
		if err != nil || q.Get("path") != dir || len(q) != 1 {
			t.Errorf("query %q decodes to %v, want path=%q only", gotRaw, q, dir)
		}
		if strings.Contains(gotRaw, "secret-token") || gotToken != "secret-token" {
			t.Errorf("the token must travel in the header only (query %q)", gotRaw)
		}
		mu.Unlock()
	}

	if err := c.RefreshSection(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/library/sections/7/refresh" || gotRaw != "" {
		t.Errorf("section refresh hit %q?%q", gotPath, gotRaw)
	}
}

func TestRefreshPathErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/sections/9/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	if err := c.RefreshPath(context.Background(), "1", "/x"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("401 = %v, want ErrUnauthorized", err)
	}
	if err := c.RefreshPath(context.Background(), "9", "/x"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404 = %v", err)
	}
	if err := New("", "").RefreshPath(context.Background(), "1", "/x"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("unconfigured = %v", err)
	}
}
