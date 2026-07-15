package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	olBaseURL      = "https://openlibrary.org"
	olCoverBase    = "https://covers.openlibrary.org/b/id"
	googleBooksAPI = "https://www.googleapis.com/books/v1/volumes"
)

// OpenLibrary is a book metadata provider backed by openlibrary.org. It needs no API
// key, which sidesteps the metadata-fragility trap that killed Readarr's single
// Goodreads dependency — Open Library is open data and can be mirrored/swapped later.
type OpenLibrary struct {
	http *http.Client
}

// NewOpenLibrary builds the provider.
func NewOpenLibrary() *OpenLibrary {
	return &OpenLibrary{http: &http.Client{Timeout: 15 * time.Second}}
}

// Available is always true — no key required.
func (o *OpenLibrary) Available() bool { return true }

func (o *OpenLibrary) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	u := olBaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return o.getURL(ctx, u)
}

// getURL fetches an arbitrary URL (used for openlibrary.org and, for cover discovery,
// the Google Books API).
func (o *OpenLibrary) getURL(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Arrmada/1.0 (self-hosted media manager)")
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// SearchBooks searches Open Library for works by title/author.
func (o *OpenLibrary) SearchBooks(ctx context.Context, query string) ([]BookResult, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("limit", "24")
	q.Set("fields", "key,title,author_name,first_publish_year,cover_i")
	body, err := o.get(ctx, "/search.json", q)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Docs []struct {
			Key     string   `json:"key"`
			Title   string   `json:"title"`
			Authors []string `json:"author_name"`
			Year    int      `json:"first_publish_year"`
			CoverID int      `json:"cover_i"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]BookResult, 0, len(payload.Docs))
	for _, d := range payload.Docs {
		if d.Title == "" {
			continue
		}
		out = append(out, BookResult{
			Key:      workKey(d.Key),
			Title:    d.Title,
			Author:   firstOr(d.Authors, ""),
			Year:     d.Year,
			CoverURL: coverURL(d.CoverID),
		})
	}
	return filterBundles(out), nil
}

// SearchAuthors finds authors by name.
func (o *OpenLibrary) SearchAuthors(ctx context.Context, query string) ([]AuthorResult, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("limit", "12")
	body, err := o.get(ctx, "/search/authors.json", q)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Docs []struct {
			Key       string `json:"key"`
			Name      string `json:"name"`
			WorkCount int    `json:"work_count"`
			TopWork   string `json:"top_work"`
			BirthDate string `json:"birth_date"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]AuthorResult, 0, len(payload.Docs))
	for _, d := range payload.Docs {
		if d.Name == "" {
			continue
		}
		out = append(out, AuthorResult{
			Key: authorKey(d.Key), Name: d.Name, WorkCount: d.WorkCount, TopWork: d.TopWork, BirthDate: d.BirthDate,
		})
	}
	return out, nil
}

