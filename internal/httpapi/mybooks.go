package httpapi

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// A book arrives in the library and then… nothing: the ebook sits on the server
// where only staff can see it. Audiobooks reach people through Audiobookshelf; ebooks
// had no way out. Now a requester has a read-only "Books" page: every book in the
// library that has a file, a download for each ebook, and their own requests still on
// the way. The list and the download are two of the few endpoints reachable from
// outside the network, so a phone on the bus gets the book too.

// MyBook is one library book as a requester sees it: what it is and what's here.
type MyBook struct {
	BookID   int64  `json:"book_id"`
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	Year     int    `json:"year,omitempty"`
	CoverURL string `json:"cover_url,omitempty"`
	AddedAt  string `json:"added_at,omitempty"`
	Series   string `json:"series,omitempty"` // "Name #3" when known
	// Ebook is set when an ebook edition exists; Audiobook says one exists (served by
	// Audiobookshelf, so no download here).
	Ebook     *MyEbook `json:"ebook,omitempty"`
	Audiobook bool     `json:"audiobook"`
	// Audiobooks lists what can be downloaded: the standard audiobook (version_id 0)
	// and any extra versions ("Full cast").
	Audiobooks []MyAudiobook `json:"audiobooks,omitempty"`
	Mine       bool          `json:"mine"` // the caller requested this one
}

// MyAudiobook is one downloadable audiobook of a book.
type MyAudiobook struct {
	VersionID int64  `json:"version_id"`
	Label     string `json:"label,omitempty"`
	Format    string `json:"format"`
	SizeBytes int64  `json:"size_bytes"`
	Files     int    `json:"files"`
}

// MyRequest is one of the caller's book requests that hasn't produced a file yet.
type MyRequest struct {
	Title       string `json:"title"`
	Author      string `json:"author,omitempty"`
	Year        int    `json:"year,omitempty"`
	CoverURL    string `json:"cover_url,omitempty"`
	Status      string `json:"status"` // pending | approved | declined
	RequestedAt string `json:"requested_at"`
	// Stage, Note and NextCheckAt are where the request has got to (requests.Tracking):
	// "searching" with "Not found yet" and the next check's time (RFC3339) for a book the
	// searches keep missing. Stage is left out when the download client couldn't be read.
	Stage       string `json:"stage,omitempty"`
	Note        string `json:"note,omitempty"`
	NextCheckAt string `json:"next_check_at,omitempty"`
}

// MyEbook is what the download button needs to know.
type MyEbook struct {
	Format    string `json:"format"`
	SizeBytes int64  `json:"size_bytes"`
}

// handleMyBooks lists the library's books that have a file, plus the caller's own
// book requests that haven't arrived yet.
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
	// By the library row a request is linked to, else by the catalogue key it was made
	// under: a re-match changes the book's key, and the request must still find it.
	mine := map[string]bool{}
	mineID := map[int64]bool{}
	for _, rq := range reqs {
		if rq.MediaType != "book" {
			continue
		}
		if rq.BookID > 0 {
			mineID[rq.BookID] = true
		} else if rq.OLKey != "" {
			mine[rq.OLKey] = true
		}
	}
	var all []books.Book
	if a.deps.Books != nil {
		all, _ = a.deps.Books.List(r.Context())
	}
	shelf := []MyBook{}
	have := map[string]bool{}
	haveID := map[int64]bool{}
	for _, b := range all {
		ebook := b.Ebook != nil && b.Ebook.Path != ""
		audio := b.Audiobook != nil && b.Audiobook.Path != ""
		var audiobooks []MyAudiobook
		if audio {
			audiobooks = append(audiobooks, MyAudiobook{Format: b.Audiobook.Format, SizeBytes: b.Audiobook.SizeBytes, Files: b.Audiobook.FileCount})
		}
		for _, v := range b.AudioVersions {
			if v.File != nil && v.File.Path != "" {
				audiobooks = append(audiobooks, MyAudiobook{VersionID: v.ID, Label: v.Label, Format: v.File.Format,
					SizeBytes: v.File.SizeBytes, Files: v.File.FileCount})
			}
		}
		audio = len(audiobooks) > 0
		if !ebook && !audio {
			continue
		}
		have[b.OLKey], haveID[b.ID] = true, true
		mb := MyBook{BookID: b.ID, Title: b.Title, Author: b.Author, Year: b.Year, CoverURL: b.CoverURL,
			AddedAt: b.AddedAt, Audiobook: audio, Audiobooks: audiobooks, Mine: mineID[b.ID] || mine[b.OLKey]}
		if ebook {
			mb.Ebook = &MyEbook{Format: b.Ebook.Format, SizeBytes: b.Ebook.SizeBytes}
		}
		if b.SeriesName != "" {
			mb.Series = b.SeriesName
			if b.SeriesPosition > 0 {
				mb.Series = fmt.Sprintf("%s #%g", b.SeriesName, b.SeriesPosition)
			}
		}
		shelf = append(shelf, mb)
	}
	var open []requests.Request
	for _, rq := range reqs {
		if rq.MediaType != "book" || haveID[rq.BookID] || (rq.BookID == 0 && have[rq.OLKey]) {
			continue
		}
		open = append(open, rq)
	}
	// Where each one has got to, from its grabs and the download queue — read once. A
	// queue that can't be read leaves the stage out rather than calling a downloading
	// book "searching".
	tracked := false
	if len(open) > 0 && a.deps.Downloads != nil {
		if queue, qerr := a.deps.Downloads.Queue(r.Context()); qerr == nil {
			a.deps.Requests.Track(r.Context(), open, queue)
			tracked = true
		}
	}
	pending := []MyRequest{}
	for _, rq := range open {
		mr := MyRequest{Title: rq.Title, Author: rq.Author, Year: rq.Year,
			CoverURL: rq.PosterURL, Status: rq.Status, RequestedAt: rq.CreatedAt}
		if t := rq.Tracking; tracked && t != nil {
			mr.Stage, mr.Note, mr.NextCheckAt = t.Stage, t.Note, t.NextCheckAt
		}
		pending = append(pending, mr)
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"books": shelf, "requests": pending})
}

// handleBookEbook sends a book's ebook file as a download to any signed-in account
// that can request (read-only accounts are for watching the calendar, not the shelf).
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
		a.writeError(w, http.StatusForbidden, "your account can't download books")
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

// mayDownloadBook: any enabled account of requester rank or above. The whole shelf is
// theirs to read — the library exists to be read — so there is no per-book gate.
func (a *api) mayDownloadBook(r *http.Request, _ books.Book) bool {
	u, ok := userFrom(r)
	return ok && u != nil && !u.Disabled && u.Role.AtLeast(auth.RoleRequester)
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
