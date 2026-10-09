package indexer

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// fakeMAM answers MAM's search API with a canned reply and records each request body.
type fakeMAM struct {
	mu      sync.Mutex
	bodies  []mamSearchBody
	cookies []string
	reply   string
}

func (f *fakeMAM) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != mamSearchPath {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var b mamSearchBody
		if err := json.Unmarshal(raw, &b); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c, _ := r.Cookie("mam_id")
		f.mu.Lock()
		f.bodies = append(f.bodies, b)
		if c != nil {
			f.cookies = append(f.cookies, c.Value)
		}
		reply := f.reply
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const mamTwoUploads = `{"found":2,"data":[
 {"id":101,"title":"Project Hail Mary","author_info":"{\"7\":\"Andy Weir\"}","narrator_info":"{\"9\":\"Ray Porter\"}",
  "series_info":"","lang_code":"ENG","filetype":"m4b","size":"512.5 MB","seeders":12,"leechers":1,
  "added":"2026-10-09 08:00:00","dl":"tok101","category":13},
 {"id":102,"title":"Coven of Bones","author_info":"{\"8\":\"Harper L. Woods\"}","narrator_info":"",
  "series_info":"{\"3\":[\"Coven of Bones\",\"1\"]}","lang_code":"ENG","filetype":"epub","size":"1.2 MB","seeders":4,"leechers":0,
  "added":"2026-10-09 07:30:00","dl":"tok102","category":14}]}`

// BOOK-07: Recent is the search API with no text, newest first, books only, one page of at
// most 100 — and the items map onto releases with their format, author and language.
func TestMAMRecentAsksForNewestBooks(t *testing.T) {
	f := &fakeMAM{reply: mamTwoUploads}
	srv := f.server(t)
	m := NewMAMSearcher(nil)
	m.base = srv.URL
	idx := Indexer{ID: 1, Name: "MAM", Kind: KindMAM, APIKey: "session-abc"}

	rels, err := m.Recent(context.Background(), idx, 250)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.bodies) != 1 {
		t.Fatalf("sent %d requests, want one per feed pull", len(f.bodies))
	}
	tor := f.bodies[0].Tor
	if tor.Text != "" || tor.SortType != "dateDesc" || tor.StartNumber != "0" || tor.SearchIn != "torrents" {
		t.Errorf("body = %+v, want empty text, dateDesc, first page of torrents", tor)
	}
	if tor.PerPage < 1 || tor.PerPage > 100 {
		t.Errorf("perpage = %d, want 1..100", tor.PerPage)
	}
	if len(tor.MainCat) != 2 || tor.MainCat[0] != mamCatAudiobooks || tor.MainCat[1] != mamCatEbooks {
		t.Errorf("main_cat = %v, want the book categories %v", tor.MainCat, mamBookMainCats)
	}
	if len(f.cookies) != 1 || f.cookies[0] != "session-abc" {
		t.Errorf("mam_id cookie = %v, want the session", f.cookies)
	}

	if len(rels) != 2 {
		t.Fatalf("got %d releases, want 2", len(rels))
	}
	r := rels[0]
	if r.Title != "Andy Weir - Project Hail Mary [M4B]" || r.Format != "M4B" || r.Author != "Andy Weir" ||
		r.Language != "ENG" || r.Narrator != "Ray Porter" || r.Indexer != "MAM" || r.Seeders != 12 {
		t.Errorf("first release mapped as %+v", r)
	}
	if rels[1].Format != "EPUB" || rels[1].Series != "Coven of Bones #1" {
		t.Errorf("second release mapped as %+v", rels[1])
	}
	for _, rel := range rels {
		if strings.Contains(rel.DownloadURL+rel.InfoURL+rel.Description+rel.Title, "session-abc") {
			t.Errorf("the mam_id session leaked into a release: %+v", rel)
		}
	}

	// A configured category list is honoured; perpage never exceeds MAM's 100.
	idx.Categories = []int{mamCatEbooks}
	if _, err := m.Recent(context.Background(), idx, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.bodies[1].Tor; len(got.MainCat) != 1 || got.MainCat[0] != mamCatEbooks || got.PerPage != 100 {
		t.Errorf("second pull = %+v, want main_cat [14] and perpage 100", got)
	}
}

// MAM says "no results" with an error string; that is an empty feed, not a failure.
func TestMAMRecentNothingIsEmpty(t *testing.T) {
	f := &fakeMAM{reply: `{"error":"Nothing returned, out of 0"}`}
	srv := f.server(t)
	m := NewMAMSearcher(nil)
	m.base = srv.URL
	rels, err := m.Recent(context.Background(), Indexer{Name: "MAM", Kind: KindMAM, APIKey: "s"}, 100)
	if err != nil || len(rels) != 0 {
		t.Errorf("Recent = %v, %v; want an empty list and no error", rels, err)
	}
	if _, err := m.Recent(context.Background(), Indexer{Name: "MAM", Kind: KindMAM}, 100); err == nil {
		t.Error("a MAM indexer with no session must fail, not report an empty feed")
	}
}

// The indexer service's feed now includes MyAnonaMouse: it satisfies Recenter, so
// fetchRecent no longer skips it.
func TestFetchRecentIncludesMAM(t *testing.T) {
	var _ Recenter = (*MAMSearcher)(nil)

	f := &fakeMAM{reply: mamTwoUploads}
	srv := f.server(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := NewService(st.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	searcher, err := s.registry.For(KindMAM)
	if err != nil {
		t.Fatal(err)
	}
	searcher.(*MAMSearcher).base = srv.URL
	if _, err := s.Create(context.Background(), Indexer{Name: "MyAnonaMouse", Kind: KindMAM, APIKey: "sess", Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Recent(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Releases) != 2 {
		t.Fatalf("feed has %d releases, want MAM's 2", len(res.Releases))
	}
	// Shared for the three RSS sweeps: a second pull inside the window asks MAM nothing.
	if _, err := s.Recent(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if n := len(f.bodies); n != 1 {
		t.Errorf("MAM was asked %d times in one cycle, want 1", n)
	}
}
