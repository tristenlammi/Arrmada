package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Hardcover is the Books module's preferred metadata source once the user has pasted an
// API key in Settings. Its catalogue is curated around one canonical book with editions
// underneath it — the shape Open Library lacks, where the same novel turns up as three
// "works" and each one looked like a new book to add. Free for personal use (5,000
// requests a day), key from hardcover.app → Settings → Hardcover API.
//
// Keys are "hc:<book id>" and authors "hc:a:<author id>", so the switch (bookswitch.go)
// can route a stored key back to the catalogue that issued it.
const (
	hardcoverAPI       = "https://api.hardcover.app/v1/graphql"
	hcKeyPrefix        = "hc:"
	hcAuthorPrefix     = "hc:a:"
	hardcoverSearchMax = 24
)

type Hardcover struct {
	key      func() string
	endpoint string
	http     *http.Client
	// extraCovers supplies the Google Books covers the picker also offers; the Open
	// Library provider already knows how (with an empty key it only asks Google).
	extraCovers *OpenLibrary
	budget      *hcBudget // daily request count + spacing (hccache.go)
	cache       *hcCache  // TTL cache over every read
}

// NewHardcoverFunc builds a provider that reads the key lazily, so a key pasted in
// Settings takes effect without a restart.
func NewHardcoverFunc(key func() string, extraCovers *OpenLibrary) *Hardcover {
	return &Hardcover{key: key, endpoint: hardcoverAPI, http: &http.Client{Timeout: 25 * time.Second}, extraCovers: extraCovers,
		budget: newHCBudget(), cache: newHCCache()}
}

// SetDiskCache keeps answers across restarts (see DiskCache).
func (h *Hardcover) SetDiskCache(c *DiskCache) { h.cache.disk = c }

// Usage reports today's request count against the daily budget.
func (h *Hardcover) Usage() (used, budget int) {
	if h == nil || h.budget == nil {
		return 0, hcDailyBudget
	}
	return h.budget.usage()
}

// Available is "a key is configured" — Hardcover does nothing without one.
func (h *Hardcover) Available() bool { return h != nil && strings.TrimSpace(h.key()) != "" }

// IsHardcoverKey reports whether a stored book key came from Hardcover.
func IsHardcoverKey(key string) bool { return strings.HasPrefix(key, hcKeyPrefix) }

// hcBookID parses "hc:123" → 123.
func hcBookID(key string) (int, bool) {
	if strings.HasPrefix(key, hcAuthorPrefix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(key, hcKeyPrefix))
	return n, err == nil && n > 0
}

func hcAuthorID(key string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(key, hcAuthorPrefix))
	return n, err == nil && n > 0
}

// query posts one GraphQL document and decodes data into out. Every call counts
// against the daily budget; past it the call is refused so a fallback can answer. A
// 429 is waited out (Retry-After, else a fixed pause) and retried twice before it's
// an error — the pacing should make it rare, but a burst from elsewhere can still
// trip the shared limiter.
func (h *Hardcover) query(ctx context.Context, q string, vars map[string]any, out any) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		var wait time.Duration
		wait, err = h.queryOnce(ctx, q, vars, out)
		if wait == 0 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return err
}

// queryOnce makes one request. A non-zero wait means "rate limited; try again after".
func (h *Hardcover) queryOnce(ctx context.Context, q string, vars map[string]any, out any) (time.Duration, error) {
	if h.budget != nil && !h.budget.take() {
		return 0, ErrHardcoverBudget
	}
	body, err := json.Marshal(map[string]any{"query": q, "variables": vars})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	// Hardcover's settings page hands out the token already prefixed with "Bearer ";
	// accept it either way.
	token := strings.TrimSpace(h.key())
	if !strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = "Bearer " + token
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Arrmada (self-hosted media manager)")
	resp, err := h.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("hardcover: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return 0, fmt.Errorf("hardcover: the API key was rejected (HTTP %d) — it may have expired", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		wait := hcRetryAfter
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && secs > 0 && secs <= 120 {
				wait = time.Duration(secs) * time.Second
			}
		}
		return wait, fmt.Errorf("hardcover: rate limited — try again in a minute")
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("hardcover: HTTP %d", resp.StatusCode)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, fmt.Errorf("hardcover: parse: %w", err)
	}
	if len(env.Errors) > 0 {
		return 0, fmt.Errorf("hardcover: %s", env.Errors[0].Message)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return 0, fmt.Errorf("hardcover: parse data: %w", err)
		}
	}
	return 0, nil
}

