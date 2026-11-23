package opds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tristenlammi/arrmada/internal/adultfilter"
	"github.com/tristenlammi/arrmada/internal/attachment"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/kepub"
	"github.com/tristenlammi/arrmada/internal/parser"
)

// The feeds. Every list is worked out from the library on each request: a home library
// is a few thousand books at most, and readers fetch a page at a time.

const (
	navType  = "application/atom+xml;profile=opds-catalog;kind=navigation"
	acqType  = "application/atom+xml;profile=opds-catalog;kind=acquisition"
	osdType  = "application/opensearchdescription+xml"
	atomType = "application/atom+xml"

	relAcquisition = "http://opds-spec.org/acquisition"
	relImage       = "http://opds-spec.org/image"
	relThumbnail   = "http://opds-spec.org/image/thumbnail"
	relNew         = "http://opds-spec.org/sort/new"

	nsAtom       = "http://www.w3.org/2005/Atom"
	nsDC         = "http://purl.org/dc/terms/"
	nsOPDS       = "http://opds-spec.org/2010/catalog"
	nsOpenSearch = "http://a9.com/-/spec/opensearch/1.1/"

	// pageSize is how many entries one feed page holds; rel=next links the rest.
	pageSize = 50
	// maxQuery bounds a search term; nobody types more into an e-reader.
	maxQuery = 200
)

// --- Atom, as encoding/xml writes it -----------------------------------------------
//
// Prefixed names ("dc:issued") are written literally, with the prefixes declared on the
// feed element, which is how every OPDS server lays them out.

type feed struct {
	XMLName      xml.Name `xml:"feed"`
	NS           string   `xml:"xmlns,attr"`
	NSDC         string   `xml:"xmlns:dc,attr"`
	NSOPDS       string   `xml:"xmlns:opds,attr"`
	NSOpenSearch string   `xml:"xmlns:opensearch,attr"`
	ID           string   `xml:"id"`
	Title        string   `xml:"title"`
	Updated      string   `xml:"updated"`
	Author       person   `xml:"author"`
	Total        *int     `xml:"opensearch:totalResults,omitempty"`
	PerPage      *int     `xml:"opensearch:itemsPerPage,omitempty"`
	Start        *int     `xml:"opensearch:startIndex,omitempty"`
	Links        []link   `xml:"link"`
	Entries      []entry  `xml:"entry"`
}

type person struct {
	Name string `xml:"name"`
	URI  string `xml:"uri,omitempty"`
}

type link struct {
	Rel   string `xml:"rel,attr,omitempty"`
	Href  string `xml:"href,attr"`
	Type  string `xml:"type,attr,omitempty"`
	Title string `xml:"title,attr,omitempty"`
}

type text struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

type category struct {
	Term  string `xml:"term,attr"`
	Label string `xml:"label,attr"`
}

type entry struct {
	Title      string     `xml:"title"`
	ID         string     `xml:"id"`
	Updated    string     `xml:"updated"`
	Authors    []person   `xml:"author,omitempty"`
	Issued     string     `xml:"dc:issued,omitempty"`
	Language   string     `xml:"dc:language,omitempty"`
	Publisher  string     `xml:"dc:publisher,omitempty"`
	Identifier string     `xml:"dc:identifier,omitempty"`
	Categories []category `xml:"category,omitempty"`
	Summary    *text      `xml:"summary,omitempty"`
	Content    *text      `xml:"content,omitempty"`
	Links      []link     `xml:"link"`
}

