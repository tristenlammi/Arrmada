package applog

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactPath(t *testing.T) {
	cases := map[string]string{
		"/api/items/b12":        "/api/items/{id}",
		"/api/items/b12v3/play": "/api/items/{id}/play",
		"/api/items/mb12":       "/api/items/{id}",
		"/api/session/2f1c7e0a-3b4d-4c5e-8f9a-0b1c2d3e4f5a/sync": "/api/session/{id}/sync",
		"/api/authors/au0123456789ab":                            "/api/authors/{id}",
		"/api/series/seabcdefabcdef":                             "/api/series/{id}",
		"/api/users/u5":                                          "/api/users/{id}",
		"/api/v1/books/12/audiobook":                             "/api/v1/books/{id}/audiobook",
		"/api/items/b12/file/1234567/download":                   "/api/items/{id}/file/{id}/download",
		"/api/libraries/arrmada-audiobooks":                      "/api/libraries/arrmada-audiobooks",
		"/api/me/items-in-progress":                              "/api/me/items-in-progress",
		"/":                                                      "/",
		"":                                                       "",
	}
	for in, want := range cases {
		if got := RedactPath(in); got != want {
			t.Errorf("RedactPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRouteLabel(t *testing.T) {
	mux := http.NewServeMux()
	var label string
	mux.HandleFunc("POST /api/items/{id}/play", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})

	r := httptest.NewRequest("POST", "/api/items/b45/play", nil)
	mux.ServeHTTP(httptest.NewRecorder(), r)
	if label = RouteLabel(r); label != "POST /api/items/{id}/play" {
		t.Errorf("matched route labelled %q", label)
	}
	// The catch-all says nothing; the redacted path does.
	r = httptest.NewRequest("GET", "/api/items/b45/nope", nil)
	mux.ServeHTTP(httptest.NewRecorder(), r)
	if label = RouteLabel(r); label != "GET /api/items/{id}/nope" {
		t.Errorf("unmatched route labelled %q", label)
	}
}

func TestQueryKeys(t *testing.T) {
	q := url.Values{"q": {"dungeon crawler"}, "limit": {"5"}, "token": {"secret"}}
	if got := QueryKeys(q, "token"); got != "limit,q" {
		t.Errorf("QueryKeys = %q, want %q", got, "limit,q")
	}
	if got := QueryKeys(nil); got != "" {
		t.Errorf("QueryKeys(nil) = %q", got)
	}
}

func TestRewriteFilesScrubsEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "arrmada.log.jsonl")
	untouched := `{"time_ms":1,"level":"INFO","msg":"series: grabbing","attrs":"series=Taskmaster"}`
	current := untouched + "\n" +
		`{"time_ms":2,"level":"INFO","msg":"secret","attrs":"item=b45"}` + "\n" +
		`not json` + "\n" +
		`{"time_ms":3,"level":"INFO","msg":"drop me"}` + "\n" +
		`{"time_ms":4,"level":"INFO","msg":"tor` // killed mid-write
	rotated := `{"time_ms":0,"level":"INFO","msg":"no time"}` + "\n" +
		`{"time_ms":5,"level":"INFO","msg":"secret","attrs":"item=b7"}` + "\n"
	if err := os.WriteFile(path, []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".2", []byte(rotated), 0o644); err != nil {
		t.Fatal(err)
	}

	err := RewriteFiles(path, func(e Entry) (Entry, bool) {
		if e.Message == "drop me" {
			return e, false
		}
		if e.Message == "secret" {
			e.Attrs = "item={id}"
		}
		return e, true
	})
	if err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(path)
	want := untouched + "\n" + `{"time_ms":2,"level":"INFO","msg":"secret","attrs":"item={id}"}` + "\n"
	if string(got) != want {
		t.Errorf("current file:\n%s\nwant:\n%s", got, want)
	}
	got, _ = os.ReadFile(path + ".2")
	if want := `{"time_ms":5,"level":"INFO","msg":"secret","attrs":"item={id}"}` + "\n"; string(got) != want {
		t.Errorf("rotated file:\n%s\nwant:\n%s", got, want)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".rewrite-") {
			t.Errorf("left a temp file: %s", e.Name())
		}
	}
	// Missing files are fine.
	if err := RewriteFiles(filepath.Join(dir, "nothing.jsonl"), func(e Entry) (Entry, bool) { return e, true }); err != nil {
		t.Errorf("missing log: %v", err)
	}
}