// --- the book shape shared by list queries ---

const hcBookFields = `id title release_year canonical_id image { url } contributions { author { id name } } rating ratings_count users_count cached_tags`

type hcBook struct {
	ID            int       `json:"id"`
	Title         string    `json:"title"`
	Subtitle      string    `json:"subtitle"`
	Description   string    `json:"description"`
	ReleaseYear   *int      `json:"release_year"`
	CanonicalID   *int      `json:"canonical_id"`
	Image         *hcImage  `json:"image"`
	Images        []hcImage `json:"images"`
	Contributions []struct {
		Contribution string `json:"contribution"` // role: empty for the author, "Illustrator", "Foreword"…
		Author       struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"author"`
	} `json:"contributions"`
	DefaultPhysicalEdition *hcEditionLang  `json:"default_physical_edition"`
	DefaultEbookEdition    *hcEditionLang  `json:"default_ebook_edition"`
	CachedTags             json.RawMessage `json:"cached_tags"`
	Rating                 *float64        `json:"rating"`
	RatingsCount           int             `json:"ratings_count"`
	UsersCount             int             `json:"users_count"`
	Pages                  *int            `json:"pages"`
	BookSeries             []struct {
		Position *float64 `json:"position"`
		Series   struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"series"`
	} `json:"book_series"`
	Editions []struct {
		Image *hcImage `json:"image"`
	} `json:"editions"`
}

type hcImage struct {
	URL string `json:"url"`
}

// hcEditionLang is the slice of an edition the author-works filter reads.
type hcEditionLang struct {
	Language *struct {
		Code2 string `json:"code2"`
	} `json:"language"`
}

func (b hcBook) result() BookResult {
	r := BookResult{Key: hcKeyPrefix + strconv.Itoa(b.ID), Title: strings.TrimSpace(b.Title)}
	if b.ReleaseYear != nil {
		r.Year = *b.ReleaseYear
	}
	if b.Image != nil {
		r.CoverURL = b.Image.URL
	}
	if len(b.Contributions) > 0 {
		r.Author = strings.TrimSpace(b.Contributions[0].Author.Name)
	}
	if b.Rating != nil && *b.Rating > 0 {
		r.Rating = *b.Rating
	}
	r.Ratings, r.Readers = b.RatingsCount, b.UsersCount
	if g := hcGenres(b.CachedTags); len(g) > 0 {
		if len(g) > 3 {
			g = g[:3]
		}
		r.Genres = g
	}
	return r
}

// --- search (Typesense-backed) ---

// hcSearchDoc is one hit's document as the search index stores it. Field types vary
// (ids as strings, images as objects or strings), so the fragile ones are raw.
type hcSearchDoc struct {
	ID          json.RawMessage `json:"id"`
	Title       string          `json:"title"`
	Name        string          `json:"name"` // authors
	AuthorNames []string        `json:"author_names"`
	ReleaseYear json.RawMessage `json:"release_year"`
	Image       json.RawMessage `json:"image"`
	BooksCount  json.RawMessage `json:"books_count"`
	Rating      json.RawMessage `json:"rating"`
	RatingsCnt  json.RawMessage `json:"ratings_count"`
	UsersCount  json.RawMessage `json:"users_count"`
	Genres      []string        `json:"genres"`
}

func rawFloat(r json.RawMessage) float64 {
	s := strings.Trim(strings.TrimSpace(string(r)), `"`)
	if s == "" || s == "null" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func rawInt(r json.RawMessage) int {
	s := strings.Trim(strings.TrimSpace(string(r)), `"`)
	if s == "" || s == "null" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int(f)
	}
	return 0
}

func rawImageURL(r json.RawMessage) string {
	s := strings.TrimSpace(string(r))
	if s == "" || s == "null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		var u string
		_ = json.Unmarshal(r, &u)
		return u
	}
	var img hcImage
	_ = json.Unmarshal(r, &img)
	return img.URL
}

// search runs Hardcover's search for one type and returns the hit documents.
func (h *Hardcover) search(ctx context.Context, query, kind string, perPage int, fields string) ([]hcSearchDoc, error) {
	const q = `query($q: String!, $t: String!, $n: Int!, $f: String) {
  search(query: $q, query_type: $t, per_page: $n, page: 1, fields: $f) { results }
}`
	vars := map[string]any{"q": query, "t": kind, "n": perPage}
	if fields != "" {
		vars["f"] = fields
	}
	var data struct {
		Search struct {
			Results json.RawMessage `json:"results"`
		} `json:"search"`
	}
	if err := h.query(ctx, q, vars, &data); err != nil {
		return nil, err
	}
	return decodeHits(data.Search.Results)
}