func newFeed(id, title string, now time.Time) *feed {
	return &feed{NS: nsAtom, NSDC: nsDC, NSOPDS: nsOPDS, NSOpenSearch: nsOpenSearch,
		ID: "urn:arrmada:opds:" + id, Title: title, Updated: stamp(now), Author: person{Name: "Arrmada"}}
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// write sends a feed. kind is navType or acqType.
func (h *Handler) write(w http.ResponseWriter, f *feed, kind string) {
	out, err := xml.MarshalIndent(f, "", " ")
	if err != nil {
		h.d.Log.Warn("opds: could not write a feed", "err", err)
		http.Error(w, "Couldn't build the catalogue.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", kind+";charset=utf-8")
	// Feeds are per person (My requests) and change as books arrive: never cached by a
	// proxy, and re-read by the reader each time.
	w.Header().Set("Cache-Control", "private, no-cache")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}

// navLinks are the links every feed carries: home, search, and itself.
func navLinks(self, kind string) []link {
	return []link{
		{Rel: "self", Href: self, Type: kind},
		{Rel: "start", Href: "/opds", Type: navType, Title: "Arrmada Books"},
		{Rel: "up", Href: "/opds", Type: navType},
		{Rel: "search", Href: "/opds/opensearch.xml", Type: osdType, Title: "Search"},
		{Rel: "search", Href: "/opds/search?q={searchTerms}", Type: atomType, Title: "Search"},
	}
}

// --- the library, as the catalogue sees it ---------------------------------------------

// visible: a library book with an ebook that the adult filter lets through. The filter
// is always on here, as on every surface a requester sees.
func visible(b books.Book) bool {
	return b.Ebook != nil && b.Ebook.Path != "" && !adultfilter.BookIsAdult(b.Title, b.Subjects)
}

// shelf is every visible book, newest first (the order the library lists them in).
func (h *Handler) shelf(ctx context.Context) ([]books.Book, error) {
	all, err := h.d.Library.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]books.Book, 0, len(all))
	for _, b := range all {
		if visible(b) {
			out = append(out, b)
		}
	}
	return out, nil
}

// group is one author or one series: an opaque id for its feed's address, the name
// shown, and its books.
type group struct {
	ID    string
	Name  string
	Books []books.Book
}

// groupID makes a feed id from a grouping key: short, URL-safe, and the same on every
// request. A hash rather than the name, so an address never carries one.
func groupID(prefix, key string) string {
	sum := sha256.Sum256([]byte(key))
	return prefix + hex.EncodeToString(sum[:8])
}

// authorGroups groups books by author: by author row (BOOK-19) when the book is linked
// to one, by the normalised name otherwise. An unlinked book joins a linked author of
// the same name when exactly one has it, so one person doesn't show up twice. Books
// with no author at all aren't listed here (they are in New, All and Search).
func (h *Handler) authorGroups(ctx context.Context, list []books.Book) []*group {
	rows, _ := h.d.Library.ListAuthors(ctx) // a failure only loses the row names
	rowName := map[int64]string{}
	byKey := map[string][]int64{}
	for _, a := range rows {
		rowName[a.ID] = a.Name
		seen := map[string]bool{}
		for _, n := range append([]string{a.Name}, a.Names...) {
			if k := books.AuthorKey(n); k != "" && !seen[k] {
				seen[k] = true
				byKey[k] = append(byKey[k], a.ID)
			}
		}
	}
	groups := map[string]*group{}
	var order []*group
	add := func(id, name string, b books.Book) {
		g := groups[id]
		if g == nil {
			g = &group{ID: id, Name: name}
			groups[id] = g
			order = append(order, g)
		}
		g.Books = append(g.Books, b)
	}
	for _, b := range list {
		name := strings.TrimSpace(b.Author)
		switch {
		case b.AuthorID > 0:
			n := rowName[b.AuthorID]
			if n == "" {
				n = name
			}
			add(strconv.FormatInt(b.AuthorID, 10), n, b)
		case name == "":
		default:
			key := books.AuthorKey(name)
			if ids := byKey[key]; len(ids) == 1 {
				n := rowName[ids[0]]
				add(strconv.FormatInt(ids[0], 10), n, b)
				continue
			}
			add(groupID("n", key), name, b)
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return lessFold(order[i].Name, order[j].Name) })
	for _, g := range order {
		sortReading(g.Books)
	}
	return order
}

