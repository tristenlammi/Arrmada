package opds

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/listening"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// --- parsing, the way a reader does: namespace-aware ----------------------------------

type pFeed struct {
	XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
	ID      string   `xml:"http://www.w3.org/2005/Atom id"`
	Title   string   `xml:"http://www.w3.org/2005/Atom title"`
	Updated string   `xml:"http://www.w3.org/2005/Atom updated"`
	Total   int      `xml:"http://a9.com/-/spec/opensearch/1.1/ totalResults"`
	Links   []pLink  `xml:"http://www.w3.org/2005/Atom link"`
	Entries []pEntry `xml:"http://www.w3.org/2005/Atom entry"`
}

type pLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr"`
}

type pEntry struct {
	Title   string   `xml:"http://www.w3.org/2005/Atom title"`
	ID      string   `xml:"http://www.w3.org/2005/Atom id"`
	Updated string   `xml:"http://www.w3.org/2005/Atom updated"`
	Authors []string `xml:"http://www.w3.org/2005/Atom author>name"`
	AuthURI []string `xml:"http://www.w3.org/2005/Atom author>uri"`
	Issued  string   `xml:"http://purl.org/dc/terms/ issued"`
	Summary string   `xml:"http://www.w3.org/2005/Atom summary"`
	Content string   `xml:"http://www.w3.org/2005/Atom content"`
	Links   []pLink  `xml:"http://www.w3.org/2005/Atom link"`
}

func (f pFeed) link(rel string) (pLink, bool) {
	for _, l := range f.Links {
		if l.Rel == rel {
			return l, true
		}
	}
	return pLink{}, false
}

func (e pEntry) link(rel string) (pLink, bool) {
	for _, l := range e.Links {
		if l.Rel == rel {
			return l, true
		}
	}
	return pLink{}, false
}

func (f pFeed) titles() []string {
	var out []string
	for _, e := range f.Entries {
		out = append(out, e.Title)
	}
	return out
}

// --- the harness: a real library and real audiobook passwords on a temp DB -------------

type harness struct {
	t        *testing.T
	ctx      context.Context
	dir      string
	db       *sql.DB
	bs       *books.Service
	users    *auth.Service
	audio    *audioserver.Server
	h        *Handler
	enabled  bool
	ids      map[int64]bool
	keys     map[string]bool
	throttle *countingThrottle
	covers   []int64
}

// countingThrottle stands in for the login limiter: it counts and can be told to refuse.
// Every turn Turn hands out must be given back (open counts them).
type countingThrottle struct {
	failed, succeeded, open int
	refuse                  bool
}

func (c *countingThrottle) Turn(*http.Request, string) (func(), bool, time.Duration) {
	if c.refuse {
		return nil, true, 30 * time.Second
	}
	c.open++
	return func() { c.open-- }, false, 0
}
func (c *countingThrottle) Failed(*http.Request, string, string) { c.failed++ }
func (c *countingThrottle) Succeeded(*http.Request, string)      { c.succeeded++ }

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB().(*sql.DB)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	x := &harness{t: t, ctx: ctx, dir: dir, db: db, enabled: true, throttle: &countingThrottle{}}
	x.users = auth.NewService(db)
	x.bs = books.NewService(db, nil, log)
	x.audio = audioserver.New(audioserver.Options{DB: db, Books: x.bs, Listen: listening.NewStore(db), Users: x.users,
		Settings: settings.NewService(db), Log: log, FFprobe: "ffprobe-not-installed", DataDir: filepath.Join(dir, "data")})
	x.h = New(Deps{
		Library:  x.bs,
		Auth:     x.audio.Accounts,
		Throttle: x.throttle,
		Enabled:  func(context.Context) bool { return x.enabled },
		Requested: func(_ context.Context, userID int64) (map[int64]bool, map[string]bool, error) {
			return x.ids, x.keys, nil
		},
		Cover: func(w http.ResponseWriter, _ *http.Request, b books.Book) {
			x.covers = append(x.covers, b.ID)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png"))
		},
		BaseURL: func(*http.Request) string { return "https://books.example" },
		Log:     log,
	})
	x.user("reader", auth.RoleRequester, "reader-pass-1")
	return x
}

