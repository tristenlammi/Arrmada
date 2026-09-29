package audioserver

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
)

// The library a listening app sees: one item per audiobook — the standard audiobook of
// each book, plus each audiobook version as its own item ("Title (Full cast)"), so an
// app lists them side by side. Item ids are "b12" and "b12v3" and never change.

const libraryID = "arrmada-audiobooks"

var errItemNotFound = errors.New("item not found")

// Item is one audiobook as apps see it.
type Item struct {
	Key     string
	Book    books.Book
	Version *books.AudioVersion
	Path    string // the audiobook file or folder
	Title   string
	AddedAt int64
}

func itemKeyFor(bookID int64, versionID int64) string {
	if versionID > 0 {
		return fmt.Sprintf("b%dv%d", bookID, versionID)
	}
	return fmt.Sprintf("b%d", bookID)
}

// parseItemKey splits "b12" / "b12v3" into ids.
func parseItemKey(key string) (bookID, versionID int64, ok bool) {
	if !strings.HasPrefix(key, "b") {
		return 0, 0, false
	}
	rest := key[1:]
	vs := ""
	if i := strings.IndexByte(rest, 'v'); i >= 0 {
		rest, vs = rest[:i], rest[i+1:]
	}
	b, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || b <= 0 {
		return 0, 0, false
	}
	if vs != "" {
		v, err := strconv.ParseInt(vs, 10, 64)
		if err != nil || v <= 0 {
			return 0, 0, false
		}
		return b, v, true
	}
	return b, 0, true
}

// items lists every audiobook with a file on disk (cached briefly; see cache.go).
func (s *Server) items(ctx context.Context) ([]Item, error) {
	now := time.Now()
	if items, ok := s.catalog.get(now); ok {
		return items, nil
	}
	gen := s.catalog.generation()
	items, err := s.loadItems(ctx)
	if err == nil {
		s.catalog.put(items, now, gen)
	}
	return items, err
}

func (s *Server) loadItems(ctx context.Context) ([]Item, error) {
	all, err := s.books.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, b := range all {
		added := parseAdded(b.AddedAt)
		if b.Audiobook != nil && b.Audiobook.Path != "" {
			out = append(out, Item{Key: itemKeyFor(b.ID, 0), Book: b, Path: b.Audiobook.Path, Title: b.Title, AddedAt: added})
		}
		for i := range b.AudioVersions {
			v := b.AudioVersions[i]
			if v.File == nil || v.File.Path == "" {
				continue
			}
			vAdded := parseAdded(v.AddedAt)
			if vAdded == 0 {
				vAdded = added
			}
			out = append(out, Item{Key: itemKeyFor(b.ID, v.ID), Book: b, Version: &v, Path: v.File.Path,
				Title: v.FolderTitle(b.Title), AddedAt: vAdded})
		}
	}
	return out, nil
}

// item loads one audiobook by key.
func (s *Server) item(ctx context.Context, key string) (Item, error) {
	bookID, versionID, ok := parseItemKey(key)
	if !ok {
		return Item{}, errItemNotFound
	}
	b, err := s.books.Get(ctx, bookID)
	if err != nil {
		return Item{}, errItemNotFound
	}
	added := parseAdded(b.AddedAt)
	if versionID == 0 {
		if b.Audiobook == nil || b.Audiobook.Path == "" {
			return Item{}, errItemNotFound
		}
		return Item{Key: key, Book: b, Path: b.Audiobook.Path, Title: b.Title, AddedAt: added}, nil
	}
	for i := range b.AudioVersions {
		v := b.AudioVersions[i]
		if v.ID == versionID && v.File != nil && v.File.Path != "" {
			return Item{Key: key, Book: b, Version: &v, Path: v.File.Path, Title: v.FolderTitle(b.Title), AddedAt: added}, nil
		}
	}
	return Item{}, errItemNotFound
}

func parseAdded(s string) int64 {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

// Author and series ids are derived from their names, so they're stable without a table.
func authorID(name string) string { return "au" + shortHash(strings.ToLower(strings.TrimSpace(name))) }
func seriesID(name string) string { return "se" + shortHash(strings.ToLower(strings.TrimSpace(name))) }

func shortHash(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:6])
}

