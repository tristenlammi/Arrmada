package httpapi

import (
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
)

// A requester's book arrives in the library and then… nothing: the ebook sits on the
// server where only staff can see it. Audiobooks reach people through Audiobookshelf;
// ebooks had no way out. Now a requester has a "Your books" page listing the books
// they asked for, and a download for each ebook that has arrived. The download is
// keyed to the request — someone can fetch the ebooks they requested, nothing else —
// and it is one of the few endpoints reachable from outside the network, so a phone
// on the bus gets the book too.

// MyBook is one of the caller's requested books, with what has arrived for it.
type MyBook struct {
	BookID      int64  `json:"book_id"`
	Title       string `json:"title"`
	Author      string `json:"author,omitempty"`
	Year        int    `json:"year,omitempty"`
	CoverURL    string `json:"cover_url,omitempty"`
	Status      string `json:"status"` // the request's: pending | approved | declined
	RequestedAt string `json:"requested_at"`
	// Ebook is set once an ebook edition exists; Audiobook says one exists (served by
	// Audiobookshelf, so no download here).
	Ebook     *MyEbook `json:"ebook,omitempty"`
	Audiobook bool     `json:"audiobook"`
}

// MyEbook is what the download button needs to know.
type MyEbook struct {
	Format    string `json:"format"`
	SizeBytes int64  `json:"size_bytes"`
}

// handleMyBooks lists the caller's own book requests joined with the library.
//
//	GET /api/v1/me/books
func (a *api) handleMyBooks(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	reqs, err := a.deps.Requests.List(r.Context(), "", u.ID)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list requests")
		return
	}
	byKey := map[string]books.Book{}
	if a.deps.Books != nil {
		if all, err := a.deps.Books.List(r.Context()); err == nil {
			for _, b := range all {
				byKey[b.OLKey] = b
			}
		}
	}
	out := []MyBook{}
	for _, rq := range reqs {
		if rq.MediaType != "book" {
			continue
		}
		mb := MyBook{Title: rq.Title, Author: rq.Author, Year: rq.Year, CoverURL: rq.PosterURL, Status: rq.Status, RequestedAt: rq.CreatedAt}
		if b, ok := byKey[rq.OLKey]; ok {
			mb.BookID = b.ID
			if b.Title != "" {
				mb.Title, mb.Author = b.Title, b.Author
			}
			if b.CoverURL != "" {
				mb.CoverURL = b.CoverURL
			}
			if b.Ebook != nil && b.Ebook.Path != "" {
				mb.Ebook = &MyEbook{Format: b.Ebook.Format, SizeBytes: b.Ebook.SizeBytes}
			}
			mb.Audiobook = b.Audiobook != nil && b.Audiobook.Path != ""
		}
		out = append(out, mb)
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"books": out})
}

// handleBookEbook sends a book's ebook file as a download. Staff may fetch any; anyone
// else only a book they requested themselves.
//
//	GET /api/v1/books/{id}/ebook
func (a *api) handleBookEbook(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	b, err := a.deps.Books.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !a.mayDownloadBook(r, b) {
		a.writeError(w, http.StatusForbidden, "you can download the books you requested")
		return
	}
	if b.Ebook == nil || b.Ebook.Path == "" {
		a.writeError(w, http.StatusNotFound, "no ebook has arrived for this book yet")
		return
	}
	path, err := ebookFile(b.Ebook.Path)
	if err != nil {
		a.deps.Log.Warn("ebook download: file missing", "book", b.Title, "path", b.Ebook.Path, "err", err)
		a.writeError(w, http.StatusNotFound, "the ebook file is missing on disk")
		return
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ct := ebookContentType(ext); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Disposition", attachmentHeader(downloadName(b, ext)))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeFile(w, r, path)
}

// mayDownloadBook: staff always; others when one of their requests is for this book.
func (a *api) mayDownloadBook(r *http.Request, b books.Book) bool {
	u, ok := userFrom(r)
	if !ok || u == nil || u.Disabled {
		return false
	}
	if u.Role.AtLeast(auth.RoleManager) {
		return true
	}
	reqs, err := a.deps.Requests.List(r.Context(), "", u.ID)
	if err != nil {
		return false
	}
	for _, rq := range reqs {
		if rq.MediaType == "book" && rq.OLKey != "" && rq.OLKey == b.OLKey {
			return true
		}
	}
	return false
}

// ebookFile resolves the stored ebook path to the file to send. An ebook edition is
// one file, but should it ever be a folder, the best-known format inside wins.
func ebookFile(stored string) (string, error) {
	st, err := os.Stat(stored)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return stored, nil
	}
	entries, err := os.ReadDir(stored)
	if err != nil {
		return "", err
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() && ebookContentType(strings.ToLower(filepath.Ext(e.Name()))) != "" {
			found = append(found, e.Name())
		}
	}
	if len(found) == 0 {
		return "", os.ErrNotExist
	}
	sort.SliceStable(found, func(i, j int) bool {
		return ebookRank(found[i]) < ebookRank(found[j])
	})
	return filepath.Join(stored, found[0]), nil
}

// ebookRank orders formats for the pick above: the ones most readers open first.
func ebookRank(name string) int {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".epub":
		return 0
	case ".azw3", ".kepub":
		return 1
	case ".mobi", ".azw":
		return 2
	case ".pdf":
		return 3
	}
	return 4
}

// ebookContentType maps an ebook extension to its media type ("" = not an ebook).
func ebookContentType(ext string) string {
	switch ext {
	case ".epub", ".kepub":
		return "application/epub+zip"
	case ".mobi":
		return "application/x-mobipocket-ebook"
	case ".azw", ".azw3":
		return "application/vnd.amazon.mobi8-ebook"
	case ".pdf":
		return "application/pdf"
	}
	return ""
}

// downloadName is "Title - Author.ext" with anything a filesystem or header would
// choke on removed.
func downloadName(b books.Book, ext string) string {
	name := b.Title
	if b.Author != "" {
		name += " - " + b.Author
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == 0x7f:
			return -1
		case strings.ContainsRune(`/\:*?"<>|`, r):
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.Join(strings.Fields(name), " "))
	if name == "" {
		name = "book"
	}
	return name + ext
}

// attachmentHeader builds a Content-Disposition that survives non-ASCII titles: Go
// encodes those per RFC 2231, and a name it still can't express falls back to the
// bare extension rather than a broken header.
func attachmentHeader(name string) string {
	if h := mime.FormatMediaType("attachment", map[string]string{"filename": name}); h != "" {
		return h
	}
	return `attachment; filename="book` + filepath.Ext(name) + `"`
}
