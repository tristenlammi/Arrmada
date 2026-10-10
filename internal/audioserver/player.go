package audioserver

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"

	"github.com/tristenlammi/arrmada/internal/listening"
)

// Arrmada's own player (the Listen tab and mini-player) reads the same library, shelves
// and places as the listening apps, through these exported calls rather than the
// Audiobookshelf JSON. The httpapi routes under /api/v1/me/audio/ wrap them.

// ErrNotFound is returned for an item key that isn't an audiobook in the library.
var ErrNotFound = errItemNotFound

// playerPrefix is where the main API serves an item (covers, audio files).
const playerPrefix = "/api/v1/me/audio/items/"

// Card is an audiobook as the web player lists it.
type Card struct {
	Key       string `json:"key"`
	BookID    int64  `json:"book_id"`
	VersionID int64  `json:"version_id,omitempty"`
	Title     string `json:"title"`
	Author    string `json:"author,omitempty"`
	Series    string `json:"series,omitempty"`
	SeriesSeq string `json:"series_seq,omitempty"`
	// Cover is a same-origin URL (empty when the book has none); its ?v= changes with
	// the cover, so it can be cached.
	Cover    string  `json:"cover,omitempty"`
	Duration float64 `json:"duration"` // seconds; 0 until the files have been read
	AddedAt  int64   `json:"added_at"`
	// Progress is the caller's own place, or null when they haven't started it.
	Progress *listening.Progress `json:"progress"`
}

// Shelf is one row of the Listen tab.
type Shelf struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Items []Card `json:"items"`
}

// Track is one audio file of an item, in play order. start_offset is where it starts
// within the whole book.
type Track struct {
	Ino         string  `json:"ino"`
	Index       int     `json:"index"`
	StartOffset float64 `json:"start_offset"`
	Duration    float64 `json:"duration"`
	Mime        string  `json:"mime"`
	URL         string  `json:"url"`
}

// VersionRef is another audiobook of the same book (a full-cast production, another
// narrator).
type VersionRef struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// ItemDetail is everything the book sheet and the player need.
type ItemDetail struct {
	Card
	Description string               `json:"description,omitempty"`
	Year        int                  `json:"year,omitempty"`
	Genres      []string             `json:"genres"`
	Chapters    []Chapter            `json:"chapters"`
	Tracks      []Track              `json:"tracks"`
	Versions    []VersionRef         `json:"versions"`
	Bookmarks   []listening.Bookmark `json:"bookmarks"`
}

// card shapes an item for the web player.
func (s *Server) card(ctx context.Context, it Item, prog map[string]listening.Progress) Card {
	files, _ := s.probe.files(ctx, it.Path, false)
	c := Card{Key: it.Key, BookID: it.Book.ID, Title: it.Title, Author: it.Book.Author, Series: it.Book.SeriesName,
		SeriesSeq: seriesSequence(it.Book), Duration: totalDuration(files), AddedAt: it.AddedAt}
	if it.Version != nil {
		c.VersionID = it.Version.ID
	}
	if it.Book.CoverURL != "" {
		// The cover URL changes when a new cover is uploaded, so ?v= follows it.
		c.Cover = playerPrefix + url.PathEscape(it.Key) + "/cover?v=" + shortHash(it.Book.CoverURL)
	}
	if p, ok := prog[it.Key]; ok {
		c.Progress = &p
	}
	return c
}

func (s *Server) cards(ctx context.Context, items []Item, prog map[string]listening.Progress) []Card {
	out := make([]Card, 0, len(items))
	for _, it := range items {
		out = append(out, s.card(ctx, it, prog))
	}
	return out
}

