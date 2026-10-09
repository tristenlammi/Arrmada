package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/adultfilter"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/requests"
)

func (a *api) handleListBooks(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Books.List(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list books")
		return
	}
	if list == nil {
		list = []books.Book{}
	}
	// Fill the wanted-edition flags here too, not just on the detail page. Without them
	// the library can't tell "no audiobook because none was ever wanted" from "no
	// audiobook and one is missing" — and only the second is worth showing in a filter.
	// Profiles are resolved once each rather than once per book: a library shares a
	// handful of them across hundreds of titles.
	wants := map[string][2]bool{}
	for i := range list {
		ref := list[i].QualityProfile
		w2, ok := wants[ref]
		if !ok {
			enriched := list[i]
			a.enrichBookWants(r, &enriched)
			w2 = [2]bool{enriched.WantEbook, enriched.WantAudiobook}
			wants[ref] = w2
		}
		list[i].WantEbook, list[i].WantAudiobook = w2[0], w2[1]
		list[i].FillNextSearch()
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"books":              list,
		"metadata_available": a.deps.Books.MetadataAvailable(),
		"metadata_source":    a.deps.Books.MetadataSource(),
		"upgradable":         a.deps.Books.Upgradable(r.Context()), // books not yet on Hardcover keys
	})
}

// handleStartBookUpgrade re-matches every non-Hardcover book to Hardcover, in the background.
func (a *api) handleStartBookUpgrade(w http.ResponseWriter, r *http.Request) {
	// A books.upgrade job; the claim is taken here so the status below already says running.
	jobID, started, err := a.deps.Books.SubmitUpgrade(a.runCtx(), a.jobSubmitter(), triggerFor(r))
	if err != nil {
		a.writeError(w, http.StatusServiceUnavailable, "couldn't start the re-match just now — try again in a moment")
		return
	}
	if !started {
		a.writeJSON(w, http.StatusOK, map[string]any{"started": false, "status": a.deps.Books.UpgradeStatus()})
		return
	}
	a.accepted(w, jobID, false, map[string]any{"started": true, "status": a.deps.Books.UpgradeStatus()})
}

func (a *api) handleBookUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Books.UpgradeStatus())
}

// handleStartBookSweep searches for every monitored book's missing editions — the
// manual, back-off-free sweep. It fills gaps only; nothing on disk is replaced.
func (a *api) handleStartBookSweep(w http.ResponseWriter, r *http.Request) {
	jobID, started, err := a.deps.Automation.SubmitBookSweep(a.runCtx(), a.jobSubmitter(), triggerFor(r))
	if err != nil {
		a.writeError(w, http.StatusServiceUnavailable, "couldn't start the sweep just now — try again in a moment")
		return
	}
	if !started {
		a.writeJSON(w, http.StatusOK, map[string]any{"started": false, "status": a.deps.Automation.BookSweepStatus()})
		return
	}
	a.accepted(w, jobID, false, map[string]any{"started": true, "status": a.deps.Automation.BookSweepStatus()})
}

func (a *api) handleBookSweepStatus(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Automation.BookSweepStatus())
}

func (a *api) handleLookupBooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		a.writeError(w, http.StatusBadRequest, "missing ?q= query")
		return
	}
	// ?source=openlibrary asks Open Library explicitly (the "show Open Library results
	// too" button); otherwise the current catalogue answers.
	results, err := a.deps.Books.LookupFrom(r.Context(), q, r.URL.Query().Get("source"))
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// Deliberately unfiltered: this is the staff Add-book search, where an exact search
	// can still find a rare legitimate title the adult filter would hide from Discover.
	a.writeJSON(w, http.StatusOK, map[string]any{"results": withoutTags(results), "source": a.deps.Books.MetadataSource()})
}

