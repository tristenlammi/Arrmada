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

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
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
	// The job outlives the request that started it.
	if !a.deps.Books.StartUpgrade(context.WithoutCancel(r.Context())) {
		a.writeJSON(w, http.StatusOK, map[string]any{"started": false, "status": a.deps.Books.UpgradeStatus()})
		return
	}
	a.writeJSON(w, http.StatusAccepted, map[string]any{"started": true, "status": a.deps.Books.UpgradeStatus()})
}

func (a *api) handleBookUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Books.UpgradeStatus())
}

// handleStartBookSweep searches for every monitored book's missing editions — the
// manual, back-off-free sweep. It fills gaps only; nothing on disk is replaced.
func (a *api) handleStartBookSweep(w http.ResponseWriter, r *http.Request) {
	started := a.deps.Automation.StartBookSweep(context.WithoutCancel(r.Context()))
	code := http.StatusOK
	if started {
		code = http.StatusAccepted
	}
	a.writeJSON(w, code, map[string]any{"started": started, "status": a.deps.Automation.BookSweepStatus()})
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
	if results == nil {
		results = []metadata.BookResult{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"results": results, "source": a.deps.Books.MetadataSource()})
}

// handleMergeBookDuplicates folds books that are the same title and author into one row
// each — the fix for a library that collected the same novel under several catalogue keys.
func (a *api) handleMergeBookDuplicates(w http.ResponseWriter, r *http.Request) {
	n, err := a.deps.Books.MergeDuplicates(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not merge duplicates")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"merged": n})
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
		go func(id int64, title string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := a.deps.Automation.SearchBookNow(ctx, id); err != nil {
				a.deps.Log.Warn("book auto-search on add failed", "book", title, "err", err)
			}
		}(b.ID, b.Title)
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
	a.writeJSON(w, http.StatusOK, b)
}

// enrichBookWants fills want_ebook/want_audiobook from the book's quality profile so
// the detail page can show wanted-but-missing editions.
func (a *api) enrichBookWants(r *http.Request, b *books.Book) {
	if sp, err := a.deps.Quality.GetStored(r.Context(), b.QualityProfile); err == nil {
		b.WantEbook, b.WantAudiobook = books.WantedEditions(sp.FormatScores)
	} else {
		b.WantEbook = true
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
	a.writeJSON(w, http.StatusOK, b)
}

// handleBookReleases runs an interactive search (ebook + audiobook) without grabbing.
func (a *api) handleBookReleases(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	list, err := a.deps.Automation.RankBookReleases(ctx, id)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, list)
}

// handleGrabBook grabs a chosen release for a book.
func (a *api) handleGrabBook(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Indexer     string `json:"indexer"`
		DownloadURL string `json:"download_url"`
		Title       string `json:"title"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.DownloadURL == "" {
		a.writeError(w, http.StatusBadRequest, "download_url is required")
		return
	}
	if err := a.deps.Automation.GrabForBook(r.Context(), id, req.Indexer, req.DownloadURL, req.Title); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "grabbed", "title": req.Title})
}

// handleBookManualImportList / handleBookManualImport handle picking an on-disk file.
func (a *api) handleBookManualImportList(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.pathID(w, r); !ok {
		return
	}
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = a.deps.Config.DownloadsDir
	}
	cands := a.deps.Automation.BookImportCandidates(dir)
	if cands == nil {
		cands = []automation.BookImportCandidate{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"candidates": cands})
}

func (a *api) handleBookManualImport(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !a.decodeJSON(w, r, &req) || req.Path == "" {
		a.writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	if err := a.deps.Automation.ManualImportBook(r.Context(), id, req.Path); err != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	go func() {
		defer cancel()
		res := a.deps.Automation.ScanBookLibrary(ctx, ebooks, audiobooks)
		a.deps.Log.Info("book library scan done", "imported", res.Imported, "skipped", res.Skipped)
	}()
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "scanning"})
}

// handleBackfillBookSeries fills in the series for books already in the library — a
// one-off for a library assembled before Arrmada started recording it. Runs in the
// background: one indexer search per unlabelled book takes minutes, not a request.
func (a *api) handleBackfillBookSeries(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	go func() {
		defer cancel()
		res, err := a.deps.Automation.BackfillBookSeries(ctx)
		if err != nil {
			a.deps.Log.Warn("book series backfill failed", "err", err,
				"scanned", res.Scanned, "learned", res.Learned)
			return
		}
		a.deps.Log.Info("book series backfill finished", "scanned", res.Scanned, "learned", res.Learned)
	}()
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "backfilling"})
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
	go func(bid int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := a.deps.Automation.MergeAudiobook(ctx, bid); err != nil {
			a.deps.Log.Warn("audiobook merge failed", "book_id", bid, "err", err)
		}
	}(id)
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "merging"})
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
		a.writeError(w, http.StatusInternalServerError, "could not delete file")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// handleBookCovers returns candidate cover images (Open Library editions + Google Books)
// for the cover picker.
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
	coverURL := fmt.Sprintf("%s/api/v1/books/%d/cover-image?v=%d", a.deps.Config.BaseURL, id, time.Now().Unix())
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
}

// enrichBookCards annotates search/browse results with library + request status.
func (a *api) enrichBookCards(ctx context.Context, results []metadata.BookResult) []bookCard {
	// By catalogue key AND by what the book is (title + author): a library built on
	// Open Library must show its books as owned when the results come from Hardcover,
	// and a second Open Library "work" for the same novel must not look like a new book.
	inLib := map[string]bool{}
	hasFile := map[string]bool{}
	if list, err := a.deps.Books.List(ctx); err == nil {
		for _, b := range list {
			inLib[b.OLKey] = true
			hasFile[b.OLKey] = b.HasFile
			if k := books.DedupeKey(b.Title, b.Author); k != "" {
				inLib["d:"+k] = true
				hasFile["d:"+k] = hasFile["d:"+k] || b.HasFile
			}
		}
	}
	// Requests.List returns newest first; iterating in order and overwriting means the
	// OLDEST request would win, so only set a key on first sight — the newest request
	// for a book determines its badge (matching the movie/series discover behavior of
	// one-status-per-title, but declined stays distinguishable from never-requested).
	reqStatus := map[string]string{}
	if reqs, err := a.deps.Requests.List(ctx, "", 0); err == nil {
		for _, rq := range reqs {
			if rq.MediaType == "book" && rq.OLKey != "" {
				if _, seen := reqStatus[rq.OLKey]; !seen {
					reqStatus[rq.OLKey] = rq.Status
				}
			}
		}
	}
	cards := make([]bookCard, 0, len(results))
	for _, br := range results {
		st := reqStatus[br.Key]
		dk := "d:" + books.DedupeKey(br.Title, br.Author)
		cards = append(cards, bookCard{
			BookResult:    br,
			InLibrary:     inLib[br.Key] || inLib[dk],
			HasFile:       hasFile[br.Key] || hasFile[dk],
			Requested:     st == "pending",
			RequestStatus: st,
		})
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
		out = append(out, row{Title: rw.Title, Seed: rw.Seed, SeedID: rw.SeedID, Books: a.enrichBookCards(ctx, rw.Books)})
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
		go func(ids []int64) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			for _, id := range ids {
				if err := a.deps.Automation.SearchBookNow(ctx, id); err != nil {
					a.deps.Log.Warn("add author: search failed", "book_id", id, "err", err)
				}
			}
		}(ids)
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
	a.writeJSON(w, http.StatusOK, d)
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
	go func(bid int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := a.deps.Automation.SearchBookNow(ctx, bid); err != nil {
			a.deps.Log.Warn("book manual search failed", "book_id", bid, "err", err)
		}
	}(id)
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "searching"})
}

func (a *api) handleDeleteBook(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("delete_files") == "true" {
		// Remove both editions' file(s) from disk before forgetting the book. Each is a
		// no-op if the edition has nothing on disk; they share a folder so DeleteBookEdition
		// only removes its own kind's files.
		_ = a.deps.Automation.DeleteBookEdition(r.Context(), id, books.KindEbook)
		_ = a.deps.Automation.DeleteBookEdition(r.Context(), id, books.KindAudiobook)
	}
	if err := a.deps.Books.Delete(r.Context(), id); err != nil {
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