// decodeHits reads Typesense's {"hits":[{"document":{...}}]} (results may arrive as a
// JSON string containing that object, or as the object itself).
func decodeHits(results json.RawMessage) ([]hcSearchDoc, error) {
	raw := bytes.TrimSpace(results)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		raw = []byte(s)
	}
	var payload struct {
		Hits []struct {
			Document hcSearchDoc `json:"document"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("hardcover: parse search results: %w", err)
	}
	out := make([]hcSearchDoc, 0, len(payload.Hits))
	for _, hit := range payload.Hits {
		out = append(out, hit.Document)
	}
	return out, nil
}

func (d hcSearchDoc) bookResult() (BookResult, bool) {
	id := rawInt(d.ID)
	if id <= 0 || strings.TrimSpace(d.Title) == "" {
		return BookResult{}, false
	}
	r := BookResult{Key: hcKeyPrefix + strconv.Itoa(id), Title: strings.TrimSpace(d.Title), Year: rawInt(d.ReleaseYear), CoverURL: rawImageURL(d.Image),
		Rating: rawFloat(d.Rating), Ratings: rawInt(d.RatingsCnt), Readers: rawInt(d.UsersCount)}
	if len(d.AuthorNames) > 0 {
		r.Author = strings.TrimSpace(d.AuthorNames[0])
	}
	if len(d.Genres) > 3 {
		r.Genres = d.Genres[:3]
	} else if len(d.Genres) > 0 {
		r.Genres = d.Genres
	}
	return r, true
}

// Verify makes the three requests the module depends on — who am I, a search, a book
// fetch — and reports what each returned, so a misconfigured key or a query the API
// no longer accepts is visible from the settings page rather than as a quiet fallback.
func (h *Hardcover) Verify(ctx context.Context) (string, error) {
	if !h.Available() {
		return "", fmt.Errorf("no Hardcover key is set")
	}
	var me struct {
		Me []struct {
			Username string `json:"username"`
		} `json:"me"`
	}
	if err := h.query(ctx, `{ me { username } }`, nil, &me); err != nil {
		return "", err
	}
	who := "the key is accepted"
	if len(me.Me) > 0 && me.Me[0].Username != "" {
		who = "signed in as " + me.Me[0].Username
	}
	hits, err := h.SearchBooks(ctx, "dune frank herbert")
	if err != nil {
		return "", fmt.Errorf("%s, but search failed: %w", who, err)
	}
	if len(hits) == 0 {
		return "", fmt.Errorf("%s, but a search for Dune returned nothing", who)
	}
	d, err := h.GetBook(ctx, hits[0].Key)
	if err != nil {
		return "", fmt.Errorf("%s, search works, but fetching a book failed: %w", who, err)
	}
	series := ""
	if d.SeriesName != "" {
		series = fmt.Sprintf(", series %q", d.SeriesName)
	}
	used, budget := h.Usage()
	return fmt.Sprintf("%s · search returned %d results · fetched %q by %s (%d)%s · %d of %d requests used today", who, len(hits), d.Title, d.Author, d.Year, series, used, budget), nil
}

// SearchBooks finds books by title/author/ISBN. Hardcover's index already folds
// editions into their book, so one novel is one result.
func (h *Hardcover) SearchBooks(ctx context.Context, query string) ([]BookResult, error) {
	return cached(ctx, h.cache, "search:"+strings.ToLower(strings.TrimSpace(query)), hcTTLSearch, func(ctx context.Context) ([]BookResult, error) {
		return h.searchBooksLive(ctx, query)
	})
}

func (h *Hardcover) searchBooksLive(ctx context.Context, query string) ([]BookResult, error) {
	docs, err := h.search(ctx, query, "Book", hardcoverSearchMax, "")
	if err != nil {
		return nil, err
	}
	out := make([]BookResult, 0, len(docs))
	for _, d := range docs {
		if r, ok := d.bookResult(); ok {
			out = append(out, r)
		}
	}
	return filterBundles(out), nil
}

// GetBook returns full details for one book. A book Hardcover has merged into another
// is answered with the canonical one, under the canonical key, so a duplicate can't be
// added under the stale id.
func (h *Hardcover) GetBook(ctx context.Context, key string) (*BookDetails, error) {
	id, ok := hcBookID(key)
	if !ok {
		return nil, fmt.Errorf("hardcover: not a hardcover key: %q", key)
	}
	return cached(ctx, h.cache, "book:"+key, hcTTLBook, func(ctx context.Context) (*BookDetails, error) {
		return h.getBookLive(ctx, id)
	})
}

func (h *Hardcover) getBookLive(ctx context.Context, id int) (*BookDetails, error) {
	b, err := h.book(ctx, id)
	if err != nil {
		return nil, err
	}
	if b.CanonicalID != nil && *b.CanonicalID > 0 && *b.CanonicalID != id {
		if c, err := h.book(ctx, *b.CanonicalID); err == nil {
			b = c
		}
	}
	d := &BookDetails{BookResult: b.result(), Description: strings.TrimSpace(b.Description)}
	d.Subjects = hcGenres(b.CachedTags)
	if b.Pages != nil {
		d.Pages = *b.Pages
	}
	// The lowest-numbered series membership is the one people mean ("Dune #1", not
	// "Frank Herbert Collection #7").
	for _, bs := range b.BookSeries {
		if bs.Series.Name == "" {
			continue
		}
		pos := 0.0
		if bs.Position != nil {
			pos = *bs.Position
		}
		if d.SeriesName == "" || (pos > 0 && (d.SeriesPosition == 0 || pos < d.SeriesPosition)) {
			d.SeriesName, d.SeriesPosition = bs.Series.Name, pos
			if bs.Series.ID > 0 {
				d.SeriesKey = hcSeriesPrefix + strconv.Itoa(bs.Series.ID)
			}
		}
	}
	return d, nil
}

func (h *Hardcover) book(ctx context.Context, id int) (*hcBook, error) {
	const q = `query($id: Int!) {
  books(where: {id: {_eq: $id}}, limit: 1) {
    ` + hcBookFields + `
    subtitle description pages
    book_series { position series { id name } }
  }
}`
	var data struct {
		Books []hcBook `json:"books"`
	}
	if err := h.query(ctx, q, map[string]any{"id": id}, &data); err != nil {
		return nil, err
	}
	if len(data.Books) == 0 {
		return nil, fmt.Errorf("hardcover: book %d not found", id)
	}
	return &data.Books[0], nil
}

// hcGenres pulls the Genre tags out of cached_tags ({"Genre":[{"tag":"Fantasy",...}]}).
func hcGenres(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var tags map[string][]struct {
		Tag string `json:"tag"`
	}
	if json.Unmarshal(raw, &tags) != nil {
		return nil
	}
	var out []string
	for _, key := range []string{"Genre", "genre"} {
		for _, t := range tags[key] {
			if t.Tag != "" {
				out = append(out, t.Tag)
			}
		}
	}
	return trimSubjects(out)
}

// Covers offers the book's own cover images plus every edition's, then the Google
// Books hits the Open Library provider knows how to fetch.
func (h *Hardcover) Covers(ctx context.Context, key, title, author string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	if id, ok := hcBookID(key); ok {
		const q = `query($id: Int!) {
  books(where: {id: {_eq: $id}}, limit: 1) {
    id image { url } images { url } editions(limit: 60) { image { url } }
  }
}`
		var data struct {
			Books []hcBook `json:"books"`
		}
		if err := h.query(ctx, q, map[string]any{"id": id}, &data); err == nil && len(data.Books) > 0 {
			b := data.Books[0]
			if b.Image != nil {
				add(b.Image.URL)
			}
			for _, im := range b.Images {
				add(im.URL)
			}
			for _, e := range b.Editions {
				if e.Image != nil {
					add(e.Image.URL)
				}
			}
		}
	}
	if h.extraCovers != nil {
		if more, err := h.extraCovers.Covers(ctx, "", title, author); err == nil {
			for _, u := range more {
				add(u)
			}
		}
	}
	return out, nil
}

// SearchAuthors finds authors by name.
func (h *Hardcover) SearchAuthors(ctx context.Context, query string) ([]AuthorResult, error) {
	return cached(ctx, h.cache, "authors:"+strings.ToLower(strings.TrimSpace(query)), hcTTLSearch, func(ctx context.Context) ([]AuthorResult, error) {
		return h.searchAuthorsLive(ctx, query)
	})
}

func (h *Hardcover) searchAuthorsLive(ctx context.Context, query string) ([]AuthorResult, error) {
	docs, err := h.search(ctx, query, "Author", 12, "")
	if err != nil {
		return nil, err
	}
	out := make([]AuthorResult, 0, len(docs))
	for _, d := range docs {
		id := rawInt(d.ID)
		if id <= 0 || strings.TrimSpace(d.Name) == "" {
			continue
		}
		out = append(out, AuthorResult{Key: hcAuthorPrefix + strconv.Itoa(id), Name: strings.TrimSpace(d.Name), WorkCount: rawInt(d.BooksCount), ImageURL: rawImageURL(d.Image)})
	}
	return out, nil
}

// AuthorWorks lists an author's books, most-shelved first, leaving out entries
// Hardcover has merged into others and compilations.
func (h *Hardcover) AuthorWorks(ctx context.Context, authorKey string, limit int) ([]BookResult, error) {
	id, ok := hcAuthorID(authorKey)
	if !ok {
		return nil, fmt.Errorf("hardcover: not a hardcover author key: %q", authorKey)
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	return cached(ctx, h.cache, fmt.Sprintf("works:%d:%d", id, limit), hcTTLList, func(ctx context.Context) ([]BookResult, error) {
		return h.authorWorksLive(ctx, id, limit)
	})
}

func (h *Hardcover) authorWorksLive(ctx context.Context, id, limit int) ([]BookResult, error) {
	const q = `query($id: Int!, $n: Int!) {
  books(
    where: {contributions: {author: {id: {_eq: $id}}}, canonical_id: {_is_null: true}, compilation: {_eq: false}},
    order_by: {users_count: desc}, limit: $n
  ) { ` + hcAuthorBookFields + ` }
}`
	var data struct {
		Books []hcBook `json:"books"`
	}
	err := h.query(ctx, q, map[string]any{"id": id, "n": limit}, &data)
	if err != nil && !errors.Is(err, ErrHardcoverBudget) {
		// The edition-language lookup is the one part of this query the schema could
		// refuse (query depth, a renamed relation). Fall back to the plain fields: the
		// role and split-part filters still apply, only the language one is lost.
		slog.Warn("hardcover: author works with edition languages failed — retrying without", "err", err)
		q2 := strings.Replace(q, hcAuthorBookFields, hcBookFields+` contributions { contribution author { id name } }`, 1)
		err = h.query(ctx, q2, map[string]any{"id": id, "n": limit}, &data)
	}
	if err != nil {
		return nil, err
	}
	return filterAuthorWorks(data.Books, id, time.Now()), nil
}

// TrendingBooks approximates "trending" as the most-shelved books released in the last
// two years — Hardcover's own trending feed isn't part of the public schema.
func (h *Hardcover) TrendingBooks(ctx context.Context) ([]BookResult, error) {
	return cached(ctx, h.cache, "trending", hcTTLList, func(ctx context.Context) ([]BookResult, error) { return h.trendingLive(ctx) })
}

func (h *Hardcover) trendingLive(ctx context.Context) ([]BookResult, error) {
	const q = `query($y: Int!) {
  books(
    where: {canonical_id: {_is_null: true}, compilation: {_eq: false}, release_year: {_gte: $y}},
    order_by: {users_count: desc}, limit: 24
  ) { ` + hcBookFields + ` }
}`
	var data struct {
		Books []hcBook `json:"books"`
	}
	if err := h.query(ctx, q, map[string]any{"y": time.Now().Year() - 2}, &data); err != nil {
		return nil, err
	}
	out := make([]BookResult, 0, len(data.Books))
	for _, b := range data.Books {
		if r := b.result(); r.Title != "" {
			out = append(out, r)
		}
	}
	return filterBundles(out), nil
}

// BooksBySubject searches the genre field; if the index refuses the field selector, a
// plain search for the subject word is close enough.
func (h *Hardcover) BooksBySubject(ctx context.Context, subject string, limit int) ([]BookResult, error) {
	if limit <= 0 {
		limit = hardcoverSearchMax
	}
	return cached(ctx, h.cache, fmt.Sprintf("subject:%s:%d", strings.ToLower(subject), limit), hcTTLList, func(ctx context.Context) ([]BookResult, error) {
		return h.subjectLive(ctx, subject, limit)
	})
}

func (h *Hardcover) subjectLive(ctx context.Context, subject string, limit int) ([]BookResult, error) {
	docs, err := h.search(ctx, subject, "Book", limit, "genres")
	if err != nil {
		docs, err = h.search(ctx, subject, "Book", limit, "")
		if err != nil {
			return nil, err
		}
	}
	out := make([]BookResult, 0, len(docs))
	for _, d := range docs {
		if r, ok := d.bookResult(); ok {
			out = append(out, r)
		}
	}
	return filterBundles(out), nil
}