// user makes an account whose Arrmada password is "arrmada-"+name and, when audio isn't
// empty, sets that as its audiobook password.
func (x *harness) user(name string, role auth.Role, audio string) *auth.User {
	x.t.Helper()
	u, err := x.users.CreateUser(x.ctx, name, "arrmada-"+name, role, false)
	if err != nil {
		x.t.Fatal(err)
	}
	if audio != "" {
		if err := x.audio.Accounts.SetPassword(x.ctx, u.ID, audio, false); err != nil {
			x.t.Fatal(err)
		}
	}
	return u
}

// book adds a library book; ebook names its file ("" = no ebook).
func (x *harness) book(title, author, ebook string, mod func(*metadata.BookResult)) books.Book {
	x.t.Helper()
	w := metadata.BookResult{Key: "hc:" + strings.ReplaceAll(strings.ToLower(title), " ", "-"), Title: title, Author: author}
	if mod != nil {
		mod(&w)
	}
	added, _ := x.bs.AddWorks(x.ctx, []metadata.BookResult{w}, "", true)
	if len(added) != 1 {
		x.t.Fatalf("book %q not added", title)
	}
	b := added[0]
	if ebook != "" {
		p := filepath.Join(x.dir, "lib", fmt.Sprint(b.ID), ebook)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("ebook bytes of "+title), 0o644); err != nil {
			x.t.Fatal(err)
		}
		format := strings.ToUpper(strings.TrimPrefix(filepath.Ext(ebook), "."))
		if err := x.bs.MarkImported(x.ctx, b.ID, books.KindEbook, p, format, 100, 1); err != nil {
			x.t.Fatal(err)
		}
	}
	got, err := x.bs.Get(x.ctx, b.ID)
	if err != nil {
		x.t.Fatal(err)
	}
	return got
}

func (x *harness) do(path, user, pass string) *httptest.ResponseRecorder {
	x.t.Helper()
	r := httptest.NewRequest("GET", "http://books.example"+path, nil)
	if user != "" || pass != "" {
		r.SetBasicAuth(user, pass)
	}
	rec := httptest.NewRecorder()
	x.h.ServeHTTP(rec, r)
	return rec
}

// feed fetches a feed as the reader and parses it, checking the media type on the way.
func (x *harness) feed(path, kind string) pFeed {
	x.t.Helper()
	rec := x.do(path, "reader", "reader-pass-1")
	if rec.Code != http.StatusOK {
		x.t.Fatalf("GET %s: HTTP %d %s", path, rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/atom+xml;profile=opds-catalog;kind="+kind) {
		x.t.Errorf("GET %s: Content-Type %q, want an OPDS %s feed", path, ct, kind)
	}
	var f pFeed
	if err := xml.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		x.t.Fatalf("GET %s: not Atom: %v\n%s", path, err, rec.Body)
	}
	if f.ID == "" || f.Title == "" || f.Updated == "" {
		x.t.Errorf("GET %s: feed lacks id/title/updated: %+v", path, f)
	}
	if self, ok := f.link("self"); !ok || self.Href == "" {
		x.t.Errorf("GET %s: no self link", path)
	}
	if _, ok := f.link("start"); !ok {
		x.t.Errorf("GET %s: no start link", path)
	}
	for _, e := range f.Entries {
		if e.ID == "" || e.Updated == "" || e.Title == "" {
			x.t.Errorf("GET %s: entry %+v lacks id/updated/title", path, e)
		}
		if _, err := time.Parse(time.RFC3339, e.Updated); err != nil {
			x.t.Errorf("GET %s: entry %q updated %q isn't RFC 3339", path, e.Title, e.Updated)
		}
	}
	return f
}

// --- tests -----------------------------------------------------------------------------

