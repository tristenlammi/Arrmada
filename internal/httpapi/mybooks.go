package httpapi

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/attachment"
	"github.com/tristenlammi/arrmada/internal/audioserver"
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
	// The shelf's book sheet and its series rows: the description (capped, see
	// shelfDescription), the series and the place in it apart (position 0 = unknown), and
	// the first few subjects.
	Description    string   `json:"description,omitempty"`
	SeriesName     string   `json:"series_name,omitempty"`
	SeriesPosition float64  `json:"series_position,omitempty"`
	Subjects       []string `json:"subjects,omitempty"`
	// Ebook is set when an ebook edition exists; Audiobook says one exists (served by
	// Audiobookshelf, so no download here).
	Ebook     *MyEbook `json:"ebook,omitempty"`
	Audiobook bool     `json:"audiobook"`
	// Audiobooks lists what can be downloaded: the standard audiobook (version_id 0)
	// and any extra versions ("Full cast").
	Audiobooks []MyAudiobook `json:"audiobooks,omitempty"`
	Mine       bool          `json:"mine"` // the caller requested this one
}

// MyAudiobook is one downloadable audiobook of a book. ItemKey is the same audiobook in
// the listening API (/api/v1/me/audio/items/{key}), so the shelf's Listen button plays it.
type MyAudiobook struct {
	VersionID int64  `json:"version_id"`
	ItemKey   string `json:"item_key"`
	Label     string `json:"label,omitempty"`
	Format    string `json:"format"`
	SizeBytes int64  `json:"size_bytes"`
	Files     int    `json:"files"`
}

// myAudiobooks lists a book's audiobooks that have a file: the standard one first, then
// each extra version.
func myAudiobooks(b books.Book) []MyAudiobook {
	var out []MyAudiobook
	if b.Audiobook != nil && b.Audiobook.Path != "" {
		out = append(out, MyAudiobook{ItemKey: audioserver.ItemKey(b.ID, 0), Format: b.Audiobook.Format,
			SizeBytes: b.Audiobook.SizeBytes, Files: b.Audiobook.FileCount})
	}
	for _, v := range b.AudioVersions {
		if v.File != nil && v.File.Path != "" {
			out = append(out, MyAudiobook{VersionID: v.ID, ItemKey: audioserver.ItemKey(b.ID, v.ID), Label: v.Label,
				Format: v.File.Format, SizeBytes: v.File.SizeBytes, Files: v.File.FileCount})
		}
	}
	return out
}

// MyRequest is one of the caller's book requests that hasn't produced a file yet.
type MyRequest struct {
	ID          int64  `json:"id"` // opens it on the Requests page
	Title       string `json:"title"`
	Author      string `json:"author,omitempty"`
	Year        int    `json:"year,omitempty"`
	CoverURL    string `json:"cover_url,omitempty"`
	Status      string `json:"status"` // pending | approved | declined
	RequestedAt string `json:"requested_at"`
	Formats     string `json:"formats,omitempty"` // what was asked for: ebook | audiobook | both ("" on an older request)
	Waiting     string `json:"waiting,omitempty"` // which of those is still on the way: ebook | audiobook | both
	// Stage, Note and NextCheckAt are where the request has got to (requests.Tracking):
	// "searching" with "Not found yet" and the next check's time (RFC3339) for a book the
	// searches keep missing. Stage is left out when the download client couldn't be read.
	Stage       string `json:"stage,omitempty"`
	Note        string `json:"note,omitempty"`
	NextCheckAt string `json:"next_check_at,omitempty"`
	// Tracking is the whole of it, in the shape the Requests page reads (lib/stages'
	// vocabulary, mapped for requesters by internal/pipeline): "Downloading 40% · 2h left".
	// Absent when nothing could be tracked; with queue_known false, a stage read off the
	// download queue isn't to be trusted.
	Tracking *requests.Tracking `json:"tracking,omitempty"`
	// DeclineReason is what staff said when they turned it down (it's the caller's own).
	DeclineReason string `json:"decline_reason,omitempty"`
	// ReleaseDate: the book isn't out yet; the day it is (YYYY-MM-DD), for "Out 12 Mar".
	ReleaseDate string `json:"release_date,omitempty"`
}

// shelfDescriptionMax caps a description on the shelf: the whole shelf comes in one
// answer, so a library of long blurbs would weigh on a phone. The sheet shows the start.
const shelfDescriptionMax = 1500

// shelfDescription is a book's description, cut at a word near shelfDescriptionMax.
func shelfDescription(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= shelfDescriptionMax {
		return s
	}
	cut := string(r[:shelfDescriptionMax])
	if i := strings.LastIndexAny(cut, " \n"); i > shelfDescriptionMax/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:\n") + "…"
}