func (a *api) handleAddBook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OLKey          string `json:"ol_key"`
		QualityProfile string `json:"quality_profile"`
		Monitored      *bool  `json:"monitored"`
		SearchOnAdd    *bool  `json:"search_on_add"`
		Title          string `json:"title"`
		Author         string `json:"author"`
		Year           int    `json:"year"`
		CoverURL       string `json:"cover_url"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.OLKey == "" {
		a.writeError(w, http.StatusBadRequest, "ol_key is required")
		return
	}
	monitored := true
	if req.Monitored != nil {
		monitored = *req.Monitored
	}
	searchOnAdd := a.deps.Settings.GetBool(r.Context(), keySearchOnAdd, true)
	if req.SearchOnAdd != nil {
		searchOnAdd = *req.SearchOnAdd
	}
	if !searchOnAdd {
		monitored = false
	}
	if req.QualityProfile == "" {
		req.QualityProfile = a.deps.Quality.DefaultProfile(r.Context(), "book")
	}
	b, err := a.deps.Books.Add(r.Context(), req.OLKey, req.QualityProfile, monitored, metadata.BookResult{
		Title: req.Title, Author: req.Author, Year: req.Year, CoverURL: req.CoverURL,
	})
	if errors.Is(err, books.ErrExists) {
		a.writeError(w, http.StatusConflict, "that book is already in your library")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if b.Monitored && searchOnAdd {
		_, _, _ = a.submit(r, triggered(automation.TriggerAdd, a.bookSearchJob(b.ID)))
	}
	a.writeJSON(w, http.StatusCreated, b)
}

func (a *api) handleGetBook(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	b, err := a.deps.Books.Get(r.Context(), id)
	if errors.Is(err, books.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "book not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load book")
		return
	}
	a.enrichBookWants(r, &b)
	setBookCatalogue(&b)
	a.setBookAliases(r.Context(), &b)
	a.writeJSON(w, http.StatusOK, b)
}

// setBookAliases lists the catalogue keys the book had before its current one, so the
// detail page can say where else it is known.
func (a *api) setBookAliases(ctx context.Context, b *books.Book) {
	keys, err := a.deps.Books.KeysFor(ctx, b.ID)
	if err != nil {
		return
	}
	for _, k := range keys {
		if k.Key != b.OLKey {
			b.Aliases = append(b.Aliases, k)
		}
	}
}

// enrichBookWants fills want_ebook/want_audiobook from the book's quality profile so
// the detail page can show wanted-but-missing editions, and next_search_at from them.
func (a *api) enrichBookWants(r *http.Request, b *books.Book) {
	w := a.bookWants(r.Context(), b.QualityProfile)
	b.WantEbook, b.WantAudiobook = w[0], w[1]
	b.FillNextSearch()
}

// setBookCatalogue names and links the catalogue the book's metadata came from, for the
// detail page's badge. No Hardcover slug is stored yet, so a Hardcover book links to a
// search for its title and author.
func setBookCatalogue(b *books.Book) {
	if ref := books.CatalogueLink(b.OLKey, "", b.Title, b.Author); ref.URL != "" {
		b.Catalogue = &ref
	}
}

// handleRefreshBook re-pulls metadata and rescans the disk for a book.
func (a *api) handleRefreshBook(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if _, err := a.deps.Books.Refresh(r.Context(), id); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not refresh book")
		return
	}
	a.deps.Automation.RescanBook(r.Context(), id)
	b, err := a.deps.Books.Get(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load book")
		return
	}
	a.enrichBookWants(r, &b)
	setBookCatalogue(&b)
	a.setBookAliases(r.Context(), &b)
	a.writeJSON(w, http.StatusOK, b)
}

// handleBookReleases runs an interactive search (ebook + audiobook) without grabbing.
func (a *api) handleBookReleases(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(indexer.WithInteractive(r.Context()), 90*time.Second)
	defer cancel()
	list, err := a.deps.Automation.RankBookReleases(ctx, id)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.tokenize(r, &list, automation.ReleaseRef{MediaKind: automation.ReleaseKindBook, MediaID: id})
	a.writeJSON(w, http.StatusOK, list)
}

// handleGrabBook grabs a chosen release for a book.
func (a *api) handleGrabBook(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Token string `json:"token"` // from this book's interactive search
		// 0 = standard; >0 = file as this audiobook version. The person picks it in the
		// modal ("grab as"), so it comes from the body; GrabForBook checks it's this book's.
		VersionID int64 `json:"version_id"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ref, ok := a.resolveRelease(w, r, req.Token, automation.ReleaseKindBook, id)
	if !ok {
		return
	}
	if err := a.deps.Automation.GrabForBook(r.Context(), id, req.VersionID, ref.Indexer, ref.DownloadURL, ref.Title); err != nil {
		a.writeGrabError(w, err, ref)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "grabbed", "title": ref.Title})
}

// handleBookManualImportList / handleBookManualImport handle picking an on-disk file.
func (a *api) handleBookManualImportList(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.pathID(w, r); !ok {
		return
	}
	dir, err := a.checkImportPath(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := importListContext(r)
	defer cancel()
	cands, truncated := a.deps.Automation.BookImportCandidates(ctx, dir)
	if cands == nil {
		cands = []automation.BookImportCandidate{}
	}
	a.writeImportList(w, r, ctx, "books", dir, cands, truncated)
}

func (a *api) handleBookManualImport(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Path      string `json:"path"`
		VersionID int64  `json:"version_id"`
	}
	if !a.decodeJSON(w, r, &req) || req.Path == "" {
		a.writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	src, err := a.checkImportPath(r.Context(), req.Path)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.deps.Automation.ManualImportBook(r.Context(), id, req.VersionID, src); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "imported"})
}