// seriesGroups groups books by series name (folded, so spellings agree), each series in
// reading order.
func seriesGroups(list []books.Book) []*group {
	groups := map[string]*group{}
	var order []*group
	for _, b := range list {
		key := books.NormKey(b.SeriesName)
		if key == "" {
			continue
		}
		g := groups[key]
		if g == nil {
			g = &group{ID: groupID("s", key), Name: strings.TrimSpace(b.SeriesName)}
			groups[key] = g
			order = append(order, g)
		}
		g.Books = append(g.Books, b)
	}
	sort.SliceStable(order, func(i, j int) bool { return lessFold(order[i].Name, order[j].Name) })
	for _, g := range order {
		sort.SliceStable(g.Books, func(i, j int) bool {
			pi, pj := g.Books[i].SeriesPosition, g.Books[j].SeriesPosition
			if (pi > 0) != (pj > 0) {
				return pi > 0 // numbered books first
			}
			if pi != pj {
				return pi < pj
			}
			return lessFold(g.Books[i].Title, g.Books[j].Title)
		})
	}
	return order
}

// sortReading orders an author's books: series together in order, then by when they came
// out, then by title.
func sortReading(list []books.Book) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if sa, sb := books.NormKey(a.SeriesName), books.NormKey(b.SeriesName); sa != sb {
			if sa == "" || sb == "" {
				return sb == "" // series first, standalones after
			}
			return sa < sb
		} else if sa != "" && a.SeriesPosition != b.SeriesPosition {
			return a.SeriesPosition < b.SeriesPosition
		}
		if da, db := issued(a), issued(b); da != db {
			return da < db
		}
		return lessFold(a.Title, b.Title)
	})
}

func lessFold(a, b string) bool {
	fa, fb := parser.FoldTitle(a), parser.FoldTitle(b)
	if fa != fb {
		return fa < fb
	}
	return a < b
}

// issued is when the book came out, as dc:issued carries it: the release day when the
// catalogue gave one, else the year.
func issued(b books.Book) string {
	if b.ReleaseDate != "" {
		return b.ReleaseDate
	}
	if b.Year > 0 {
		return fmt.Sprintf("%04d", b.Year)
	}
	return ""
}

// --- entries -------------------------------------------------------------------------

// bookEntry is one book in an acquisition feed: what it is, its cover, and where to
// download it. authorHref is its author's feed ("" when it has none). withKepub adds a second
// download, a KEPUB for Kobo's stock reader, to an EPUB; readers that don't know the type
// (KOReader, Moon+) keep taking the EPUB.
func bookEntry(b books.Book, authorHref string, now time.Time, withKepub bool) entry {
	e := entry{
		Title:   b.Title,
		ID:      fmt.Sprintf("urn:arrmada:book:%d", b.ID),
		Updated: stamp(updatedAt(b, now)),
		Issued:  issued(b),
		// Details edited by hand on the book page (language is a canonical code, which
		// reads as the BCP 47 tag dc:language wants).
		Language:  b.Language,
		Publisher: b.Publisher,
	}
	if b.ISBN != "" {
		e.Identifier = "urn:isbn:" + b.ISBN
	}
	if b.Author != "" {
		e.Authors = []person{{Name: b.Author, URI: authorHref}}
	}
	for i, s := range b.Subjects {
		if i == 5 {
			break
		}
		e.Categories = append(e.Categories, category{Term: s, Label: s})
	}
	var about []string
	if b.SeriesName != "" {
		s := "Series: " + b.SeriesName
		if b.SeriesPosition > 0 {
			s += " #" + strconv.FormatFloat(b.SeriesPosition, 'f', -1, 64)
		}
		about = append(about, s)
	}
	if d := strings.TrimSpace(b.Description); d != "" {
		about = append(about, clip(d, 4000))
	}
	if len(about) > 0 {
		e.Summary = &text{Type: "text", Body: strings.Join(about, "\n\n")}
	}
	base := fmt.Sprintf("/opds/books/%d", b.ID)
	if b.CoverURL != "" {
		ct := coverType(b.CoverURL)
		e.Links = append(e.Links,
			link{Rel: relImage, Href: base + "/cover", Type: ct},
			link{Rel: relThumbnail, Href: base + "/cover", Type: ct})
	}
	ext := ebookExt(b)
	e.Links = append(e.Links, link{Rel: relAcquisition, Href: base + "/file", Type: contentType(ext),
		Title: strings.ToUpper(strings.TrimPrefix(ext, "."))})
	if withKepub && ext == ".epub" {
		e.Links = append(e.Links, link{Rel: relAcquisition, Href: base + "/kepub", Type: kepub.ContentType, Title: "KEPUB (Kobo)"})
	}
	return e
}