// splitAuthors turns "A & B" / "A, B" / "A and B" into names.
func splitAuthors(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, sep := range []string{" & ", "; ", ", ", " and "} {
		s = strings.ReplaceAll(s, sep, "\x00")
	}
	var out []string
	for _, p := range strings.Split(s, "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sortTitle drops a leading article, as Audiobookshelf does for sorting.
func sortTitle(t string) string {
	l := strings.ToLower(strings.TrimSpace(t))
	for _, a := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(l, a) {
			return l[len(a):]
		}
	}
	return l
}

func seriesSequence(b books.Book) string {
	if b.SeriesPosition <= 0 {
		return ""
	}
	return strconv.FormatFloat(b.SeriesPosition, 'f', -1, 64)
}

// sortItems orders items by an Audiobookshelf sort key.
func sortItems(items []Item, key string, desc bool, dur func(Item) float64) {
	less := func(i, j int) bool {
		a, b := items[i], items[j]
		switch key {
		case "media.metadata.authorName", "media.metadata.authorNameLF":
			if x, y := strings.ToLower(a.Book.Author), strings.ToLower(b.Book.Author); x != y {
				return x < y
			}
		case "addedAt", "mtimeMs", "birthtimeMs", "updatedAt":
			if a.AddedAt != b.AddedAt {
				return a.AddedAt < b.AddedAt
			}
		case "media.duration":
			if dur != nil {
				if x, y := dur(a), dur(b); x != y {
					return x < y
				}
			}
		case "media.metadata.publishedYear":
			if a.Book.Year != b.Book.Year {
				return a.Book.Year < b.Book.Year
			}
		case "sequence", "media.metadata.series.sequence":
			if a.Book.SeriesPosition != b.Book.SeriesPosition {
				return a.Book.SeriesPosition < b.Book.SeriesPosition
			}
		}
		return sortTitle(a.Title) < sortTitle(b.Title)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if desc {
			return less(j, i)
		}
		return less(i, j)
	})
}

// Warm probes every audiobook's files ahead of time, so an app opening a book gets its
// chapters and durations at once rather than waiting on ffprobe. Cached files cost a
// database read each; it pauses briefly between books to stay out of the way.
func (s *Server) Warm(ctx context.Context) (probed int) {
	if !s.warming.CompareAndSwap(false, true) {
		return 0
	}
	defer s.warming.Store(false)
	items, err := s.items(ctx)
	if err != nil {
		return 0
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return probed
		}
		files, _ := s.probe.files(ctx, it.Path, false)
		known := len(files) > 0
		for _, f := range files {
			if f.Duration <= 0 {
				known = false
			}
		}
		if known {
			continue
		}
		_, _ = s.probe.files(ctx, it.Path, true)
		probed++
		select {
		case <-ctx.Done():
			return probed
		case <-time.After(200 * time.Millisecond):
		}
	}
	return probed
}

// LibraryStats counts the audiobooks served and how many have been probed.
func (s *Server) LibraryStats(ctx context.Context) (items, ready int) {
	list, err := s.items(ctx)
	if err != nil {
		return 0, 0
	}
	for _, it := range list {
		files, _ := s.probe.files(ctx, it.Path, false)
		ok := len(files) > 0
		for _, f := range files {
			if f.Duration <= 0 {
				ok = false
			}
		}
		if ok {
			ready++
		}
	}
	return len(list), ready
}

// ItemKey returns the key an app uses for a book's standard audiobook or a version.
func ItemKey(bookID, versionID int64) string { return itemKeyFor(bookID, versionID) }

// ItemInfo is what Arrmada's own pages need about an item.
type ItemInfo struct {
	Key       string `json:"item_key"`
	BookID    int64  `json:"book_id"`
	VersionID int64  `json:"version_id,omitempty"`
	Title     string `json:"title"`
	Author    string `json:"author,omitempty"`
	CoverURL  string `json:"cover_url,omitempty"`
}

// Info resolves an item key for display.
func (s *Server) Info(ctx context.Context, key string) (ItemInfo, bool) {
	it, err := s.item(ctx, key)
	if err != nil {
		return ItemInfo{}, false
	}
	var vid int64
	if it.Version != nil {
		vid = it.Version.ID
	}
	return ItemInfo{Key: key, BookID: it.Book.ID, VersionID: vid, Title: it.Title, Author: it.Book.Author, CoverURL: it.Book.CoverURL}, true
}