// handleBookRename renames single-file editions to their canonical path.
func (a *api) handleBookRename(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	moved, err := a.deps.Automation.BookRename(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not rename")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"renamed": moved})
}

// handleScanBookLibrary catalogs books already present in the library folder.
func (a *api) handleScanBookLibrary(w http.ResponseWriter, r *http.Request) {
	ebooks, audiobooks := a.libEbooks(r), a.libAudiobooks(r)
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "books.scan", Target: "all", Class: jobs.ClassLibraryScan, Timeout: 15 * time.Minute,
		Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
			res := a.deps.Automation.ScanBookLibrary(ctx, ebooks, audiobooks)
			a.deps.Log.Info("book library scan done", "imported", res.Imported, "skipped", res.Skipped)
			p.SetMessage(scanMessage("book", res.Imported, len(res.Unmatched)))
			return scanSummary{Imported: res.Imported, Skipped: res.Skipped, Unmatched: len(res.Unmatched)}, nil
		}})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "scanning"})
}

// handleBackfillBookSeries fills in the series for books already in the library — a
// one-off for a library assembled before Arrmada started recording it. Runs in the
// background: one indexer search per unlabelled book takes minutes, not a request.
func (a *api) handleBackfillBookSeries(w http.ResponseWriter, r *http.Request) {
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "books.backfill-series", Target: "all", Class: jobs.ClassIndexerSearch, Timeout: 30 * time.Minute,
		Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
			res, err := a.deps.Automation.BackfillBookSeries(ctx)
			if err != nil {
				a.deps.Log.Warn("book series backfill failed", "err", err,
					"scanned", res.Scanned, "learned", res.Learned)
				return res, err
			}
			a.deps.Log.Info("book series backfill finished", "scanned", res.Scanned, "learned", res.Learned)
			p.SetMessage(fmt.Sprintf("Found the series for %d of %d books", res.Learned, res.Scanned))
			return res, nil
		}})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "backfilling"})
}

// handleBookSeries returns the series a book belongs to, its siblings in reading order,
// and the numbered entries missing between them.
func (a *api) handleBookSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	res, err := a.deps.Automation.BookSeriesFor(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the series")
		return
	}
	if res.Entries == nil {
		res.Entries = []automation.BookSeriesEntry{} // [] not null, or the panel can't render
	}
	a.writeJSON(w, http.StatusOK, res)
}

// handleBookEditionFiles lists an edition's individual files (?edition=ebook|audiobook).
func (a *api) handleBookEditionFiles(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	kind := r.URL.Query().Get("edition")
	files := a.deps.Automation.EditionFiles(r.Context(), id, kind)
	if files == nil {
		files = []automation.BookFileEntry{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// handleMergeAudiobook combines a multi-file audiobook into a single chapterized .m4b.
func (a *api) handleMergeAudiobook(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if !a.deps.Automation.MergeAudiobookAvailable() {
		a.writeError(w, http.StatusBadRequest, "ffmpeg isn't available on the server, so audiobooks can't be merged")
		return
	}
	if a.deps.Automation.MergingAudiobook(id) {
		a.writeError(w, http.StatusConflict, "this audiobook is already being merged")
		return
	}
	// Refuse here what would stop before it started, so the page shows the reason now
	// rather than waiting on a merge that never ran.
	if err := a.deps.Automation.CheckAudiobookMerge(r.Context(), id); err != nil {
		if errors.Is(err, books.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, err.Error())
		} else {
			a.writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	// The outcome lands on the book as a 'merged' or 'merge-failed' event, which the page
	// watches for; the log line is for the server's side.
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "book.merge-audiobook", Target: jobTarget("book", id), Timeout: 30 * time.Minute,
		Fn: errFn(func(ctx context.Context) error { return a.deps.Automation.MergeAudiobook(ctx, id) })})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "merging"})
}