// isKobo reports whether a request comes from Kobo's own software, which reads a KEPUB
// best. KOReader running on a Kobo names the device in its User-Agent too, but renders
// EPUB itself, so it keeps getting the EPUB.
func isKobo(ua string) bool {
	ua = strings.ToLower(ua)
	return strings.Contains(ua, "kobo") && !strings.Contains(ua, "koreader")
}

// navEntry is one row of a navigation feed: a link to another feed.
// rel, when set, is a second, more specific relation (sort/new) after "subsection":
// readers that only follow subsection still open it.
func navEntry(id, title, about, href, kind, rel string, now time.Time) entry {
	links := []link{{Rel: "subsection", Href: href, Type: kind}}
	if rel != "" {
		links = append(links, link{Rel: rel, Href: href, Type: kind})
	}
	return entry{Title: title, ID: "urn:arrmada:opds:" + id, Updated: stamp(now),
		Content: &text{Type: "text", Body: about}, Links: links}
}

// updatedAt is when the book's entry last changed: its details (an edit, a refresh, a new
// cover — meta_updated_at) or, before any of that, when it was added. Readers that cache
// a catalogue see a newer <updated> and take the new details.
func updatedAt(b books.Book, now time.Time) time.Time {
	t := addedAt(b, now)
	if b.MetaUpdatedAt > 0 {
		if m := time.UnixMilli(b.MetaUpdatedAt).UTC(); m.After(t) {
			return m
		}
	}
	return t
}

func addedAt(b books.Book, now time.Time) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, b.AddedAt); err == nil {
			return t
		}
	}
	return now
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}

// ebookExt is the extension of the file a download would send (lower case, with the
// dot), from the file on disk when it can be read, else from the recorded format.
func ebookExt(b books.Book) string {
	if p, err := books.EbookFile(b.Ebook.Path); err == nil {
		if ext := strings.ToLower(filepath.Ext(p)); ext != "" {
			return ext
		}
	}
	if b.Ebook.Format != "" {
		return "." + strings.ToLower(b.Ebook.Format)
	}
	return ""
}

// contentType is the media type a reader is told for an ebook: the shared table first,
// then the comic and other formats Arrmada imports that the shelf's download doesn't
// name.
func contentType(ext string) string {
	if ct := books.EbookContentType(ext); ct != "" {
		return ct
	}
	switch ext {
	case ".cbz":
		return "application/vnd.comicbook+zip"
	case ".cbr":
		return "application/vnd.comicbook-rar"
	case ".fb2":
		return "application/x-fictionbook+xml"
	case ".djvu":
		return "image/vnd.djvu"
	}
	return "application/octet-stream"
}

// coverType guesses a cover's image type from its address; readers only use it to
// decide whether a link is an image.
func coverType(u string) string {
	if p, err := url.Parse(u); err == nil {
		u = p.Path
	}
	switch strings.ToLower(filepath.Ext(u)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	}
	return "image/jpeg"
}

// --- paging -----------------------------------------------------------------------------