// The root is a navigation feed to New, My requests, Authors, Series and All, with search.
func TestRootNavigation(t *testing.T) {
	x := newHarness(t)
	f := x.feed("/opds", "navigation")
	want := map[string]string{"/opds/new": "acquisition", "/opds/mine": "acquisition", "/opds/authors": "navigation",
		"/opds/series": "navigation", "/opds/all": "acquisition"}
	for _, e := range f.Entries {
		l, ok := e.link("subsection")
		if !ok {
			t.Fatalf("entry %q has no subsection link: %+v", e.Title, e.Links)
		}
		kind, ok := want[l.Href]
		if !ok {
			t.Errorf("unexpected entry %q → %s", e.Title, l.Href)
			continue
		}
		if l.Type != "application/atom+xml;profile=opds-catalog;kind="+kind {
			t.Errorf("%s type = %q, want kind=%s", l.Href, l.Type, kind)
		}
		if e.Content == "" {
			t.Errorf("navigation entry %q has no content (Atom needs it without an alternate link)", e.Title)
		}
		delete(want, l.Href)
	}
	if len(want) != 0 {
		t.Errorf("missing navigation entries: %v", want)
	}
	if n, _ := f.link("http://opds-spec.org/sort/new"); n.Href != "" {
		t.Error("sort/new belongs on the New entry, not the feed")
	}
	// New also says what it is, for readers that sort by it; the others don't.
	for _, e := range f.Entries {
		l, ok := e.link("http://opds-spec.org/sort/new")
		if isNew := e.Title == "New"; ok != isNew || (ok && l.Href != "/opds/new") {
			t.Errorf("%q sort/new link = %+v (present %v)", e.Title, l, ok)
		}
	}
	var osd, tmpl bool
	for _, l := range f.Links {
		if l.Rel == "search" && l.Type == "application/opensearchdescription+xml" && l.Href == "/opds/opensearch.xml" {
			osd = true
		}
		if l.Rel == "search" && l.Type == "application/atom+xml" && strings.Contains(l.Href, "{searchTerms}") {
			tmpl = true
		}
	}
	if !osd || !tmpl {
		t.Errorf("root search links: description %v, template %v; links %+v", osd, tmpl, f.Links)
	}
	if _, ok := f.link("up"); ok {
		t.Error("the root has nowhere to go up to")
	}
	// The trailing-slash address is the same feed.
	if g := x.feed("/opds/", "navigation"); len(g.Entries) != len(f.Entries) {
		t.Errorf("/opds/ has %d entries, /opds %d", len(g.Entries), len(f.Entries))
	}
}

// New lists books with an ebook, newest first, each with cover and acquisition links;
// audiobook-only books and adult books are left out.
func TestNewFeedEntries(t *testing.T) {
	x := newHarness(t)
	dune := x.book("Dune", "Frank Herbert", "Dune.epub", func(w *metadata.BookResult) {
		w.CoverURL = "https://covers.example/dune.png"
		w.ReleaseDate = "1965-08-01"
	})
	if err := x.bs.SetSeries(x.ctx, dune.ID, "Dune Chronicles", 1); err != nil {
		t.Fatal(err)
	}
	x.book("Middlemarch", "George Eliot", "middlemarch.pdf", func(w *metadata.BookResult) { w.Year = 1871 })
	x.book("Project Hail Mary", "Andy Weir", "", nil) // no ebook
	audio := x.book("Recursion", "Blake Crouch", "", nil)
	dirA := filepath.Join(x.dir, "audio")
	_ = os.MkdirAll(dirA, 0o755)
	_ = os.WriteFile(filepath.Join(dirA, "a.mp3"), []byte("x"), 0o644)
	if err := x.bs.MarkImported(x.ctx, audio.ID, books.KindAudiobook, dirA, "MP3", 1, 1); err != nil {
		t.Fatal(err)
	}
	x.book("Brazzers Presents", "Someone", "b.epub", nil)             // adult by title
	night := x.book("Night Games", "Someone Else", "night.epub", nil) // adult by tag
	if _, err := x.db.ExecContext(x.ctx, `UPDATE books SET subjects_json = '["Fiction","Erotica"]' WHERE id = ?`, night.ID); err != nil {
		t.Fatal(err)
	}

	f := x.feed("/opds/new", "acquisition")
	if got := f.titles(); strings.Join(got, "|") != "Middlemarch|Dune" {
		t.Fatalf("New = %v, want Middlemarch then Dune (newest first, ebooks only, no adult titles)", got)
	}
	if f.Total != 2 {
		t.Errorf("totalResults = %d, want 2", f.Total)
	}
	d := f.Entries[1]
	if len(d.Authors) != 1 || d.Authors[0] != "Frank Herbert" || len(d.AuthURI) != 1 || !strings.HasPrefix(d.AuthURI[0], "/opds/authors/") {
		t.Errorf("Dune author = %v %v", d.Authors, d.AuthURI)
	}
	if d.Issued != "1965-08-01" {
		t.Errorf("Dune dc:issued = %q, want the release day", d.Issued)
	}
	if !strings.Contains(d.Summary, "Dune Chronicles #1") {
		t.Errorf("Dune summary %q doesn't name its series", d.Summary)
	}
	if f.Entries[0].Issued != "1871" {
		t.Errorf("Middlemarch dc:issued = %q, want the year", f.Entries[0].Issued)
	}
	acq, ok := d.link("http://opds-spec.org/acquisition")
	if !ok || acq.Href != fmt.Sprintf("/opds/books/%d/file", dune.ID) || acq.Type != "application/epub+zip" {
		t.Errorf("Dune acquisition = %+v", acq)
	}
	if acq, _ := f.Entries[0].link("http://opds-spec.org/acquisition"); acq.Type != "application/pdf" {
		t.Errorf("Middlemarch acquisition type = %q", acq.Type)
	}
	img, ok := d.link("http://opds-spec.org/image")
	if !ok || img.Href != fmt.Sprintf("/opds/books/%d/cover", dune.ID) || img.Type != "image/png" {
		t.Errorf("Dune image = %+v", img)
	}
	if _, ok := d.link("http://opds-spec.org/image/thumbnail"); !ok {
		t.Error("Dune has no thumbnail link")
	}
	if _, ok := f.Entries[0].link("http://opds-spec.org/image"); ok {
		t.Error("a book with no cover must not advertise one")
	}
	if _, ok := f.link("next"); ok {
		t.Error("one page needs no next link")
	}
}