// handleDeleteBookFile removes one edition's file(s) (?edition=ebook|audiobook).
func (a *api) handleDeleteBookFile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	kind := r.URL.Query().Get("edition")
	if kind != books.KindEbook && kind != books.KindAudiobook {
		a.writeError(w, http.StatusBadRequest, "edition must be ebook or audiobook")
		return
	}
	if err := a.deps.Automation.DeleteBookEdition(r.Context(), id, kind); err != nil {
		if a.writeBinRefusal(w, err) {
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete file")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// handleBookCovers returns candidate cover images for the cover picker, from the book's
// own catalogue: Hardcover's editions for a Hardcover book, otherwise Open Library
// editions plus Google Books.
func (a *api) handleBookCovers(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	covers, err := a.deps.Books.Covers(ctx, id)
	if errors.Is(err, books.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "book not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not fetch covers")
		return
	}
	if covers == nil {
		covers = []string{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"covers": covers})
}

// handleSetBookCover sets the book's cover to a chosen (remote) URL from the picker.
func (a *api) handleSetBookCover(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		a.writeError(w, http.StatusBadRequest, "url must be an http(s) image URL")
		return
	}
	if err := a.deps.Books.SetCover(r.Context(), id, req.URL); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not set cover")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"cover_url": req.URL})
}

// handleUploadBookCover stores a custom cover image the user uploaded and points the book
// at it (served back via handleBookCoverImage).
func (a *api) handleUploadBookCover(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid upload")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if !allowedCoverExt(ext) {
		a.writeError(w, http.StatusBadRequest, "cover must be a JPG, PNG, WebP or GIF image")
		return
	}
	dir := filepath.Join(a.deps.Config.DataDir, "covers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not store cover")
		return
	}
	// Drop any previous custom cover for this book (the extension may differ).
	for _, old := range coverFiles(dir, id) {
		_ = os.Remove(old)
	}
	dst := filepath.Join(dir, fmt.Sprintf("book-%d%s", id, ext))
	out, err := os.Create(dst)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not store cover")
		return
	}
	if _, err := io.Copy(out, io.LimitReader(file, 16<<20)); err != nil {
		out.Close()
		_ = os.Remove(dst)
		a.writeError(w, http.StatusInternalServerError, "could not store cover")
		return
	}
	out.Close()
	// Cache-busted, root-absolute path so the <img> reloads after a re-upload.
	coverURL := fmt.Sprintf("/api/v1/books/%d/cover-image?v=%d", id, time.Now().Unix())
	if err := a.deps.Books.SetCover(r.Context(), id, coverURL); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save cover")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"cover_url": coverURL})
}

// handleBookCoverImage serves a custom uploaded cover from disk.
func (a *api) handleBookCoverImage(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	dir := filepath.Join(a.deps.Config.DataDir, "covers")
	matches := coverFiles(dir, id)
	if len(matches) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, matches[0])
}

// allowedCoverExt guards which image extensions may be uploaded as covers.
func allowedCoverExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return true
	}
	return false
}

// coverFiles returns any custom cover files stored on disk for a book id (there is at most
// one, but the extension varies).
func coverFiles(dir string, id int64) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, fmt.Sprintf("book-%d.*", id)))
	return matches
}

// --- Books Discover (Open Library browse/search + author catalogues) ---

// bookCard is a discover result annotated with the viewer-relevant library/request state
// so the UI can badge what's already owned or pending.
type bookCard struct {
	metadata.BookResult
	InLibrary     bool   `json:"in_library"`
	HasFile       bool   `json:"has_file"`
	Requested     bool   `json:"requested"`                // kept for compatibility: a pending request exists
	RequestStatus string `json:"request_status,omitempty"` // pending | approved | declined (mirrors discoverCard)

	// Per format, for a book in the library: which editions are on disk and which its
	// profile wants, so a card can read "Ebook ✓" and offer "Request audiobook". And
	// what the request badging the card asked for (ebook | audiobook | both; "" for one
	// made before the choice existed).
	HasEbook       bool   `json:"has_ebook"`
	HasAudiobook   bool   `json:"has_audiobook"`
	WantEbook      bool   `json:"want_ebook"`
	WantAudiobook  bool   `json:"want_audiobook"`
	RequestFormats string `json:"request_formats,omitempty"`
}