// Shelves is the Listen tab's rows for a user: the same shelves an app's home screen
// shows (Continue Listening, Continue Series, Recently Added, Listen Again).
func (s *Server) Shelves(ctx context.Context, userID int64) ([]Shelf, error) {
	items, err := s.items(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.listen.AllProgress(ctx, userID)
	if err != nil {
		return nil, err
	}
	prog := progressByKey(all)
	out := []Shelf{}
	for _, def := range shelvesFor(items, all, prog) {
		out = append(out, Shelf{ID: def.id, Label: def.label, Items: s.cards(ctx, def.items, prog)})
	}
	return out, nil
}

// CatalogQuery picks a page of the library.
type CatalogQuery struct {
	Q      string // matches title, author or series
	Sort   string // title (default), author, added (newest first), duration
	Filter string // "", in-progress, finished, not-started
	Page   int    // from 0
	Limit  int    // default 50, at most 200
}

// PageSize is the page length used: Limit, 50 when unset, at most 200.
func (q CatalogQuery) PageSize() int {
	if q.Limit <= 0 {
		return 50
	}
	return min(q.Limit, 200)
}

// Catalog is the whole library as cards, searched, filtered, sorted and paged, with the
// total before paging.
func (s *Server) Catalog(ctx context.Context, userID int64, q CatalogQuery) ([]Card, int, error) {
	items, err := s.items(ctx)
	if err != nil {
		return nil, 0, err
	}
	prog := s.progressMap(ctx, userID)
	if needle := strings.ToLower(strings.TrimSpace(q.Q)); needle != "" {
		kept := items[:0:0]
		for _, it := range items {
			if strings.Contains(strings.ToLower(it.Title), needle) || strings.Contains(strings.ToLower(it.Book.Author), needle) ||
				strings.Contains(strings.ToLower(it.Book.SeriesName), needle) {
				kept = append(kept, it)
			}
		}
		items = kept
	}
	switch q.Filter {
	case "in-progress", "finished", "not-started":
		// The same filter an app's library view uses (it takes the value base64'd).
		items = applyFilter(items, "progress."+base64.StdEncoding.EncodeToString([]byte(q.Filter)), prog)
	default:
		items = append([]Item(nil), items...) // sortItems sorts in place; the catalogue is shared
	}
	dur := func(it Item) float64 {
		files, _ := s.probe.files(ctx, it.Path, false)
		return totalDuration(files)
	}
	switch q.Sort {
	case "author":
		sortItems(items, "media.metadata.authorName", false, nil)
	case "added":
		sortItems(items, "addedAt", true, nil)
	case "duration":
		sortItems(items, "media.duration", false, dur)
	default:
		sortItems(items, "media.metadata.title", false, nil)
	}
	limit := q.PageSize()
	total := len(items)
	start := min(max(q.Page, 0)*limit, total)
	return s.cards(ctx, items[start:min(start+limit, total)], prog), total, nil
}

// Detail is one audiobook with its chapters, tracks, other versions and the caller's
// bookmarks and place. It reads files not read yet (ffprobe), like an app opening it.
func (s *Server) Detail(ctx context.Context, userID int64, key string) (ItemDetail, error) {
	it, err := s.item(ctx, key)
	if err != nil {
		return ItemDetail{}, ErrNotFound
	}
	files, _ := s.probe.files(ctx, it.Path, true)
	prog := map[string]listening.Progress{}
	if p, ok, err := s.listen.Progress(ctx, userID, key); err != nil {
		return ItemDetail{}, err
	} else if ok {
		prog[key] = p
	}
	d := ItemDetail{Card: s.card(ctx, it, prog), Description: it.Book.Description, Year: it.Book.Year,
		Genres: nonNilStrings(it.Book.Subjects), Chapters: nonNilChapters(bookChapters(files)), Tracks: tracksFor(it, files),
		Versions: []VersionRef{}}
	d.Duration = totalDuration(files)
	if all, err := s.items(ctx); err == nil {
		for _, other := range all {
			if other.Book.ID == it.Book.ID && other.Key != it.Key {
				d.Versions = append(d.Versions, VersionRef{Key: other.Key, Title: other.Title})
			}
		}
	}
	if d.Bookmarks, err = s.listen.Bookmarks(ctx, userID, key); err != nil {
		return ItemDetail{}, err
	}
	return d, nil
}

// tracksFor lists an item's files as play tracks, each fetched from the main API.
func tracksFor(it Item, files []AudioFile) []Track {
	out := make([]Track, 0, len(files))
	offset := 0.0
	for _, f := range files {
		out = append(out, Track{Ino: f.Ino, Index: f.Index, StartOffset: offset, Duration: f.Duration, Mime: mimeFor(f.Ext),
			URL: playerPrefix + url.PathEscape(it.Key) + "/file/" + url.PathEscape(f.Ino)})
		offset += f.Duration
	}
	return out
}

// ServeCover sends an item's cover to the web player. It's private to the signed-in
// browser (it comes through Arrmada's own sign-in), cached for a day.
func (s *Server) ServeCover(w http.ResponseWriter, r *http.Request, key string) {
	s.serveCover(w, r, key, "private, max-age=86400")
}