// Fifty to a page, linked with next / previous / first / last.
func TestPagination(t *testing.T) {
	x := newHarness(t)
	for i := 1; i <= 55; i++ {
		x.book(fmt.Sprintf("Volume %02d", i), "Prolific Writer", "v.epub", nil)
	}
	p1 := x.feed("/opds/all", "acquisition")
	if len(p1.Entries) != 50 || p1.Total != 55 {
		t.Fatalf("page 1: %d entries of %d", len(p1.Entries), p1.Total)
	}
	if p1.Entries[0].Title != "Volume 01" {
		t.Errorf("All starts with %q, want A to Z", p1.Entries[0].Title)
	}
	next, ok := p1.link("next")
	if !ok || next.Href != "/opds/all?page=2" {
		t.Fatalf("page 1 next = %+v", next)
	}
	if _, ok := p1.link("previous"); ok {
		t.Error("page 1 has no previous page")
	}
	p2 := x.feed(next.Href, "acquisition")
	if len(p2.Entries) != 5 || p2.Entries[4].Title != "Volume 55" {
		t.Fatalf("page 2 = %v", p2.titles())
	}
	if _, ok := p2.link("next"); ok {
		t.Error("the last page has no next")
	}
	if prev, _ := p2.link("previous"); prev.Href != "/opds/all" {
		t.Errorf("page 2 previous = %q", prev.Href)
	}
	if self, _ := p2.link("self"); self.Href != "/opds/all?page=2" {
		t.Errorf("page 2 self = %q", self.Href)
	}
	// A page past the end shows the last one rather than nothing.
	if p9 := x.feed("/opds/all?page=9", "acquisition"); len(p9.Entries) != 5 {
		t.Errorf("page 9 has %d entries, want the last page's 5", len(p9.Entries))
	}
}