// enrichBookCards annotates search/browse results with library + request status.
//
// It is also the one funnel every Books Discover list passes through (browse, trending,
// Recommended, search, author works, subjects, similar), so the always-on adult filter
// runs here, for every role. A new Discover surface must come through here too.
func (a *api) enrichBookCards(ctx context.Context, results []metadata.BookResult) []bookCard {
	results = adultfilter.FilterBooks(results, bookFilterFields)
	// By catalogue key AND by what the book is (title + author): a library built on
	// Open Library must show its books as owned when the results come from Hardcover,
	// and a second Open Library "work" for the same novel must not look like a new book.
	// Every key a book has had counts (book_keys): a card still carrying a book's old
	// Open Library key after the Hardcover upgrade is that book.
	list, _ := a.deps.Books.List(ctx)
	byKey := a.deps.Books.KeyIndex(ctx, list)
	same := books.NewIdentityIndex(list)
	// Requests.List returns newest first; iterating in order and overwriting means the
	// OLDEST request would win, so only set a key on first sight — the newest request
	// for a book determines its badge (matching the movie/series discover behavior of
	// one-status-per-title, but declined stays distinguishable from never-requested).
	// A request linked to a library row also badges that row, so the card under the
	// book's new catalogue key still reads Requested after a re-match.
	type reqInfo struct{ status, formats string }
	reqStatus := map[string]reqInfo{}
	reqByBook := map[int64]reqInfo{}
	if reqs, err := a.deps.Requests.Records(ctx, requests.ListFilter{MediaType: "book"}); err == nil {
		for _, rq := range reqs {
			if rq.MediaType != "book" {
				continue
			}
			ri := reqInfo{rq.Status, rq.Formats}
			if _, seen := reqStatus[rq.OLKey]; !seen && rq.OLKey != "" {
				reqStatus[rq.OLKey] = ri
			}
			if _, seen := reqByBook[rq.BookID]; !seen && rq.BookID > 0 {
				reqByBook[rq.BookID] = ri
			}
		}
	}
	// Which editions a library row's profile wants, read once per profile.
	wants := map[string][2]bool{}
	wantsOf := func(ref string) [2]bool {
		w, ok := wants[ref]
		if !ok {
			w = [2]bool{true, false}
			if a.deps.Quality != nil {
				w[0], w[1] = a.deps.Quality.BookEditions(ctx, ref)
			}
			wants[ref] = w
		}
		return w
	}
	cards := make([]bookCard, 0, len(results))
	for _, br := range results {
		br.Tags = nil // filter-only; br is a copy, so the cached list keeps them
		ri := reqStatus[br.Key]
		row, in := byKey[br.Key]
		if lb, ok := same.Find(br.Title, br.Author); ok && (!in || (lb.HasFile && !row.HasFile)) {
			row, in = lb, true // Find prefers the row with files
		}
		if ri.status == "" && in {
			ri = reqByBook[row.ID]
		}
		card := bookCard{
			BookResult:     br,
			InLibrary:      in,
			HasFile:        in && row.HasFile,
			Requested:      ri.status == "pending",
			RequestStatus:  ri.status,
			RequestFormats: ri.formats,
		}
		if in {
			card.HasEbook = row.Ebook != nil && row.Ebook.Path != ""
			card.HasAudiobook = row.Audiobook != nil && row.Audiobook.Path != ""
			for _, v := range row.AudioVersions {
				card.HasAudiobook = card.HasAudiobook || (v.File != nil && v.File.Path != "")
			}
			w := wantsOf(row.QualityProfile)
			card.WantEbook, card.WantAudiobook = w[0], w[1]
		}
		cards = append(cards, card)
	}
	return cards
}

// handleBookDiscoverBrowse returns one browse row: trending, new_releases, top_rated,
// popular. A row the catalogue can't produce is a 404 so the page hides it.
func (a *api) handleBookDiscoverBrowse(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := a.deps.Books.Browse(ctx, r.PathValue("kind"))
	if errors.Is(err, metadata.ErrNotSupported) {
		a.writeError(w, http.StatusNotFound, "not available from this catalogue")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not load books")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"books": a.enrichBookCards(ctx, res)})
}

// handleBookDiscoverRecommended returns the "Because you own …" rows.
func (a *api) handleBookDiscoverRecommended(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rows, err := a.deps.Books.Recommended(ctx)
	if err != nil && !errors.Is(err, metadata.ErrNotSupported) {
		a.writeError(w, http.StatusBadGateway, "could not build recommendations")
		return
	}
	type row struct {
		Title  string     `json:"title"`
		Seed   string     `json:"seed"`
		SeedID int64      `json:"seed_id"`
		Books  []bookCard `json:"books"`
	}
	out := make([]row, 0, len(rows))
	for _, rw := range rows {
		cards := a.enrichBookCards(ctx, rw.Books)
		if len(cards) == 0 && len(rw.Books) > 0 {
			continue // everything in it was filtered out: no empty strip
		}
		out = append(out, row{Title: rw.Title, Seed: rw.Seed, SeedID: rw.SeedID, Books: cards})
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"rows": out})
}