// MyEbook is what the download and Send to Kindle buttons need to know.
type MyEbook struct {
	Format    string `json:"format"`
	SizeBytes int64  `json:"size_bytes"`
	// Kindle: Amazon's Send to Kindle email takes this format (books.KindleAccepts), so the
	// button is offered; otherwise it's shown disabled with the format named.
	Kindle bool `json:"kindle"`
	// Kepub: "Download for Kobo (KEPUB)" is offered — an EPUB, with the conversion on.
	Kepub bool `json:"kepub,omitempty"`
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
	reqs, _, err := a.deps.Requests.List(r.Context(), requests.ListFilter{UserID: u.ID, MediaType: "book"})
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
	kepubOn := a.kepubReady(r.Context())
	have := map[string]bool{}
	haveID := map[int64]bool{}
	for _, b := range all {
		ebook := b.Ebook != nil && b.Ebook.Path != ""
		audiobooks := myAudiobooks(b)
		audio := len(audiobooks) > 0
		if !ebook && !audio {
			continue
		}
		have[b.OLKey], haveID[b.ID] = true, true
		mb := MyBook{BookID: b.ID, Title: b.Title, Author: b.Author, Year: b.Year, CoverURL: b.CoverURL,
			AddedAt: b.AddedAt, Audiobook: audio, Audiobooks: audiobooks, Mine: mineID[b.ID] || mine[b.OLKey],
			Description: shelfDescription(b.Description), SeriesName: b.SeriesName, SeriesPosition: b.SeriesPosition}
		if len(b.Subjects) > 0 {
			mb.Subjects = b.Subjects[:min(3, len(b.Subjects))]
		}
		if ebook {
			mb.Ebook = &MyEbook{Format: b.Ebook.Format, SizeBytes: b.Ebook.SizeBytes, Kindle: books.KindleAccepts(b.Ebook.Format),
				Kepub: kepubOn && kepubable(b.Ebook)}
		}
		if b.SeriesName != "" {
			mb.Series = b.SeriesName
			if b.SeriesPosition > 0 {
				mb.Series = fmt.Sprintf("%s #%g", b.SeriesName, b.SeriesPosition)
			}
		}
		shelf = append(shelf, mb)
	}
	// Still on the way until every format it asked for is here (Available, set per format
	// by List): a "both" request with only its ebook stays listed, saying the audiobook is
	// coming. A request from before the format choice is here once its book is on the shelf.
	var open []requests.Request
	for _, rq := range reqs {
		if rq.MediaType != "book" || rq.Available {
			continue
		}
		if rq.Formats == "" && (haveID[rq.BookID] || (rq.BookID == 0 && have[rq.OLKey])) {
			continue
		}
		open = append(open, rq)
	}
	// Where each one has got to, from its grabs and the download queue — read once. A
	// queue that can't be read leaves the older stage fields out rather than calling a
	// downloading book "searching", and says so (queue_known) for the tracking.
	tracked, queueKnown := false, true
	if len(open) > 0 && a.deps.Downloads != nil {
		queue, qerr := a.deps.Downloads.Queue(r.Context())
		queueKnown = qerr == nil
		a.deps.Requests.Track(r.Context(), open, queue, queueKnown)
		tracked = queueKnown
	}
	// Which of the formats asked for is still coming, from the library row itself, so the
	// shelf can say "Audiobook on the way" even when the download client can't be read.
	byID := map[int64]books.Book{}
	for _, b := range all {
		byID[b.ID] = b
	}
	var byKey map[string]books.Book
	if len(open) > 0 && a.deps.Books != nil {
		byKey = a.deps.Books.KeyIndex(r.Context(), all)
	}
	pending := []MyRequest{}
	for _, rq := range open {
		mr := MyRequest{ID: rq.ID, Title: rq.Title, Author: rq.Author, Year: rq.Year,
			CoverURL: rq.PosterURL, Status: rq.Status, RequestedAt: rq.CreatedAt, Formats: rq.Formats,
			Tracking: rq.Tracking, DeclineReason: rq.DeclineReason}
		b, ok := byID[rq.BookID]
		if !ok {
			b = byKey[rq.OLKey]
		}
		mr.Waiting = requests.StillComing(rq.Formats, b)
		if t := rq.Tracking; tracked && t != nil {
			mr.Stage, mr.Note, mr.NextCheckAt = t.Stage, t.Note, t.NextCheckAt
		}
		if b.ID > 0 && !books.Released(b.ReleaseDate, time.Now()) {
			mr.ReleaseDate = b.ReleaseDate
		}
		pending = append(pending, mr)
	}
	// listen: the caller may play audiobooks here (the audiobook server is on and their
	// account is allowed), so the shelf offers Listen beside the downloads.
	listen := a.deps.AudioServer != nil && a.deps.Settings.GetBool(r.Context(), audioserver.KeyEnabled, false) &&
		a.deps.AudioServer.Allowed(r.Context(), u)
	a.writeJSON(w, http.StatusOK, map[string]any{"books": shelf, "requests": pending, "listen": listen, "queue_known": queueKnown})
}

// handleBookEbook sends a book's ebook file as a download to any signed-in account
// that can request (read-only accounts are for watching the calendar, not the shelf).
// ?format=kepub sends an EPUB converted for Kobo's stock reader (kepub.go), or the EPUB
// itself when that can't be done.
//
//	GET /api/v1/books/{id}/ebook[?format=kepub]
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
	path, err := books.EbookFile(b.Ebook.Path)
	if err != nil {
		a.deps.Log.Warn("ebook download: file missing", "book", b.Title, "path", b.Ebook.Path, "err", err)
		a.writeError(w, http.StatusNotFound, "the ebook file is missing on disk")
		return
	}
	if r.URL.Query().Get("format") == "kepub" && a.serveKepub(w, r, b, path) {
		return
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ct := books.EbookContentType(ext); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Disposition", attachment.Header(books.EbookDownloadName(b, ext)))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeFile(w, r, path)
}

// mayDownloadBook: any enabled account of requester rank or above. The whole shelf is
// theirs to read — the library exists to be read — so there is no per-book gate.
func (a *api) mayDownloadBook(r *http.Request, _ books.Book) bool {
	u, ok := userFrom(r)
	return ok && u != nil && !u.Disabled && u.Role.AtLeast(auth.RoleRequester)
}
