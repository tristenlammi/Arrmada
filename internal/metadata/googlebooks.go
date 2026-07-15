package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// gbKeyPrefix marks a Google Books volume key so the fallback provider can route GetBook back to
// the right source (Open Library keys look like "/works/OL…W").
const gbKeyPrefix = "gb:"

// GoogleBooks is a secondary book-metadata provider. Open Library is the primary; Google Books is
// the fallback for the acquisition-critical paths (search + get) so a book Open Library can't find
// (the prolific-author / obscure-edition case that broke Readarr) still resolves. It deliberately
// does not implement authors/trending/subject — those stay with Open Library.
type GoogleBooks struct {
	http *http.Client
}

// NewGoogleBooks builds the provider (no API key required for basic volume lookups).
func NewGoogleBooks() *GoogleBooks {
	return &GoogleBooks{http: &http.Client{Timeout: 15 * time.Second}}
}

func (g *GoogleBooks) Available() bool { return true }

// gbVolume is the slice of the Google Books volume response we use.
type gbVolume struct {
	ID         string `json:"id"`
	VolumeInfo struct {
		Title         string   `json:"title"`
		Subtitle      string   `json:"subtitle"`
		Authors       []string `json:"authors"`
		PublishedDate string   `json:"publishedDate"`
		Description   string   `json:"description"`
		PageCount     int      `json:"pageCount"`
		Categories    []string `json:"categories"`
		ImageLinks    struct {
			Thumbnail      string `json:"thumbnail"`
			SmallThumbnail string `json:"smallThumbnail"`
		} `json:"imageLinks"`
	} `json:"volumeInfo"`
}

func (g *GoogleBooks) fetch(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Arrmada/1.0 (self-hosted media manager)")
	resp, err := g.http.Do(req)
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

func (v gbVolume) toResult() BookResult {
	author := ""
	if len(v.VolumeInfo.Authors) > 0 {
		author = v.VolumeInfo.Authors[0]
	}
	cover := v.VolumeInfo.ImageLinks.Thumbnail
	if cover == "" {
		cover = v.VolumeInfo.ImageLinks.SmallThumbnail
	}
	return BookResult{
		Key:      gbKeyPrefix + v.ID,
		Title:    v.VolumeInfo.Title,
		Author:   author,
		Year:     yearFromDate(v.VolumeInfo.PublishedDate),
		CoverURL: httpsCover(cover),
	}
}

// SearchBooks queries Google Books volumes for the free-text query.
func (g *GoogleBooks) SearchBooks(ctx context.Context, query string) ([]BookResult, error) {
	q := url.Values{"q": {query}, "maxResults": {"20"}, "printType": {"books"}}
	body, err := g.fetch(ctx, googleBooksAPI+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var raw struct {
		Items []gbVolume `json:"items"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]BookResult, 0, len(raw.Items))
	for _, v := range raw.Items {
		if v.VolumeInfo.Title == "" {
			continue
		}
		out = append(out, v.toResult())
	}
	return out, nil
}

// GetBook fetches a single Google Books volume by its "gb:<id>" key.
func (g *GoogleBooks) GetBook(ctx context.Context, key string) (*BookDetails, error) {
	id := strings.TrimPrefix(key, gbKeyPrefix)
	if id == "" {
		return nil, fmt.Errorf("empty google books key")
	}
	body, err := g.fetch(ctx, googleBooksAPI+"/"+url.PathEscape(id))
	if err != nil {
		return nil, err
	}
	var v gbVolume
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	if v.VolumeInfo.Title == "" {
		return nil, fmt.Errorf("volume not found")
	}
	return &BookDetails{
		BookResult:  v.toResult(),
		Description: v.VolumeInfo.Description,
		Subjects:    v.VolumeInfo.Categories,
		Pages:       v.VolumeInfo.PageCount,
	}, nil
}

// The author / discover surfaces stay with Open Library — Google Books has no equivalent, so these
// return empty (the fallback provider never routes them here).
func (g *GoogleBooks) Covers(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}
func (g *GoogleBooks) SearchAuthors(context.Context, string) ([]AuthorResult, error) { return nil, nil }
func (g *GoogleBooks) AuthorWorks(context.Context, string, int) ([]BookResult, error) { return nil, nil }
func (g *GoogleBooks) TrendingBooks(context.Context) ([]BookResult, error)            { return nil, nil }
func (g *GoogleBooks) BooksBySubject(context.Context, string, int) ([]BookResult, error) {
	return nil, nil
}

// yearFromDate pulls a 4-digit year from a "YYYY", "YYYY-MM", or "YYYY-MM-DD" date string.
func yearFromDate(d string) int {
	if len(d) < 4 {
		return 0
	}
	y := 0
	for _, c := range d[:4] {
		if c < '0' || c > '9' {
			return 0
		}
		y = y*10 + int(c-'0')
	}
	return y
}

// httpsCover upgrades Google's http image links to https.
func httpsCover(u string) string {
	if strings.HasPrefix(u, "http://") {
		return "https://" + strings.TrimPrefix(u, "http://")
	}
	return u
}