// handleBookDiscoverTrending returns books trending this week.
func (a *api) handleBookDiscoverTrending(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := a.deps.Books.Trending(ctx)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not load trending books")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"books": a.enrichBookCards(ctx, res)})
}

// handleBookDiscoverSearch searches both books (titles) and authors for the query.
func (a *api) handleBookDiscoverSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		a.writeError(w, http.StatusBadRequest, "missing ?q= query")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	source := r.URL.Query().Get("source")
	bookRes, err := a.deps.Books.LookupFrom(ctx, q, source)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	authors := []metadata.AuthorResult{}
	if source == "" {
		// Authors only for the primary search; the Open Library add-on is books only.
		if got, _ := a.deps.Books.SearchAuthors(ctx, q); got != nil { // best-effort; books are the main result
			authors = got
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"authors": authors,
		"books":   a.enrichBookCards(ctx, bookRes),
		"source":  a.deps.Books.MetadataSource(),
	})
}

// handleBookAuthorSearch finds authors by name (for the Add author picker).
func (a *api) handleBookAuthorSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		a.writeError(w, http.StatusBadRequest, "missing ?q= query")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	authors, err := a.deps.Books.SearchAuthors(ctx, q)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if authors == nil {
		authors = []metadata.AuthorResult{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"authors": authors})
}

// handleAddAuthor bulk-adds an author's entire official catalogue (individual books only —
// AuthorWorks is ranked + bundle-filtered) to the library, then searches for each.
func (a *api) handleAddAuthor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AuthorKey      string `json:"author_key"`
		QualityProfile string `json:"quality_profile"`
		Monitored      *bool  `json:"monitored"`
		SearchOnAdd    *bool  `json:"search_on_add"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.AuthorKey == "" {
		a.writeError(w, http.StatusBadRequest, "author_key is required")
		return
	}
	fetchCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	works, err := a.deps.Books.AuthorWorks(fetchCtx, req.AuthorKey, 0)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not load author's works")
		return
	}
	profile := req.QualityProfile
	if profile == "" {
		profile = a.deps.Quality.DefaultProfile(r.Context(), "book")
	}
	monitored := true
	if req.Monitored != nil {
		monitored = *req.Monitored
	}
	searchOnAdd := a.deps.Settings.GetBool(r.Context(), keySearchOnAdd, true)
	if req.SearchOnAdd != nil {
		searchOnAdd = *req.SearchOnAdd
	}
	if !searchOnAdd {
		monitored = false
	}
	added, skipped := a.deps.Books.AddWorks(r.Context(), works, profile, monitored)
	if monitored && searchOnAdd && len(added) > 0 {
		ids := make([]int64, len(added))
		for i, b := range added {
			ids[i] = b.ID
		}
		// One job for the batch, in the indexer-search class: a big catalogue waits its
		// turn behind searches already running rather than hammering the indexers.
		_, _, _ = a.submit(r, jobs.Spec{Kind: "books.add-author-search", Target: "author:" + req.AuthorKey, Class: jobs.ClassIndexerSearch, Timeout: 20 * time.Minute,
			Fn: errFn(func(ctx context.Context) error {
				for i, id := range ids {
					_, err := a.deps.Automation.SearchBookNow(automation.WithSearchTrigger(ctx, automation.TriggerAdd), id)
					if errors.Is(err, automation.ErrAlreadySearching) {
						continue // already being searched: that search covers it
					}
					if indexer.IsOutage(err) {
						// The indexers can't answer: the rest would only fail the same way. No miss
						// is recorded, so the scheduled sweep picks these books up once they're back.
						a.deps.Log.Warn("add author: no indexer could answer — leaving the rest to the scheduled sweep",
							"searched", i+1, "of", len(ids), "err", err)
						return nil
					}
					if err != nil {
						a.deps.Log.Warn("add author: search failed", "book_id", id, "err", err)
					}
					if ctx.Err() != nil {
						return ctx.Err() // shutting down, or out of time: the sweep finishes the rest
					}
				}
				return nil
			})})
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"added": len(added), "skipped": skipped, "total": len(works)})
}

// handleBookAuthorWorks returns an author's catalogue.
func (a *api) handleBookAuthorWorks(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		a.writeError(w, http.StatusBadRequest, "missing author key")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := a.deps.Books.AuthorWorks(ctx, key, 0)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not load author's works")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"author_key": key, "books": a.enrichBookCards(ctx, res)})
}

// handleBookDiscoverSubject returns books for a subject/genre.
func (a *api) handleBookDiscoverSubject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		a.writeError(w, http.StatusBadRequest, "missing subject")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := a.deps.Books.BySubject(ctx, name, 24)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not load subject")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"subject": name, "books": a.enrichBookCards(ctx, res)})
}

// handleAddMissingInSeries adds every entry of a book's series the library lacks.
func (a *api) handleAddMissingInSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		QualityProfile string `json:"quality_profile"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // body is optional
	if req.QualityProfile == "" {
		req.QualityProfile = a.deps.Quality.DefaultProfile(r.Context(), "book")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	added, skipped, err := a.deps.Books.AddMissingInSeries(ctx, id, req.QualityProfile, true)
	if errors.Is(err, metadata.ErrNotSupported) {
		a.writeError(w, http.StatusBadRequest, "this book's series isn't known to the catalogue")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not load the series")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"added": added, "skipped": skipped})
}

// handleBookAuthorDetail returns an author's photo and biography when the catalogue has them.
func (a *api) handleBookAuthorDetail(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	d, err := a.deps.Books.AuthorDetail(ctx, key)
	if errors.Is(err, metadata.ErrNotSupported) {
		a.writeError(w, http.StatusNotFound, "no author details from this catalogue")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not load the author")
		return
	}
	a.writeJSON(w, http.StatusOK, d)
}

// handleBookDiscoverSimilar returns the catalogue's similar-books list for a key.
func (a *api) handleBookDiscoverSimilar(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		a.writeError(w, http.StatusBadRequest, "missing ?key=")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := a.deps.Books.SimilarBooks(ctx, key)
	if err != nil {
		// Not an error for the page: the catalogue simply has none.
		a.writeJSON(w, http.StatusOK, map[string]any{"books": []bookCard{}})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"books": a.enrichBookCards(ctx, res)})
}