// pageOf is the ?page= asked for (1-based; anything unreadable is the first page).
func pageOf(r *http.Request) int {
	p, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

// paged cuts one page out of n items and adds the paging links and OpenSearch counts to
// f. path is the feed's address; extra its other query values (a search's q).
func paged(f *feed, n, page int, path string, extra url.Values, kind string) (lo, hi int) {
	last := (n + pageSize - 1) / pageSize
	if last < 1 {
		last = 1
	}
	if page > last {
		page = last
	}
	lo = (page - 1) * pageSize
	hi = min(lo+pageSize, n)
	href := func(p int) string {
		q := url.Values{}
		for k, v := range extra {
			q[k] = v
		}
		if p > 1 {
			q.Set("page", strconv.Itoa(p))
		}
		if len(q) == 0 {
			return path
		}
		return path + "?" + q.Encode()
	}
	f.Links = navLinks(href(page), kind)
	if last > 1 {
		f.Links = append(f.Links, link{Rel: "first", Href: href(1), Type: kind})
		if page > 1 {
			f.Links = append(f.Links, link{Rel: "previous", Href: href(page - 1), Type: kind})
		}
		if page < last {
			f.Links = append(f.Links, link{Rel: "next", Href: href(page + 1), Type: kind})
		}
		f.Links = append(f.Links, link{Rel: "last", Href: href(last), Type: kind})
	}
	total, per, start := n, pageSize, lo+1
	f.Total, f.PerPage, f.Start = &total, &per, &start
	return lo, hi
}

// --- handlers ---------------------------------------------------------------------------

func (h *Handler) root(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	now := h.d.Now()
	f := newFeed("root", "Arrmada Books", now)
	// The root is the start, so it has no "up".
	for _, l := range navLinks("/opds", navType) {
		if l.Rel != "up" {
			f.Links = append(f.Links, l)
		}
	}
	f.Entries = []entry{
		navEntry("new", "New", "The newest ebooks in the library.", "/opds/new", acqType, relNew, now),
		navEntry("mine", "My requests", "Books you asked for that have arrived.", "/opds/mine", acqType, "", now),
		navEntry("authors", "Authors", "Every author, A to Z.", "/opds/authors", navType, "", now),
		navEntry("series", "Series", "Every series, in reading order.", "/opds/series", navType, "", now),
		navEntry("all", "All books", "The whole library, A to Z.", "/opds/all", acqType, "", now),
	}
	h.write(w, f, navType)
}

// openSearch describes the search for readers that read the description first. The
// template must be absolute, so it's built from the address the reader used.
func (h *Handler) openSearch(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	base := ""
	if h.d.BaseURL != nil {
		base = strings.TrimRight(h.d.BaseURL(r), "/")
	}
	type osURL struct {
		Type     string `xml:"type,attr"`
		Template string `xml:"template,attr"`
	}
	doc := struct {
		XMLName       xml.Name `xml:"OpenSearchDescription"`
		NS            string   `xml:"xmlns,attr"`
		ShortName     string   `xml:"ShortName"`
		Description   string   `xml:"Description"`
		InputEncoding string   `xml:"InputEncoding"`
		URLs          []osURL  `xml:"Url"`
	}{NS: nsOpenSearch, ShortName: "Arrmada Books", Description: "Search the library by title, author or series.",
		InputEncoding: "UTF-8", URLs: []osURL{
			{Type: atomType, Template: base + "/opds/search?q={searchTerms}"},
			{Type: acqType, Template: base + "/opds/search?q={searchTerms}"},
		}}
	out, err := xml.MarshalIndent(doc, "", " ")
	if err != nil {
		http.Error(w, "Couldn't build the search description.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", osdType+";charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-cache")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}

// acquisition writes a paged feed of books.
func (h *Handler) acquisition(w http.ResponseWriter, r *http.Request, id, title, path string, extra url.Values, list []books.Book) {
	ctx := r.Context()
	now := h.d.Now()
	f := newFeed(id, title, now)
	lo, hi := paged(f, len(list), pageOf(r), path, extra, acqType)
	authorHref := h.authorHrefs(ctx, list[lo:hi])
	withKepub := h.kepubReady(ctx)
	for _, b := range list[lo:hi] {
		f.Entries = append(f.Entries, bookEntry(b, authorHref[b.ID], now, withKepub))
	}
	h.write(w, f, acqType)
}

// authorHrefs is each book's author feed, by the same grouping the Authors feed uses.
func (h *Handler) authorHrefs(ctx context.Context, list []books.Book) map[int64]string {
	out := map[int64]string{}
	for _, g := range h.authorGroups(ctx, list) {
		for _, b := range g.Books {
			out[b.ID] = "/opds/authors/" + g.ID
		}
	}
	return out
}

func (h *Handler) loadShelf(w http.ResponseWriter, r *http.Request) ([]books.Book, bool) {
	list, err := h.shelf(r.Context())
	if err != nil {
		h.d.Log.Warn("opds: could not read the library", "err", err)
		http.Error(w, "Couldn't read the library.", http.StatusInternalServerError)
		return nil, false
	}
	return list, true
}

func (h *Handler) newest(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	if list, ok := h.loadShelf(w, r); ok {
		h.acquisition(w, r, "new", "New", "/opds/new", nil, list)
	}
}

func (h *Handler) all(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	list, ok := h.loadShelf(w, r)
	if !ok {
		return
	}
	sort.SliceStable(list, func(i, j int) bool { return lessFold(list[i].Title, list[j].Title) })
	h.acquisition(w, r, "all", "All books", "/opds/all", nil, list)
}

// mine is the books behind the caller's own requests: linked by library row, or by the
// catalogue key an older request was made under (current or former).
func (h *Handler) mine(w http.ResponseWriter, r *http.Request, u *auth.User) {
	ctx := r.Context()
	var ids map[int64]bool
	var keys map[string]bool
	if h.d.Requested != nil {
		var err error
		if ids, keys, err = h.d.Requested(ctx, u.ID); err != nil {
			h.d.Log.Warn("opds: could not read requests", "err", err)
			http.Error(w, "Couldn't read your requests.", http.StatusInternalServerError)
			return
		}
	}
	all, err := h.d.Library.List(ctx)
	if err != nil {
		h.d.Log.Warn("opds: could not read the library", "err", err)
		http.Error(w, "Couldn't read the library.", http.StatusInternalServerError)
		return
	}
	if len(keys) > 0 {
		for k, b := range h.d.Library.KeyIndex(ctx, all) {
			if keys[k] {
				if ids == nil {
					ids = map[int64]bool{}
				}
				ids[b.ID] = true
			}
		}
	}
	var list []books.Book
	for _, b := range all {
		if ids[b.ID] && visible(b) {
			list = append(list, b)
		}
	}
	h.acquisition(w, r, "mine", "My requests", "/opds/mine", nil, list)
}

func (h *Handler) authors(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	list, ok := h.loadShelf(w, r)
	if !ok {
		return
	}
	h.groupList(w, r, "authors", "Authors", "/opds/authors", h.authorGroups(r.Context(), list))
}

func (h *Handler) seriesList(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	list, ok := h.loadShelf(w, r)
	if !ok {
		return
	}
	h.groupList(w, r, "series", "Series", "/opds/series", seriesGroups(list))
}

// groupList is a paged navigation feed of authors or series.
func (h *Handler) groupList(w http.ResponseWriter, r *http.Request, id, title, path string, groups []*group) {
	now := h.d.Now()
	f := newFeed(id, title, now)
	lo, hi := paged(f, len(groups), pageOf(r), path, nil, navType)
	for _, g := range groups[lo:hi] {
		n := len(g.Books)
		about := fmt.Sprintf("%d books", n)
		if n == 1 {
			about = "1 book"
		}
		f.Entries = append(f.Entries, navEntry(id+":"+g.ID, g.Name, about, path+"/"+g.ID, acqType, "", now))
	}
	h.write(w, f, navType)
}

func (h *Handler) author(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	list, ok := h.loadShelf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	for _, g := range h.authorGroups(r.Context(), list) {
		if g.ID == id {
			h.acquisition(w, r, "authors:"+g.ID, g.Name, "/opds/authors/"+g.ID, nil, g.Books)
			return
		}
	}
	http.NotFound(w, r)
}

func (h *Handler) series(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	list, ok := h.loadShelf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	for _, g := range seriesGroups(list) {
		if g.ID == id {
			h.acquisition(w, r, "series:"+g.ID, g.Name, "/opds/series/"+g.ID, nil, g.Books)
			return
		}
	}
	http.NotFound(w, r)
}

// search finds books whose title, author or series has every word typed (each word
// whole or as the start of one, so "dun herb" finds Dune). Title hits come first.
func (h *Handler) search(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	q := r.URL.Query().Get("q")
	if utf8.RuneCountInString(q) > maxQuery {
		q = string([]rune(q)[:maxQuery])
	}
	list, ok := h.loadShelf(w, r)
	if !ok {
		return
	}
	words := parser.TitleWords(q)
	type hit struct {
		b     books.Book
		title bool
	}
	var hits []hit
	if len(words) > 0 {
		for _, b := range list {
			title := parser.TitleWords(b.Title)
			if matchesAll(words, title) {
				hits = append(hits, hit{b, true})
			} else if matchesAll(words, parser.TitleWords(b.Title+" "+b.Author+" "+b.SeriesName)) {
				hits = append(hits, hit{b, false})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].title && !hits[j].title })
	found := make([]books.Book, len(hits))
	for i, x := range hits {
		found[i] = x.b
	}
	h.acquisition(w, r, "search", "Search", "/opds/search", url.Values{"q": {q}}, found)
}

// matchesAll reports whether every query word is a word of hay or the start of one.
func matchesAll(words, hay []string) bool {
	for _, w := range words {
		ok := false
		for _, x := range hay {
			if strings.HasPrefix(x, w) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// book reads the {id} book for the file and cover routes: only a book the catalogue
// lists can be fetched, so an id guessed past the adult filter is a 404 too.
func (h *Handler) book(w http.ResponseWriter, r *http.Request) (books.Book, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return books.Book{}, false
	}
	b, err := h.d.Library.Get(r.Context(), id)
	if err != nil || !visible(b) {
		http.NotFound(w, r)
		return books.Book{}, false
	}
	return b, true
}

// file sends the ebook, named "Title - Author.epub", with its media type. Nothing about
// the download is logged: who read what is nobody's business. Kobo's own software is
// sent an EPUB as a KEPUB (isKobo), while the setting is on.
func (h *Handler) file(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	h.sendFile(w, r, isKobo(r.UserAgent()))
}

// kepubFile is the KEPUB download the feeds offer beside an EPUB. With the setting
// switched off since the feed was read, or a book that isn't an EPUB, it sends the file
// as it is rather than fail the reader's download.
func (h *Handler) kepubFile(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	h.sendFile(w, r, true)
}

func (h *Handler) kepubReady(ctx context.Context) bool {
	return h.d.Kepub != nil && h.d.Kepub.Ready(ctx)
}

func (h *Handler) sendFile(w http.ResponseWriter, r *http.Request, wantKepub bool) {
	b, ok := h.book(w, r)
	if !ok {
		return
	}
	path, err := books.EbookFile(b.Ebook.Path)
	if err != nil {
		http.Error(w, "The ebook file is missing on the server.", http.StatusNotFound)
		return
	}
	if wantKepub && kepub.IsEPUB(path) && h.kepubReady(r.Context()) {
		kf, err := h.d.Kepub.Open(r.Context(), b.ID, path)
		if err == nil {
			defer kf.Close()
			if st, err := kf.Stat(); err == nil {
				w.Header().Set("Content-Type", kepub.ContentType)
				w.Header().Set("Content-Disposition", attachment.Header(books.EbookDownloadName(b, kepub.Ext)))
				w.Header().Set("Cache-Control", "private, no-store")
				http.ServeContent(w, r, "", st.ModTime(), kf)
				return
			}
		} else if kepub.Failed(err) && r.Context().Err() == nil {
			// No book and no person in the line: only that a conversion failed and why.
			h.d.Log.Warn("opds: couldn't make a KEPUB, sending the EPUB", "err", err)
		}
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "The ebook file is missing on the server.", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.Error(w, "The ebook file is missing on the server.", http.StatusNotFound)
		return
	}
	ext := strings.ToLower(filepath.Ext(path))
	w.Header().Set("Content-Type", contentType(ext))
	w.Header().Set("Content-Disposition", attachment.Header(books.EbookDownloadName(b, ext)))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (h *Handler) cover(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	b, ok := h.book(w, r)
	if !ok {
		return
	}
	if h.d.Cover == nil || b.CoverURL == "" {
		http.NotFound(w, r)
		return
	}
	h.d.Cover(w, r, b)
}