// Search matches every word against title, author and series, whole or as a prefix; title
// hits come first; the query is carried into the paging links.
func TestSearch(t *testing.T) {
	x := newHarness(t)
	x.book("Dune", "Frank Herbert", "d.epub", nil)
	x.book("Dune Messiah", "Frank Herbert", "dm.epub", nil)
	x.book("The Dosadi Experiment", "Frank Herbert", "do.epub", nil)
	x.book("Pokémon Adventures", "Hidenori Kusaka", "p.epub", nil)
	for _, c := range []struct{ q, want string }{
		{"dune", "Dune|Dune Messiah"},
		{"dun mess", "Dune Messiah"},
		{"herb dosadi", "The Dosadi Experiment"},
		{"pokemon", "Pokémon Adventures"},
		{"FRANK", "Dune|Dune Messiah|The Dosadi Experiment"},
		{"nothing-like-this", ""},
		{"", ""},
	} {
		f := x.feed("/opds/search?q="+url.QueryEscape(c.q), "acquisition")
		got := f.titles()
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("search %q = %v, want nothing", c.q, got)
			}
			continue
		}
		want := strings.Split(c.want, "|")
		if len(got) != len(want) {
			t.Errorf("search %q = %v, want %v", c.q, got, want)
			continue
		}
		set := map[string]bool{}
		for _, g := range got {
			set[g] = true
		}
		for _, w := range want {
			if !set[w] {
				t.Errorf("search %q = %v, missing %q", c.q, got, w)
			}
		}
	}
	// A title hit outranks an author-only hit.
	x.book("Herbert West", "H. P. Lovecraft", "hw.epub", nil)
	if got := x.feed("/opds/search?q=herbert", "acquisition").titles(); len(got) == 0 || got[0] != "Herbert West" {
		t.Errorf("search herbert = %v, want the title match first", got)
	}
	// The OpenSearch description points at the same search, absolutely.
	rec := x.do("/opds/opensearch.xml", "reader", "reader-pass-1")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/opensearchdescription+xml") {
		t.Fatalf("opensearch.xml: HTTP %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	var osd struct {
		XMLName xml.Name `xml:"http://a9.com/-/spec/opensearch/1.1/ OpenSearchDescription"`
		URLs    []struct {
			Type     string `xml:"type,attr"`
			Template string `xml:"template,attr"`
		} `xml:"http://a9.com/-/spec/opensearch/1.1/ Url"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &osd); err != nil || len(osd.URLs) == 0 {
		t.Fatalf("opensearch.xml doesn't parse: %v\n%s", err, rec.Body)
	}
	if u := osd.URLs[0]; u.Type != "application/atom+xml" || u.Template != "https://books.example/opds/search?q={searchTerms}" {
		t.Errorf("OpenSearch Url = %+v", u)
	}
	// Paging keeps the query.
	for i := 0; i < 52; i++ {
		x.book(fmt.Sprintf("Sandworm %02d", i), "Frank Herbert", "s.epub", nil)
	}
	f := x.feed("/opds/search?q=sandworm", "acquisition")
	if next, _ := f.link("next"); next.Href != "/opds/search?page=2&q=sandworm" {
		t.Errorf("search next = %q", next.Href)
	}
}

// Authors are grouped by author row; Series list series in reading order.
func TestAuthorsAndSeries(t *testing.T) {
	x := newHarness(t)
	b2 := x.book("Children of Dune", "Frank Herbert", "c.epub", func(w *metadata.BookResult) { w.Year = 1976 })
	b1 := x.book("Dune", "Frank Herbert", "d.epub", func(w *metadata.BookResult) { w.Year = 1965 })
	x.book("Emma", "Jane Austen", "e.epub", nil)
	x.book("Persuasion", "Jane Austen", "", nil) // no ebook: not counted
	if err := x.bs.SetSeries(x.ctx, b2.ID, "Dune Chronicles", 3); err != nil {
		t.Fatal(err)
	}
	if err := x.bs.SetSeries(x.ctx, b1.ID, "Dune Chronicles", 1); err != nil {
		t.Fatal(err)
	}

	authors := x.feed("/opds/authors", "navigation")
	if got := strings.Join(authors.titles(), "|"); got != "Frank Herbert|Jane Austen" {
		t.Fatalf("authors = %s", got)
	}
	frank := authors.Entries[0]
	if frank.Content != "2 books" || authors.Entries[1].Content != "1 book" {
		t.Errorf("author counts = %q, %q", frank.Content, authors.Entries[1].Content)
	}
	l := frank.Links[0]
	if l.Rel != "subsection" || !strings.HasPrefix(l.Type, "application/atom+xml;profile=opds-catalog;kind=acquisition") {
		t.Errorf("author link = %+v", l)
	}
	fb := x.feed(l.Href, "acquisition")
	if got := strings.Join(fb.titles(), "|"); got != "Dune|Children of Dune" {
		t.Errorf("Frank Herbert's books = %s, want series order", got)
	}
	if fb.Entries[0].AuthURI[0] != l.Href {
		t.Errorf("entry author uri %q, want the author's feed %q", fb.Entries[0].AuthURI[0], l.Href)
	}
	if b1.AuthorID > 0 && l.Href != fmt.Sprintf("/opds/authors/%d", b1.AuthorID) {
		t.Errorf("a linked author's feed is by its row id: %q, want %d", l.Href, b1.AuthorID)
	}

	series := x.feed("/opds/series", "navigation")
	if got := series.titles(); len(got) != 1 || got[0] != "Dune Chronicles" {
		t.Fatalf("series = %v", got)
	}
	sf := x.feed(series.Entries[0].Links[0].Href, "acquisition")
	if got := strings.Join(sf.titles(), "|"); got != "Dune|Children of Dune" {
		t.Errorf("series books = %s, want reading order", got)
	}
	for _, p := range []string{"/opds/authors/999999", "/opds/authors/nffff", "/opds/series/s0000"} {
		if rec := x.do(p, "reader", "reader-pass-1"); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: HTTP %d, want 404", p, rec.Code)
		}
	}
}

// An unlinked book (no author row, or an ambiguous one) is grouped by its author's name,
// and joins the author row of the same name when exactly one has it.
func TestAuthorGroupsFoldUnlinkedByName(t *testing.T) {
	lib := fakeLibrary{authors: []books.Author{{ID: 7, Name: "Ursula K. Le Guin"}, {ID: 8, Name: "Jo Walton"}, {ID: 9, Name: "Jo  Walton"}}}
	h := New(Deps{Library: lib})
	list := []books.Book{
		{ID: 1, Title: "A Wizard of Earthsea", Author: "Ursula K. Le Guin", AuthorID: 7},
		{ID: 2, Title: "The Dispossessed", Author: "Le Guin, Ursula K."}, // unlinked, same person
		{ID: 3, Title: "Among Others", Author: "Jo Walton"},              // two rows share the name
		{ID: 4, Title: "Tooth and Claw", Author: "Jo Walton"},
		{ID: 5, Title: "Anonymous", Author: ""},
	}
	groups := h.authorGroups(context.Background(), list)
	if len(groups) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	byName := map[string]*group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	if g := byName["Ursula K. Le Guin"]; g == nil || g.ID != "7" || len(g.Books) != 2 {
		t.Errorf("Le Guin = %+v, want row 7 with both books", g)
	}
	if g := byName["Jo Walton"]; g == nil || g.ID == "8" || g.ID == "9" || len(g.Books) != 2 || !strings.HasPrefix(g.ID, "n") {
		t.Errorf("Jo Walton = %+v, want a name group (two rows share the name)", g)
	}
}

type fakeLibrary struct {
	authors []books.Author
}

func (f fakeLibrary) List(context.Context) ([]books.Book, error) { return nil, nil }
func (f fakeLibrary) Get(context.Context, int64) (books.Book, error) {
	return books.Book{}, books.ErrNotFound
}
func (f fakeLibrary) ListAuthors(context.Context) ([]books.Author, error) { return f.authors, nil }
func (f fakeLibrary) KeyIndex(context.Context, []books.Book) map[string]books.Book {
	return nil
}

// My requests: books behind the caller's requests — linked by row, or by the catalogue
// key an older request named — that have an ebook.
func TestMine(t *testing.T) {
	x := newHarness(t)
	a := x.book("Dune", "Frank Herbert", "d.epub", nil)
	b := x.book("Emma", "Jane Austen", "e.epub", nil)
	c := x.book("Persuasion", "Jane Austen", "", nil)
	x.book("Middlemarch", "George Eliot", "m.epub", nil)
	x.ids = map[int64]bool{a.ID: true, c.ID: true}
	x.keys = map[string]bool{b.OLKey: true}
	got := x.feed("/opds/mine", "acquisition").titles()
	if strings.Join(got, "|") != "Emma|Dune" {
		t.Errorf("mine = %v, want Emma and Dune (Persuasion has no ebook, Middlemarch isn't theirs)", got)
	}
}

// The file comes with its media type and a "Title - Author.ext" attachment name; covers
// go through the cover hook; a book that isn't listed can't be fetched by id either.
func TestFileAndCover(t *testing.T) {
	x := newHarness(t)
	d := x.book("Dune", "Frank Herbert", "Dune.epub", func(w *metadata.BookResult) { w.CoverURL = "https://c.example/d.jpg" })
	m := x.book("Bröther's Keeper", "Ånna", "k.mobi", nil)
	none := x.book("Emma", "Jane Austen", "", nil)
	adult := x.book("Brazzers Presents", "Someone", "b.epub", nil)

	rec := x.do(fmt.Sprintf("/opds/books/%d/file", d.ID), "reader", "reader-pass-1")
	if rec.Code != 200 || rec.Body.String() != "ebook bytes of Dune" {
		t.Fatalf("file: HTTP %d %q", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/epub+zip" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="Dune - Frank Herbert.epub"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	rec = x.do(fmt.Sprintf("/opds/books/%d/file", m.ID), "reader", "reader-pass-1")
	if ct := rec.Header().Get("Content-Type"); rec.Code != 200 || ct != "application/x-mobipocket-ebook" {
		t.Errorf("mobi: HTTP %d %q", rec.Code, ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "filename*=utf-8''") {
		t.Errorf("non-ASCII name not RFC 2231 encoded: %q", cd)
	}
	for _, id := range []int64{none.ID, adult.ID, 999999} {
		for _, what := range []string{"file", "cover"} {
			if rec := x.do(fmt.Sprintf("/opds/books/%d/%s", id, what), "reader", "reader-pass-1"); rec.Code != http.StatusNotFound {
				t.Errorf("book %d %s: HTTP %d, want 404", id, what, rec.Code)
			}
		}
	}
	if rec := x.do("/opds/books/abc/file", "reader", "reader-pass-1"); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: HTTP %d", rec.Code)
	}
	// A range request (readers resume downloads) gets the part asked for.
	r := httptest.NewRequest("GET", fmt.Sprintf("/opds/books/%d/file", d.ID), nil)
	r.SetBasicAuth("reader", "reader-pass-1")
	r.Header.Set("Range", "bytes=0-4")
	rr := httptest.NewRecorder()
	x.h.ServeHTTP(rr, r)
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "ebook" {
		t.Errorf("range: HTTP %d %q", rr.Code, rr.Body)
	}

	rec = x.do(fmt.Sprintf("/opds/books/%d/cover", d.ID), "reader", "reader-pass-1")
	if rec.Code != 200 || len(x.covers) != 1 || x.covers[0] != d.ID {
		t.Errorf("cover: HTTP %d, hook calls %v", rec.Code, x.covers)
	}
	if rec := x.do(fmt.Sprintf("/opds/books/%d/cover", m.ID), "reader", "reader-pass-1"); rec.Code != http.StatusNotFound {
		t.Errorf("no cover: HTTP %d, want 404", rec.Code)
	}
}

// Sign-in: Basic with the username and the audiobook password, nothing else.
func TestBasicAuth(t *testing.T) {
	x := newHarness(t)
	x.user("viewer", auth.RoleReadonly, "viewer-pass-1")
	gone := x.user("gone", auth.RoleRequester, "gone-pass-1")
	if err := x.users.SetDisabled(x.ctx, gone.ID, true); err != nil {
		t.Fatal(err)
	}
	denied := x.user("denied", auth.RoleRequester, "denied-pass-1")
	if err := x.audio.SetAllowed(x.ctx, denied.ID, false); err != nil {
		t.Fatal(err)
	}
	x.user("nopass", auth.RoleRequester, "")
	x.user("boss", auth.RoleAdmin, "boss-pass-1")

	for _, c := range []struct {
		name, user, pass string
		want             int
		fails            bool
	}{
		{"no credentials", "", "", 401, false},
		{"wrong password", "reader", "nope-nope-nope", 401, true},
		{"the Arrmada password", "reader", "arrmada-reader", 401, true},
		{"unknown user", "stranger", "reader-pass-1", 401, true},
		{"no audiobook password set", "nopass", "arrmada-nopass", 401, true},
		{"audiobook password", "reader", "reader-pass-1", 200, false},
		{"username in other case", "READER", "reader-pass-1", 200, false},
		{"admin", "boss", "boss-pass-1", 200, false},
		{"read-only role", "viewer", "viewer-pass-1", 403, false},
		{"disabled user", "gone", "gone-pass-1", 403, false},
		{"switched off by an admin", "denied", "denied-pass-1", 403, false},
		{"disabled user, wrong password", "gone", "wrong-wrong", 401, true},
	} {
		before := x.throttle.failed
		rec := x.do("/opds", c.user, c.pass)
		if rec.Code != c.want {
			t.Errorf("%s: HTTP %d, want %d", c.name, rec.Code, c.want)
		}
		if c.want == 401 && !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), `Basic realm="Arrmada Books"`) {
			t.Errorf("%s: WWW-Authenticate = %q", c.name, rec.Header().Get("WWW-Authenticate"))
		}
		if got := x.throttle.failed > before; got != c.fails {
			t.Errorf("%s: counted as a failure = %v, want %v", c.name, got, c.fails)
		}
		if x.throttle.open != 0 {
			t.Errorf("%s: %d sign-in turns never given back", c.name, x.throttle.open)
		}
	}

	// Throttled: refused before the password is even checked.
	x.throttle.refuse = true
	before := x.throttle.failed
	rec := x.do("/opds", "reader", "reader-pass-1")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("throttled: HTTP %d Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if x.throttle.failed != before {
		t.Error("a throttled request counted as another failure")
	}
	x.throttle.refuse = false

	// Every route asks for the password, the file and cover included.
	b := x.book("Dune", "Frank Herbert", "d.epub", nil)
	for _, p := range []string{"/opds/new", "/opds/all", "/opds/mine", "/opds/authors", "/opds/series",
		"/opds/search?q=dune", "/opds/opensearch.xml", fmt.Sprintf("/opds/books/%d/file", b.ID), fmt.Sprintf("/opds/books/%d/cover", b.ID)} {
		if rec := x.do(p, "", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without credentials: HTTP %d, want 401", p, rec.Code)
		}
	}
}

// A changed password stops working at once, even though a good one is remembered for a
// while so fifty covers don't cost fifty bcrypt runs.
func TestRememberedPasswordFollowsChanges(t *testing.T) {
	x := newHarness(t)
	if rec := x.do("/opds", "reader", "reader-pass-1"); rec.Code != 200 {
		t.Fatalf("HTTP %d", rec.Code)
	}
	u, _ := x.users.UserByUsername(x.ctx, "reader")
	if err := x.audio.Accounts.SetPassword(x.ctx, u.ID, "reader-pass-2", false); err != nil {
		t.Fatal(err)
	}
	if rec := x.do("/opds", "reader", "reader-pass-1"); rec.Code != 401 {
		t.Errorf("old password after a change: HTTP %d, want 401", rec.Code)
	}
	if rec := x.do("/opds", "reader", "reader-pass-2"); rec.Code != 200 {
		t.Errorf("new password: HTTP %d", rec.Code)
	}
	if err := x.users.SetDisabled(x.ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if rec := x.do("/opds", "reader", "reader-pass-2"); rec.Code != 403 {
		t.Errorf("remembered password of a now-disabled user: HTTP %d, want 403", rec.Code)
	}
	if err := x.users.SetDisabled(x.ctx, u.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := x.audio.Accounts.RemovePassword(x.ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if rec := x.do("/opds", "reader", "reader-pass-2"); rec.Code != 401 {
		t.Errorf("removed password: HTTP %d, want 401", rec.Code)
	}
}

// With the Books module off there's no catalogue: 404, with or without a password.
func TestModuleOff(t *testing.T) {
	x := newHarness(t)
	x.enabled = false
	for _, p := range []string{"/opds", "/opds/new", "/opds/books/1/file", "/opds/whatever"} {
		for _, creds := range [][2]string{{"", ""}, {"reader", "reader-pass-1"}} {
			if rec := x.do(p, creds[0], creds[1]); rec.Code != http.StatusNotFound {
				t.Errorf("GET %s (%v) with Books off: HTTP %d, want 404", p, creds[0] != "", rec.Code)
			}
		}
	}
	if x.throttle.failed+x.throttle.succeeded != 0 {
		t.Error("a switched-off catalogue still checked passwords")
	}
	x.enabled = true
	if rec := x.do("/opds/whatever", "reader", "reader-pass-1"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown /opds path: HTTP %d, want 404", rec.Code)
	}
}