// handleBookAuthorImages returns a photo per library author, resolving a few unknown
// ones per call; pending says how many are still to look up.
func (a *api) handleBookAuthorImages(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	images, pending, err := a.deps.Books.AuthorImages(ctx)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load author images")
		return
	}
	if images == nil {
		images = map[string]string{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"images": images, "pending": pending})
}

// handleBookDiscoverDetail returns full metadata (description, subjects) for a work — the
// Discover request modal loads it lazily.
func (a *api) handleBookDiscoverDetail(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		a.writeError(w, http.StatusBadRequest, "missing ?key=")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	d, err := a.deps.Books.Detail(ctx, key)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not load book")
		return
	}
	// A flagged book is simply not there, however its key was found.
	if adultfilter.BookIsAdult(d.Title, bookDetailTags(d)) {
		a.writeError(w, http.StatusNotFound, "not available")
		return
	}
	out := *d // d may be the catalogue cache's own copy: don't touch it
	out.Tags = nil
	// The request sheet's Read / Listen / Both control starts on the owner's default book
	// profile's editions when the viewer hasn't picked one before.
	def := requests.FormatsEbook
	if a.deps.Quality != nil {
		def = requests.FormatsOf(a.deps.Quality.BookEditions(r.Context(), ""))
	}
	a.writeJSON(w, http.StatusOK, struct {
		metadata.BookDetails
		DefaultBookFormats string `json:"default_book_formats"`
	}{out, def})
}

// bookFilterFields is what the adult filter reads from a catalogue result: its title,
// and its shown genres plus every other label the catalogue gave it.
func bookFilterFields(b metadata.BookResult) (string, []string) {
	return b.Title, append(append([]string{}, b.Genres...), b.Tags...)
}

// bookDetailTags is every label on a full record: subjects, genres and tags.
func bookDetailTags(d *metadata.BookDetails) []string {
	_, tags := bookFilterFields(d.BookResult)
	return append(tags, d.Subjects...)
}

// withoutTags copies results with the filter-only Tags dropped, leaving the (possibly
// cached) originals alone.
func withoutTags(results []metadata.BookResult) []metadata.BookResult {
	out := make([]metadata.BookResult, len(results))
	for i, r := range results {
		r.Tags = nil
		out[i] = r
	}
	return out
}

// handleSetBookKeepCatalogue marks a book as one the Hardcover re-match leaves alone
// — the way to silence "no result matched" for a book the catalogue doesn't have.
func (a *api) handleSetBookKeepCatalogue(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Keep bool `json:"keep"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Books.SetKeepCatalogue(r.Context(), id, req.Keep); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update the book")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"keep": req.Keep})
}

func (a *api) handleSetBookMonitored(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Monitored bool `json:"monitored"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Books.SetMonitored(r.Context(), id, req.Monitored); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update monitoring")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"monitored": req.Monitored})
}