// AuthorWorks returns an author's catalogue, ranked by edition count (most significant
// works first) and limited to English titles. This deliberately uses the Open Library
// *search* API rather than /authors/{key}/works.json: the latter returns an unranked
// firehose of every translation, SparkNotes-style study guide and junk record ever
// attributed to the author key, whereas search ranks the real canonical works and carries
// covers + years.
func (o *OpenLibrary) AuthorWorks(ctx context.Context, key string, limit int) ([]BookResult, error) {
	if limit <= 0 {
		limit = 48
	}
	q := url.Values{}
	q.Set("author_key", authorKey(key))
	q.Set("sort", "editions") // most-published works first ≈ the author's real, notable books
	q.Set("language", "eng")  // collapse the pile of translations to the English edition
	q.Set("limit", fmt.Sprintf("%d", limit))
	q.Set("fields", "key,title,author_name,first_publish_year,cover_i")
	body, err := o.get(ctx, "/search.json", q)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Docs []struct {
			Key     string   `json:"key"`
			Title   string   `json:"title"`
			Authors []string `json:"author_name"`
			Year    int      `json:"first_publish_year"`
			CoverID int      `json:"cover_i"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]BookResult, 0, len(payload.Docs))
	for _, d := range payload.Docs {
		if d.Title == "" {
			continue
		}
		out = append(out, BookResult{
			Key: workKey(d.Key), Title: d.Title, Author: firstOr(d.Authors, ""),
			Year: d.Year, CoverURL: coverURL(d.CoverID),
		})
	}
	return filterBundles(out), nil
}

// TrendingBooks returns books trending this week.
func (o *OpenLibrary) TrendingBooks(ctx context.Context) ([]BookResult, error) {
	body, err := o.get(ctx, "/trending/weekly.json", nil)
	if err != nil {
		return nil, err
	}
	return decodeWorkList(body)
}

// BooksBySubject returns books tagged with a subject/genre.
func (o *OpenLibrary) BooksBySubject(ctx context.Context, subject string, limit int) ([]BookResult, error) {
	if limit <= 0 {
		limit = 24
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", limit))
	subject = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(subject), " ", "_"))
	body, err := o.get(ctx, "/subjects/"+subject+".json", q)
	if err != nil {
		return nil, err
	}
	return decodeWorkList(body)
}

// decodeWorkList parses the {works:[...]} shape shared by /trending and /subjects, where
// each work carries cover_i (trending) or cover_id (subjects) plus author_name/authors.
func decodeWorkList(body []byte) ([]BookResult, error) {
	var payload struct {
		Works []struct {
			Key         string   `json:"key"`
			Title       string   `json:"title"`
			CoverI      int      `json:"cover_i"`
			CoverID     int      `json:"cover_id"`
			Year        int      `json:"first_publish_year"`
			AuthorNames []string `json:"author_name"`
			Authors     []struct {
				Name string `json:"name"`
			} `json:"authors"`
		} `json:"works"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]BookResult, 0, len(payload.Works))
	for _, wk := range payload.Works {
		if wk.Title == "" {
			continue
		}
		br := BookResult{Key: workKey(wk.Key), Title: wk.Title, Year: wk.Year}
		switch {
		case wk.CoverI > 0:
			br.CoverURL = coverURL(wk.CoverI)
		case wk.CoverID > 0:
			br.CoverURL = coverURL(wk.CoverID)
		}
		if len(wk.AuthorNames) > 0 {
			br.Author = wk.AuthorNames[0]
		} else if len(wk.Authors) > 0 {
			br.Author = wk.Authors[0].Name
		}
		out = append(out, br)
	}
	return out, nil
}