// handleOverrideBookMetadata applies a manual metadata correction (title/author/year/overview/cover).
func (a *api) handleOverrideBookMetadata(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Title    string `json:"title"`
		Author   string `json:"author"`
		Year     int    `json:"year"`
		Overview string `json:"overview"`
		CoverURL string `json:"cover_url"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Title == "" || req.Author == "" {
		a.writeError(w, http.StatusBadRequest, "title and author are required")
		return
	}
	if err := a.deps.Books.OverrideMetadata(r.Context(), id, req.Title, req.Author, req.Year, req.Overview, req.CoverURL); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update metadata")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "updated"})
}

func (a *api) handleSetBookProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		QualityProfile string `json:"quality_profile"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Books.SetQualityProfile(r.Context(), id, req.QualityProfile); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update quality profile")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"quality_profile": req.QualityProfile})
}

func (a *api) handleSearchBook(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	started := time.Now().UnixMilli()
	jobID, existing, ok := a.submitOr503(w, r, a.bookSearchJob(id))
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "searching", "started_at_ms": started})
}

func (a *api) handleDeleteBook(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	// Detached from the request so a browser giving up part-way can't strand the files in
	// the bin while the book stays.
	ctx := context.WithoutCancel(r.Context())
	if r.URL.Query().Get("delete_files") == "true" {
		// Remove both editions' file(s) and every audio version's from disk before
		// forgetting the book. Each is a no-op if it has nothing on disk; they share a
		// folder so DeleteBookEdition only removes its own kind's files. The first file
		// the recycle bin refuses stops the delete with the book kept.
		b, err := a.deps.Books.Get(ctx, id)
		if err != nil {
			if errors.Is(err, books.ErrNotFound) {
				a.writeError(w, http.StatusNotFound, "book not found")
				return
			}
			a.writeError(w, http.StatusInternalServerError, "could not delete book")
			return
		}
		steps := []func() error{
			func() error { return a.deps.Automation.DeleteBookEdition(ctx, id, books.KindEbook) },
			func() error { return a.deps.Automation.DeleteBookEdition(ctx, id, books.KindAudiobook) },
		}
		for _, v := range b.AudioVersions {
			steps = append(steps, func() error { return a.deps.Automation.DeleteAudioVersionFile(ctx, id, v.ID) })
		}
		for _, step := range steps {
			if err := step(); err != nil {
				if a.writeBinRefusal(w, err) {
					return
				}
				a.writeError(w, http.StatusInternalServerError, "could not delete the book's files — the book was kept")
				return
			}
		}
	}
	if err := a.deps.Books.Delete(ctx, id); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not delete book")
		return
	}
	// Drop any custom uploaded cover so it doesn't orphan on disk.
	for _, f := range coverFiles(filepath.Join(a.deps.Config.DataDir, "covers"), id) {
		_ = os.Remove(f)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBookHistory returns a book's activity timeline (added / grabbed / imported /
// renamed / matched / failed) for the History panel on the detail page.
func (a *api) handleBookHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	events, err := a.deps.Books.Events(r.Context(), id, 100)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	if events == nil {
		events = []books.Event{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// handleRematchBook re-points a book at a different Open Library work — the fix for a book
// the providers, or the library scan (which takes the first search hit), identified wrongly.
// The files already on disk, monitoring and the quality profile are kept; only the metadata
// identity changes.
func (a *api) handleRematchBook(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		OLKey    string `json:"ol_key"`
		Title    string `json:"title"`
		Author   string `json:"author"`
		Year     int    `json:"year"`
		CoverURL string `json:"cover_url"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.OLKey) == "" {
		a.writeError(w, http.StatusBadRequest, "ol_key is required")
		return
	}
	b, err := a.deps.Books.Rematch(r.Context(), id, strings.TrimSpace(req.OLKey), metadata.BookResult{
		Key: req.OLKey, Title: req.Title, Author: req.Author, Year: req.Year, CoverURL: req.CoverURL,
	})
	switch {
	case errors.Is(err, books.ErrNotFound):
		a.writeError(w, http.StatusNotFound, "book not found")
		return
	case errors.Is(err, books.ErrExists):
		// The chosen work is already a separate row. Merging the two is the user's call —
		// say so plainly instead of failing with a bare constraint error.
		a.writeError(w, http.StatusConflict, "that work is already in your library as another book — delete one of them first")
		return
	case err != nil:
		a.writeError(w, http.StatusBadGateway, "could not re-match: "+err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, b)
}