// GetBook fetches a work's details (description, subjects) by its work key.
func (o *OpenLibrary) GetBook(ctx context.Context, key string) (*BookDetails, error) {
	body, err := o.get(ctx, "/works/"+workKey(key)+".json", nil)
	if err != nil {
		return nil, err
	}
	var w struct {
		Title       string          `json:"title"`
		Description json.RawMessage `json:"description"`
		Subjects    []string        `json:"subjects"`
		Covers      []int           `json:"covers"`
		Authors     []struct {
			Author struct {
				Key string `json:"key"`
			} `json:"author"`
		} `json:"authors"`
		Created struct {
			Value string `json:"value"`
		} `json:"created"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("openlibrary: parse work: %w", err)
	}
	d := &BookDetails{
		BookResult:  BookResult{Key: workKey(key), Title: w.Title},
		Description: descOf(w.Description),
		Subjects:    trimSubjects(w.Subjects),
	}
	if len(w.Covers) > 0 {
		d.CoverURL = coverURL(w.Covers[0])
	}
	// Author name needs a second lookup (work only carries the author key).
	if len(w.Authors) > 0 {
		d.Author = o.authorName(ctx, w.Authors[0].Author.Key)
	}
	return d, nil
}

// Covers aggregates candidate cover images for a book from multiple free sources so the
// user can pick a nicer one: every Open Library edition's cover (large, portrait), plus
// Google Books results for extra variety. No API key is required for either.
func (o *OpenLibrary) Covers(ctx context.Context, key, title, author string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	// 1. Open Library editions — a popular work often has dozens of distinct covers.
	if k := workKey(key); k != "" {
		q := url.Values{}
		q.Set("limit", "50")
		if body, err := o.get(ctx, "/works/"+k+"/editions.json", q); err == nil {
			var payload struct {
				Entries []struct {
					Covers []int `json:"covers"`
				} `json:"entries"`
			}
			if json.Unmarshal(body, &payload) == nil {
				for _, e := range payload.Entries {
					for _, id := range e.Covers {
						add(coverURL(id))
					}
				}
			}
		}
	}
	// 2. Google Books — extra portrait covers, keyed off title/author.
	for _, u := range o.googleCovers(ctx, title, author) {
		add(u)
	}
	return out, nil
}

// googleCovers queries the Google Books volumes API (no key required) and returns cover
// image URLs from the hits.
func (o *OpenLibrary) googleCovers(ctx context.Context, title, author string) []string {
	if strings.TrimSpace(title) == "" {
		return nil
	}
	q := "intitle:" + title
	if author != "" {
		q += " inauthor:" + author
	}
	v := url.Values{}
	v.Set("q", q)
	v.Set("maxResults", "20")
	v.Set("printType", "books")
	v.Set("country", "US")
	body, err := o.getURL(ctx, googleBooksAPI+"?"+v.Encode())
	if err != nil {
		return nil
	}
	var payload struct {
		Items []struct {
			VolumeInfo struct {
				ImageLinks struct {
					Thumbnail string `json:"thumbnail"`
				} `json:"imageLinks"`
			} `json:"volumeInfo"`
		} `json:"items"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	var out []string
	for _, it := range payload.Items {
		if u := normalizeGoogleCover(it.VolumeInfo.ImageLinks.Thumbnail); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// normalizeGoogleCover cleans a Google Books thumbnail URL: force https and drop the
// page-curl edge so it reads as a flat cover.
func normalizeGoogleCover(u string) string {
	if u == "" {
		return ""
	}
	u = strings.Replace(u, "http://", "https://", 1)
	u = strings.Replace(u, "&edge=curl", "", 1)
	return u
}

func (o *OpenLibrary) authorName(ctx context.Context, key string) string {
	key = strings.TrimPrefix(key, "/authors/")
	key = strings.TrimSuffix(key, ".json")
	if key == "" {
		return ""
	}
	body, err := o.get(ctx, "/authors/"+key+".json", nil)
	if err != nil {
		return ""
	}
	var a struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(body, &a) != nil {
		return ""
	}
	return a.Name
}

// --- helpers ---

// workKey normalizes "/works/OL45804W" (or a bare id) to "OL45804W".
func workKey(k string) string {
	k = strings.TrimPrefix(k, "/works/")
	k = strings.TrimSuffix(k, ".json")
	return k
}

// authorKey normalizes "/authors/OL23919A" (or a bare id) to "OL23919A".
func authorKey(k string) string {
	k = strings.TrimPrefix(k, "/authors/")
	k = strings.TrimSuffix(k, ".json")
	return k
}

// bundleRangeRe matches multi-volume / range markers that only appear on box sets,
// omnibuses and split-volume fragments — e.g. "[2/4]", "books 1-7", "vol. 1-3", "#1-6",
// "(1-6)". It deliberately requires a book/volume/series/# prefix or parentheses so it
// won't false-positive on real titles like "11-22-63" or "Catch-22".
var bundleRangeRe = regexp.MustCompile(`(?i)\[\s*\d+\s*/\s*\d+\s*\]|(?:books?|vols?|volumes?|series|parts?|#)\s*\.?\s*\d+\s*[-–—]\s*\d+|\(\s*\d+\s*[-–—]\s*\d+\s*\)`)

// bundleKeywords flag titles that are collections/bundles or third-party study guides
// rather than a single book the author actually wrote.
var bundleKeywords = []string{
	// Box sets / omnibuses / multi-book bundles.
	"(series)", "box set", "boxed set", "boxset", "slipcase", "omnibus", "collection",
	"complete series", "complete novels", "complete works", "the complete",
	"book set", "books set", "set of", "-book set", "boxed", "bundle", "-in-1", " in 1)",
	"trilogy set", "schoolbooks",
	// Third-party study guides / summaries (the "random community content" to exclude).
	"sparknotes", "cliffsnotes", "cliff's notes", "study guide", "literature guide",
	"summary and analysis", "summary & analysis", "a study of",
}

// isBundleTitle reports whether a title is a box set / omnibus / multi-volume bundle, a
// third-party study guide, or a junk catalogue record — rather than a single book the
// author wrote. Open Library mixes these in with real works; we hide them so users only see
// (and can add/request) individual books.
func isBundleTitle(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "[") || strings.Contains(title, "*INVALID*") {
		return true
	}
	if strings.Contains(title, " / ") { // combined editions, e.g. "Book A / Book B"
		return true
	}
	for _, kw := range bundleKeywords {
		if strings.Contains(t, kw) {
			return true
		}
	}
	return bundleRangeRe.MatchString(t)
}

// filterBundles drops box-set / omnibus / junk records, keeping only individual books.
func filterBundles(in []BookResult) []BookResult {
	out := make([]BookResult, 0, len(in))
	for _, b := range in {
		if !isBundleTitle(b.Title) {
			out = append(out, b)
		}
	}
	return out
}

func coverURL(id int) string {
	if id <= 0 {
		return ""
	}
	return fmt.Sprintf("%s/%d-L.jpg", olCoverBase, id)
}

func firstOr(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}

// descOf handles Open Library's description being either a plain string or a
// {"type":..., "value":...} object.
func descOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Value
	}
	return ""
}

func trimSubjects(s []string) []string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
