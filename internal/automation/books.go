package automation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/audiobook"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// bookCategory keeps ebook/audiobook downloads in their own download-client category
// so the book importer processes them (not the movie/series video importers).
const bookCategory = "arrmada-books"

var reBookFormat = regexp.MustCompile(`(?i)\b(epub|azw3|azw|mobi|pdf|cbz|cbr|fb2|djvu|lit|m4b|m4a|mp3|aac|flac|ogg|opus)\b`)

// detectBookFormat extracts the ebook/audiobook format tag from a release name.
func detectBookFormat(title string) string {
	return strings.ToUpper(reBookFormat.FindString(title))
}

// SearchBooksMissing sweeps every monitored book and grabs any wanted edition it lacks.
func (c *Coordinator) SearchBooksMissing(ctx context.Context) {
	if c.books == nil {
		return
	}
	all, err := c.books.List(ctx)
	if err != nil {
		return
	}
	// A book stays "missing" for the whole download (b.Ebook is only set after import),
	// so without an in-flight guard every sweep re-grabbed the same release.
	queue, qerr := c.downloads.Queue(ctx)
	if qerr != nil {
		// An unreadable queue looks exactly like an empty one — every in-flight book
		// would read as "not downloading" and the sweep would stack a duplicate grab
		// on each. Skip the cycle; the next sweep is 15 minutes away.
		c.log.Warn("book: couldn't read the download queue — skipping the missing-books sweep this cycle", "err", qerr)
		return
	}
	for _, b := range all {
		if !b.Monitored {
			continue
		}
		if c.bookDownloading(ctx, queue, b.ID) {
			continue // already downloading for this book — let it finish
		}
		if err := c.SearchBookNow(ctx, b.ID); err != nil {
			c.log.Warn("book: search failed", "title", b.Title, "err", err)
		}
	}
}

// bookDownloading reports whether the queue already holds a book torrent for this book,
// so search/RSS sweeps don't stack a second grab on an in-flight one.
func (c *Coordinator) bookDownloading(ctx context.Context, queue []download.Item, bookID int64) bool {
	if c.books == nil {
		return false
	}
	for _, it := range queue {
		if it.Category != bookCategory {
			continue
		}
		if b, ok := c.books.MatchByRelease(ctx, it.Name); ok && b.ID == bookID {
			return true
		}
	}
	return false
}

// SearchBookNow searches for each wanted edition (ebook/audiobook, per the profile)
// that the book doesn't yet have, and grabs the best-format release for it.
func (c *Coordinator) SearchBookNow(ctx context.Context, bookID int64) error {
	if c.books == nil {
		return nil
	}
	b, err := c.books.Get(ctx, bookID)
	if err != nil {
		return err
	}
	sp := c.bookProfile(ctx, b.QualityProfile)
	wantEbook, wantAudio := books.WantedEditions(sp.FormatScores)
	if wantEbook && b.Ebook == nil {
		c.grabBookEdition(ctx, b, books.KindEbook, sp)
	}
	if wantAudio && b.Audiobook == nil {
		c.grabBookEdition(ctx, b, books.KindAudiobook, sp)
	}
	return nil
}

func (c *Coordinator) grabBookEdition(ctx context.Context, b books.Book, kind string, sp quality.StoredProfile) {
	res, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: bookQuery(b, kind), MediaType: indexer.MediaBook, Limit: 60})
	if err != nil || len(res.Releases) == 0 {
		return
	}
	res.Releases = c.dropBlockedBook(ctx, b.ID, res.Releases) // don't re-grab a blocklisted (e.g. stalled) release
	// DB pending-grab guard, mirroring the movie path's pendingGrabTitles: a release
	// already grabbed for this book (and not yet imported/failed) must not be grabbed
	// again, even when the queue-based bookDownloading check couldn't see it.
	res.Releases = dropPendingBook(res.Releases, c.pendingBookGrabTitles(ctx, b.ID))
	best := pickBestBookForKind(sp, res.Releases, kind)
	if best == nil {
		c.log.Info("book: no matching-format release", "title", b.Title, "edition", kind)
		return
	}
	hash, err := c.grabTo(ctx, best.Indexer, best.DownloadURL, best.Title, bookCategory)
	if err != nil {
		c.log.Warn("book: grab failed", "title", b.Title, "err", err)
		return
	}
	c.recordBookGrab(ctx, b.ID, best.Title, best.Indexer, b.QualityProfile, hash)
	c.log.Info("book: grabbing", "title", b.Title, "edition", kind, "release", best.Title, "format", detectBookFormat(best.Title))
}

// bookQuery builds the indexer query. Audiobook searches append "audiobook" so the
// audio editions surface (ebook releases dominate a bare title search).
func bookQuery(b books.Book, kind string) string {
	q := b.Title
	if b.Author != "" {
		q = b.Author + " " + b.Title
	}
	if kind == books.KindAudiobook {
		q += " audiobook"
	}
	return q
}

// pickBestBookForKind ranks releases of the given edition by the profile: format
// score + keyword score combine (so "graphic audio +100" or a preferred format
// wins), seeders break ties. Hard-reject terms and unwanted formats are skipped.
func pickBestBookForKind(sp quality.StoredProfile, releases []indexer.Release, kind string) *indexer.Release {
	var best *indexer.Release
	var bestScore int
	var bestPartial bool
	for i := range releases {
		if books.EditionOf(releaseBookFormat(releases[i])) != kind {
			continue
		}
		score, ok := bookRelScore(sp, releases[i])
		if !ok {
			continue
		}
		partial := isPartialBook(releases[i].Title)
		if best == nil || betterBook(partial, score, bestPartial, bestScore) {
			best, bestScore, bestPartial = &releases[i], score, partial
		}
	}
	return best
}

// betterBook reports whether a candidate (partial, score) should replace the
// current best (bp, bs): a complete release always beats a partial one, then the
// higher score wins. This keeps a "(Part 1 of 6)" split from being grabbed over
// the complete book.
func betterBook(partial bool, score int, bp bool, bs int) bool {
	if partial != bp {
		return !partial
	}
	return score > bs
}

// rePartialBook matches the "N of M" / "Part N of M" markers trackers use for a
// split release (e.g. MyAnonaMouse's "(Part 3 of 6)", "(2of5)").
var rePartialBook = regexp.MustCompile(`(?i)\b(?:part\s*)?\d+\s*of\s*\d+\b`)

// isPartialBook reports whether a release title looks like one part of a split
// set rather than the complete book.
func isPartialBook(title string) bool { return rePartialBook.MatchString(title) }

// releaseBookFormat prefers the indexer's structured file type (e.g. MyAnonaMouse
// exposes it directly) and falls back to parsing the release title for trackers
// that only put the format in the name.
func releaseBookFormat(rel indexer.Release) string {
	if f := strings.ToUpper(rel.Format); f != "" && books.EditionOf(f) != "" {
		return f
	}
	return detectBookFormat(rel.Title)
}

// bookScoreText is the text keyword and reject rules are matched against. It
// spans the structured fields — not just the title — so a "GraphicAudio"
// preference matches the narrator field even when the release name doesn't
// contain it (which is exactly how MyAnonaMouse presents full-cast productions).
func bookScoreText(rel indexer.Release) string {
	return strings.Join([]string{rel.Title, rel.Author, rel.Narrator, rel.Series, rel.Description}, " ")
}

// bookRelScore ranks a book release under a profile — format score plus keyword
// score (so preferences like "graphic audio +100" combine with format
// preference), seeders as the low-order tiebreak. ok=false when the format isn't
// wanted (score ≤ 0 or unknown) or a hard-reject term is present.
func bookRelScore(sp quality.StoredProfile, rel indexer.Release) (int, bool) {
	f := releaseBookFormat(rel)
	if f == "" {
		return 0, false
	}
	fs, ok := sp.FormatScores[f]
	if !ok || fs <= 0 {
		return 0, false
	}
	text := bookScoreText(rel)
	if quality.Rejects(sp.Rejected, text) {
		return 0, false
	}
	return (fs+quality.KeywordScore(sp.Keywords, text))*1_000_000 + rel.Seeders, true
}

// bookProfile resolves the book's quality profile, falling back to a sensible
// ebook-preferring default when unset.
func (c *Coordinator) bookProfile(ctx context.Context, profileRef string) quality.StoredProfile {
	if sp, err := c.quality.GetStored(ctx, profileRef); err == nil && len(sp.FormatScores) > 0 {
		return sp
	}
	return quality.StoredProfile{FormatScores: map[string]int{"EPUB": 40, "AZW3": 30, "MOBI": 20, "PDF": 10}}
}

// ImportBookDownloads imports finished book downloads: for each completed torrent in
// the book category, match it to a book, group its files by edition (ebook vs
// audiobook), and hardlink each edition into the library.
func (c *Coordinator) ImportBookDownloads(ctx context.Context) {
	if c.books == nil || c.imp == nil {
		return
	}
	completed, err := c.downloads.CompletedInCategory(ctx, bookCategory)
	if err != nil {
		c.log.Warn("book import: couldn't list completed downloads — skipping this cycle", "err", err)
		return
	}
	for _, it := range completed {
		if it.ContentPath == "" {
			continue
		}
		// Without this the sweep re-hardlinked and re-marked every completed book
		// torrent every 30 seconds for as long as it seeded — pure disk/IO churn.
		if c.hashAlreadyImported(ctx, it.Hash) {
			continue
		}
		if c.hasReview(ctx, it.Hash) {
			continue // already held for review (or resolved) — don't re-flag or import
		}
		b, ok := c.books.MatchByRelease(ctx, it.Name)
		if !ok {
			// The sweep runs every 30 seconds, so an unmatchable download used to be
			// rescanned in silence forever. Log once, and after enough attempts hand it
			// to review so a human sees it (same escalation as the series sweep).
			n := c.noteUnmatched(it.Hash)
			switch {
			case n == 1:
				c.log.Info("book import: no matching library book", "release", it.Name)
			case n == unmatchedReviewAfter:
				parsed := bookParsedTitle(it.Name)
				c.log.Warn("book import: download still matches no book — sending to review",
					"release", it.Name, "parsed_title", parsed, "attempts", n)
				c.addReview(ctx, Review{
					Hash: it.Hash, Name: it.Name, ContentPath: it.ContentPath, MediaType: "book",
					ParsedTitle: parsed, SizeBytes: it.SizeBytes,
					Reason: fmt.Sprintf("Parsed as %q, which matches no book in your library", parsed),
				})
			}
			continue
		}
		var ebooks, audio []library.FoundFile
		for _, f := range library.FindBookFiles(it.ContentPath) {
			if library.IsAudiobookFile(f.Path) {
				audio = append(audio, f)
			} else {
				ebooks = append(ebooks, f)
			}
		}
		// Containment heuristic: audiobook releases routinely ship a companion PDF
		// (artwork/booklet) next to the M4B. Importing that PDF as the book's EBOOK
		// edition satisfied the edition forever, so a real EPUB was never searched
		// again. A download holding BOTH kinds is an audiobook release: import only
		// the audio and leave the ebook edition unclaimed. Ebook-only and audio-only
		// downloads import as before.
		if len(audio) > 0 && len(ebooks) > 0 {
			skipped := make([]string, 0, len(ebooks))
			for _, f := range ebooks {
				skipped = append(skipped, filepath.Base(f.Path))
			}
			c.log.Info("book import: audiobook release carries ebook-extension companion files — importing audio only, not claiming the ebook edition",
				"book", b.Title, "release", it.Name, "skipped", strings.Join(skipped, ", "))
			ebooks = nil
		}
		okEbook := c.importBookEdition(ctx, b, books.KindEbook, ebooks, it.Hash, it.Name)
		okAudio := c.importBookEdition(ctx, b, books.KindAudiobook, audio, it.Hash, it.Name)
		switch {
		case okEbook || okAudio:
			// At least one edition actually landed — drop it from the downloads view.
			c.recordImportedHash(ctx, it.Hash, it.Name, it.SizeBytes)
		case len(ebooks) > 0 || len(audio) > 0:
			// Files were found but every import failed. Recording the hash here would
			// make the sweep skip this torrent forever while the book stays missing;
			// leave it unrecorded so the next sweep retries.
			c.log.Warn("book import: found files but no edition imported — will retry next sweep",
				"book", b.Title, "release", it.Name)
		}
	}
}

// bookParsedTitle reduces a release name to a readable title guess for a review row:
// separators become spaces and format tags are stripped.
func bookParsedTitle(name string) string {
	t := strings.NewReplacer(".", " ", "_", " ").Replace(name)
	t = reBookFormat.ReplaceAllString(t, "")
	return strings.Join(strings.Fields(t), " ")
}

// importBookEdition hardlinks one edition's files into the library and records it,
// reporting whether the edition actually imported — callers must only mark the
// download handled when something did.
func (c *Coordinator) importBookEdition(ctx context.Context, b books.Book, kind string, files []library.FoundFile, infoHash, downloadName string) bool {
	if len(files) == 0 {
		return false
	}
	bi, err := c.imp.ImportBookEdition(b.Author, b.Title, files)
	if err != nil {
		c.log.Warn("book: import failed", "title", b.Title, "edition", kind, "err", err)
		return false
	}
	if err := c.books.MarkImported(ctx, b.ID, kind, bi.TargetPath, bi.Format, bi.SizeBytes, bi.FileCount); err != nil {
		c.log.Warn("book: recording the imported edition failed", "title", b.Title, "edition", kind, "err", err)
		return false
	}
	c.log.Info("book: imported", "title", b.Title, "edition", kind, "format", bi.Format, "files", bi.FileCount)
	c.markBookGrabImported(ctx, b.ID, infoHash, downloadName) // flip THIS grab (not siblings) for seed cleanup
	c.bus.Publish("book.imported", map[string]any{"title": b.Title, "id": b.ID, "edition": kind})
	return true
}

// GrabForBook grabs a chosen release for a book (interactive search) into the book category.
func (c *Coordinator) GrabForBook(ctx context.Context, bookID int64, indexerName, downloadURL, title string) error {
	hash, err := c.grabTo(ctx, indexerName, downloadURL, title, bookCategory)
	if err != nil {
		return err
	}
	if b, err := c.books.Get(ctx, bookID); err == nil {
		c.recordBookGrab(ctx, bookID, title, indexerName, b.QualityProfile, hash)
	}
	return nil
}

// RankBookReleases runs an interactive search for a book and returns ebook + audiobook
// releases ranked by format preference (no grabbing).
func (c *Coordinator) RankBookReleases(ctx context.Context, bookID int64) (ReleaseList, error) {
	if c.books == nil {
		return ReleaseList{}, nil
	}
	b, err := c.books.Get(ctx, bookID)
	if err != nil {
		return ReleaseList{}, err
	}
	sp := c.bookProfile(ctx, b.QualityProfile)
	query := b.Title
	if b.Author != "" {
		query = b.Author + " " + b.Title
	}
	// Dedup by download URL — the unique per-torrent link. Deduping by title
	// wrongly collapsed distinct editions that render the same display name (e.g. a
	// GraphicAudio M4B and a standard-narration M4B both "<Author> - <Title> [M4B]"),
	// hiding valid options.
	seen := map[string]bool{}
	var all []indexer.Release
	for _, q := range []string{query, query + " audiobook"} {
		res, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: q, MediaType: indexer.MediaBook, Limit: 60})
		if err != nil {
			continue
		}
		for _, rel := range res.Releases {
			key := rel.DownloadURL
			if key == "" {
				key = rel.Title
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, rel)
		}
	}

	// Score each release once (keyword scoring now spans narrator/series/author,
	// so a GraphicAudio preference actually fires) and keep the score for ordering.
	type ranked struct {
		rr       RankedRelease
		score    int
		eligible bool
		partial  bool
	}
	items := make([]ranked, 0, len(all))
	for _, rel := range all {
		f := releaseBookFormat(rel)
		if f == "" {
			continue // not an identifiable book release
		}
		score, eligible := bookRelScore(sp, rel)
		edition := books.EditionOf(f)
		narrator := rel.Narrator
		if narrator == "" && edition == books.KindAudiobook {
			narrator = parseNarrator(rel.Title + " " + rel.Description)
		}
		items = append(items, ranked{
			rr: RankedRelease{
				Title: rel.Title, Indexer: rel.Indexer, DownloadURL: rel.DownloadURL, InfoURL: rel.InfoURL,
				SizeGB: rel.SizeGB(), Seeders: rel.Seeders, Summary: summarizeBook(f),
				Eligible: eligible, Edition: edition, Format: f, Narrator: narrator,
				Author: rel.Author, Series: rel.Series, Language: rel.Language,
			},
			score: score, eligible: eligible, partial: isPartialBook(rel.Title),
		})
	}
	// Order by the profile's preference: eligible first, then complete before
	// split ("Part N of M") releases, then by combined score.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].eligible != items[j].eligible {
			return items[i].eligible
		}
		if items[i].partial != items[j].partial {
			return !items[i].partial
		}
		return items[i].score > items[j].score
	})
	// Mark the best eligible release of each edition as the recommended pick.
	recommended := map[string]bool{}
	out := make([]RankedRelease, 0, len(items))
	for i := range items {
		rr := items[i].rr
		if items[i].eligible && !recommended[rr.Edition] {
			rr.Recommended = true
			recommended[rr.Edition] = true
		}
		out = append(out, rr)
	}
	return ReleaseList{Profile: b.QualityProfile, Releases: out}, nil
}

// reNarrator pulls a narrator name from an audiobook release title or description
// ("Narrated by Michael Kramer", "Read by Kate Reading", "Narrator: …").
var reNarrator = regexp.MustCompile(`(?i)(?:narrated by|read by|narrator[:\s]+)\s*([A-Z][\p{L}.'-]+(?:\s+(?:&|and|,)?\s*[A-Z][\p{L}.'-]+){0,3})`)

// parseNarrator returns the first narrator credit found in text, or "".
func parseNarrator(text string) string {
	m := reNarrator.FindStringSubmatch(text)
	if len(m) > 1 {
		return strings.TrimSpace(strings.Trim(m[1], " ,&"))
	}
	return ""
}

func summarizeBook(format string) string {
	edition := "Ebook"
	if books.IsAudiobookFormat(format) {
		edition = "Audiobook"
	}
	return edition + " · " + format
}

// RescanBook re-reads the book's library folder and records the ebook/audiobook
// editions present. A multi-file audiobook is recorded as its folder + a file count.
func (c *Coordinator) RescanBook(ctx context.Context, bookID int64) {
	if c.books == nil || c.imp == nil {
		return
	}
	b, err := c.books.Get(ctx, bookID)
	if err != nil {
		return
	}
	var ebooks, audio []library.FoundFile
	for _, f := range c.imp.BookLibraryFiles(b.Author, b.Title) {
		if library.IsAudiobookFile(f.Path) {
			audio = append(audio, f)
		} else {
			ebooks = append(ebooks, f)
		}
	}
	c.recordEdition(ctx, bookID, books.KindEbook, ebooks)
	c.recordEdition(ctx, bookID, books.KindAudiobook, audio)
}

// recordEdition marks an edition present from on-disk files: a single file uses its
// path; multiple files use their shared folder + the file count.
func (c *Coordinator) recordEdition(ctx context.Context, bookID int64, kind string, files []library.FoundFile) {
	if len(files) == 0 {
		return
	}
	if len(files) == 1 {
		f := files[0]
		_ = c.books.MarkImported(ctx, bookID, kind, f.Path, library.BookFileFormat(f.Path), f.Size, 1)
		return
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	dir := filepath.Dir(files[0].Path)
	_ = c.books.MarkImported(ctx, bookID, kind, dir, library.BookFileFormat(files[0].Path), total, len(files))
}

// BookImportCandidate is an on-disk book file that can be manually imported.
type BookImportCandidate struct {
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	Edition   string `json:"edition"` // ebook | audiobook
	Format    string `json:"format"`
	SizeBytes int64  `json:"size_bytes"`
}

// BookImportCandidates lists importable book files under dir.
func (c *Coordinator) BookImportCandidates(dir string) []BookImportCandidate {
	files := library.FindBookFiles(dir)
	out := make([]BookImportCandidate, 0, len(files))
	for _, f := range files {
		edition := books.KindEbook
		if library.IsAudiobookFile(f.Path) {
			edition = books.KindAudiobook
		}
		out = append(out, BookImportCandidate{
			Path: f.Path, Filename: filepath.Base(f.Path), Edition: edition,
			Format: library.BookFileFormat(f.Path), SizeBytes: f.Size,
		})
	}
	return out
}

// ManualImportBook imports one on-disk file into a book as the correct edition.
func (c *Coordinator) ManualImportBook(ctx context.Context, bookID int64, path string) error {
	if c.books == nil || c.imp == nil {
		return errBooksNotReady
	}
	b, err := c.books.Get(ctx, bookID)
	if err != nil {
		return err
	}
	kind := books.KindEbook
	if library.IsAudiobookFile(path) {
		kind = books.KindAudiobook
	}
	bi, err := c.imp.ImportBookEdition(b.Author, b.Title, []library.FoundFile{{Path: path}})
	if err != nil {
		return err
	}
	return c.books.MarkImported(ctx, bookID, kind, bi.TargetPath, bi.Format, bi.SizeBytes, bi.FileCount)
}

// DeleteBookEdition removes an edition's file(s) from disk and forgets the edition.
func (c *Coordinator) DeleteBookEdition(ctx context.Context, bookID int64, kind string) error {
	if c.books == nil {
		return errBooksNotReady
	}
	b, err := c.books.Get(ctx, bookID)
	if err != nil {
		return err
	}
	var bf *books.BookFile
	if kind == books.KindAudiobook {
		bf = b.Audiobook
	} else {
		bf = b.Ebook
	}
	if bf != nil && bf.Path != "" {
		if fi, err := os.Stat(bf.Path); err == nil && fi.IsDir() {
			// Multi-file edition in a (possibly shared) folder — remove only this
			// edition's files, never the sibling edition's.
			for _, f := range library.FindBookFiles(bf.Path) {
				if library.IsAudiobookFile(f.Path) == (kind == books.KindAudiobook) {
					c.removeBookFile(f.Path)
				}
			}
			_ = os.Remove(bf.Path) // succeeds only if now empty
		} else {
			c.removeBookFile(bf.Path)
			_ = os.Remove(filepath.Dir(bf.Path)) // prune if empty
		}
	}
	return c.books.ClearEdition(ctx, bookID, kind)
}

// removeBookFile deletes one book file, moving it to the recycle bin when one is
// configured (like movies) and hard-deleting otherwise.
func (c *Coordinator) removeBookFile(path string) {
	if c.recycle != "" {
		if dst, err := library.RecycleFile(c.recycle, path); err != nil {
			c.log.Warn("book: recycle failed, hard-deleting", "path", path, "err", err)
			_ = os.Remove(path)
		} else if dst != "" {
			c.log.Info("book: moved to recycle bin", "from", path, "to", dst)
		}
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		c.log.Warn("book: could not delete file", "path", path, "err", err)
	}
}

// BookRename renames single-file editions to their canonical library path, returning
// how many moved. Multi-file (folder) editions are left as-is.
func (c *Coordinator) BookRename(ctx context.Context, bookID int64) (int, error) {
	if c.books == nil || c.imp == nil {
		return 0, errBooksNotReady
	}
	b, err := c.books.Get(ctx, bookID)
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, e := range []struct {
		kind string
		f    *books.BookFile
	}{{books.KindEbook, b.Ebook}, {books.KindAudiobook, b.Audiobook}} {
		if e.f == nil || e.f.Path == "" {
			continue
		}
		if fi, err := os.Stat(e.f.Path); err == nil && fi.IsDir() {
			continue // folder edition — leave it
		}
		target := c.imp.BookEditionCanonical(b.Author, b.Title, e.f.Path)
		if target == "" || target == e.f.Path {
			continue
		}
		if err := c.imp.Move(e.f.Path, target); err != nil {
			c.log.Warn("book: rename failed", "from", e.f.Path, "err", err)
			continue
		}
		_ = c.books.MarkImported(ctx, bookID, e.kind, target, e.f.Format, e.f.SizeBytes, 1)
		moved++
	}
	return moved, nil
}

// BookScanResult summarizes a book library scan.
type BookScanResult struct {
	Imported  int      `json:"imported"`
	Skipped   int      `json:"skipped"`
	Unmatched []string `json:"unmatched,omitempty"`
}

// ScanBookLibrary catalogs books already in the library folder. Each book folder is
// matched to Open Library, added unmonitored, and assigned the profile that matches the
// editions found on disk (ebook-only → Ebook, audio-only → Audiobook, both → both).
func (c *Coordinator) ScanBookLibrary(ctx context.Context, ebookRoot, audiobookRoot string) BookScanResult {
	var res BookScanResult
	if c.books == nil || c.imp == nil || !c.books.MetadataAvailable() {
		return res
	}
	existing := map[string]books.Book{}
	if list, err := c.books.List(ctx); err == nil {
		for _, b := range list {
			existing[b.OLKey] = b
		}
	}
	var folders []library.BookFolder
	if ebookRoot != "" || audiobookRoot != "" {
		folders = c.imp.FindBookFoldersIn(ebookRoot, audiobookRoot)
	} else {
		folders = c.imp.FindBookFolders()
	}

	// Group folders by the metadata book they match, so an ebook folder and an
	// audiobook folder for the same title — often in separate roots, sometimes with
	// different casing — become ONE book with both editions, not one edition each.
	type pending struct {
		match  metadata.BookResult
		ebooks []library.FoundFile
		audio  []library.FoundFile
	}
	byKey := map[string]*pending{}
	var order []string
	for _, bf := range folders {
		if bf.Title == "" || (len(bf.Ebooks) == 0 && len(bf.Audiobooks) == 0) {
			continue
		}
		query := bf.Title
		if bf.Author != "" {
			query = bf.Author + " " + bf.Title
		}
		results, err := c.books.Lookup(ctx, query)
		if err != nil || len(results) == 0 {
			res.Unmatched = append(res.Unmatched, bf.Title)
			continue
		}
		match := results[0]
		p := byKey[match.Key]
		if p == nil {
			p = &pending{match: match}
			byKey[match.Key] = p
			order = append(order, match.Key)
		}
		p.ebooks = append(p.ebooks, bf.Ebooks...)
		p.audio = append(p.audio, bf.Audiobooks...)
	}

	for _, key := range order {
		p := byKey[key]
		hasE, hasA := len(p.ebooks) > 0, len(p.audio) > 0
		if b, ok := existing[key]; ok {
			// Already in the library — backfill any edition present on disk but not
			// yet recorded (e.g. the ebook when only the audiobook was found before).
			merged := false
			if hasE && b.Ebook == nil {
				c.recordEdition(ctx, b.ID, books.KindEbook, p.ebooks)
				merged = true
			}
			if hasA && b.Audiobook == nil {
				c.recordEdition(ctx, b.ID, books.KindAudiobook, p.audio)
				merged = true
			}
			if merged {
				// Widen the profile so both on-disk editions count as wanted/included.
				wantE := hasE || b.Ebook != nil
				wantA := hasA || b.Audiobook != nil
				_ = c.books.SetQualityProfile(ctx, b.ID, c.bookProfileFor(ctx, wantE, wantA))
				res.Imported++
				c.log.Info("book scan: added missing edition", "title", b.Title, "ebook", hasE, "audio", hasA)
			} else {
				res.Skipped++
			}
			continue
		}
		profile := c.bookProfileFor(ctx, hasE, hasA)
		added, err := c.books.Add(ctx, p.match.Key, profile, false, p.match)
		if err != nil {
			res.Unmatched = append(res.Unmatched, p.match.Title)
			continue
		}
		c.recordEdition(ctx, added.ID, books.KindEbook, p.ebooks)
		c.recordEdition(ctx, added.ID, books.KindAudiobook, p.audio)
		res.Imported++
		c.log.Info("book scan: imported", "title", added.Title, "ebooks", len(p.ebooks), "audio", len(p.audio))
	}
	return res
}

// bookProfileFor returns the book profile ref whose wanted editions match the files
// found on disk, falling back to the default book profile.
func (c *Coordinator) bookProfileFor(ctx context.Context, wantEbook, wantAudio bool) string {
	if profiles, err := c.quality.ListStored(ctx, "book"); err == nil {
		for _, sp := range profiles {
			e, a := books.WantedEditions(sp.FormatScores)
			if e == wantEbook && a == wantAudio {
				return "custom:" + strconv.FormatInt(sp.ID, 10)
			}
		}
	}
	return c.quality.DefaultProfile(ctx, "book")
}

// BookFileEntry is one file inside an edition (for the collapsible file list).
type BookFileEntry struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

// EditionFiles lists the individual files of an edition (a multi-file audiobook shows
// all its chapter files).
func (c *Coordinator) EditionFiles(ctx context.Context, bookID int64, kind string) []BookFileEntry {
	if c.books == nil {
		return nil
	}
	b, err := c.books.Get(ctx, bookID)
	if err != nil {
		return nil
	}
	var bf *books.BookFile
	if kind == books.KindAudiobook {
		bf = b.Audiobook
	} else {
		bf = b.Ebook
	}
	if bf == nil {
		return nil
	}
	if fi, err := os.Stat(bf.Path); err == nil && fi.IsDir() {
		files := library.FindBookFiles(bf.Path)
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		out := make([]BookFileEntry, 0, len(files))
		for _, f := range files {
			// The ebook and audiobook editions can share a folder — only list files of
			// the requested edition's kind.
			if library.IsAudiobookFile(f.Path) != (kind == books.KindAudiobook) {
				continue
			}
			out = append(out, BookFileEntry{Name: filepath.Base(f.Path), SizeBytes: f.Size})
		}
		return out
	}
	return []BookFileEntry{{Name: filepath.Base(bf.Path), SizeBytes: bf.SizeBytes}}
}

// MergeAudiobookAvailable reports whether ffmpeg is present for the merge feature.
func (c *Coordinator) MergeAudiobookAvailable() bool { return audiobook.Available() }

// MergeAudiobook combines a multi-file audiobook into a single chapterized .m4b (one
// chapter per source file). Runs synchronously — callers should background it.
func (c *Coordinator) MergeAudiobook(ctx context.Context, bookID int64) error {
	if c.books == nil {
		return errBooksNotReady
	}
	b, err := c.books.Get(ctx, bookID)
	if err != nil {
		return err
	}
	if b.Audiobook == nil || b.Audiobook.FileCount <= 1 {
		return errString("nothing to merge — the audiobook is a single file")
	}
	files := library.FindBookFiles(b.Audiobook.Path)
	var paths []string
	for _, f := range files {
		if library.IsAudiobookFile(f.Path) {
			paths = append(paths, f.Path)
		}
	}
	sort.Strings(paths)
	if len(paths) < 2 {
		return errString("nothing to merge")
	}
	out := filepath.Join(b.Audiobook.Path, sanitizeName(b.Title)+".m4b")
	c.log.Info("book: merging audiobook", "title", b.Title, "files", len(paths))
	if err := audiobook.Merge(ctx, paths, out); err != nil {
		return err
	}
	// Success: drop the source chapter files, keep the single m4b, update the edition.
	for _, p := range paths {
		_ = os.Remove(p)
	}
	fi, _ := os.Stat(out)
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	c.log.Info("book: merged audiobook", "title", b.Title, "out", out)
	c.bus.Publish("book.imported", map[string]any{"title": b.Title, "id": b.ID, "edition": "audiobook"})
	return c.books.MarkImported(ctx, bookID, books.KindAudiobook, out, "M4B", size, 1)
}

func sanitizeName(s string) string {
	repl := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "", "?", "", "\"", "", "<", "", ">", "", "|", "")
	return strings.TrimSpace(repl.Replace(s))
}

// pendingBookGrabTitles returns the normalized titles of a book's still-pending grabs
// (grabbed but not yet imported or failed) — the DB-backed twin of the queue-based
// bookDownloading guard, mirroring pendingGrabTitles for movies. When the download
// client's queue can't be read, or a torrent's name doesn't match back to the book,
// this still stops the same release being grabbed again. Bounded to a day so a grab
// stuck 'grabbed' forever (torrent removed by hand, stall timeout unset) can't block
// re-grabbing that release permanently.
func (c *Coordinator) pendingBookGrabTitles(ctx context.Context, bookID int64) map[string]bool {
	rows, err := c.db.QueryContext(ctx,
		`SELECT title FROM grabs
		 WHERE movie_id = ? AND status = 'grabbed' AND media_type = 'book'
		   AND grabbed_at > datetime('now', '-1 day')`, bookID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var title string
		if rows.Scan(&title) == nil {
			set[normTitle(title)] = true
		}
	}
	return set
}

// dropPendingBook removes releases whose normalized title is already pending as a grab.
func dropPendingBook(releases []indexer.Release, pending map[string]bool) []indexer.Release {
	if len(pending) == 0 {
		return releases
	}
	out := releases[:0]
	for _, rel := range releases {
		if !pending[normTitle(rel.Title)] {
			out = append(out, rel)
		}
	}
	return out
}

// recordBookGrab tracks a book grab for seed cleanup (media_type=book, movie_id=bookID).
func (c *Coordinator) recordBookGrab(ctx context.Context, bookID int64, title, indexer, profile, infoHash string) {
	seedEnabled, seedRatio, seedHours := c.seedRules(ctx, indexer)
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, version_id, title, indexer, quality_profile, stall_minutes, seed_enabled, seed_ratio, seed_hours, media_type, info_hash)
		 VALUES (?, 0, ?, ?, ?, 0, ?, ?, ?, 'book', ?)`,
		bookID, title, indexer, profile, boolToInt(seedEnabled), seedRatio, seedHours, infoHash)
	if err != nil {
		c.log.Warn("book: record grab failed", "err", err)
	}
}

// markBookGrabImported flips the ONE grab this download came from to imported —
// matching by info hash first (the torrent's real identity), then by normalized
// release name for rows without one. Mirrors markSeriesGrabImported.
//
// It used to flip EVERY pending book grab for the book, which marked a sibling
// edition's torrent — the audiobook still downloading while the ebook landed — as
// imported before its data existed. Seed cleanup only considers imported grabs and
// removes finished ones WITH their data, so the sibling was deleted the moment it
// completed and the edition re-grabbed: the same grab → delete → re-grab loop the
// series path fixed. A grab matching neither hash nor name stays pending;
// detectStalledBook resolves it once its own edition lands.
func (c *Coordinator) markBookGrabImported(ctx context.Context, bookID int64, infoHash, downloadName string) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, title, info_hash FROM grabs WHERE movie_id = ? AND status = 'grabbed' AND media_type = 'book'`, bookID)
	if err != nil {
		return
	}
	wantHash := strings.ToLower(infoHash)
	want := normRelease(downloadName)
	var byHash, byName []int64
	for rows.Next() {
		var id int64
		var title, hash string
		if rows.Scan(&id, &title, &hash) != nil {
			continue
		}
		if wantHash != "" && hash != "" && strings.ToLower(hash) == wantHash {
			byHash = append(byHash, id)
		} else if normRelease(title) == want {
			byName = append(byName, id)
		}
	}
	ids := byHash
	if len(ids) == 0 {
		ids = byName
	}
	rows.Close() // close before writing — SQLite won't take a write while a read is open
	for _, id := range ids {
		if _, err := c.db.ExecContext(ctx, `UPDATE grabs SET status = 'imported' WHERE id = ?`, id); err != nil {
			c.log.Warn("book: mark grab imported failed", "err", err)
		}
	}
}

var errBooksNotReady = errString("books module not ready")

type errString string

func (e errString) Error() string { return string(e) }

// dropBlockedBook removes releases blocklisted for this book (so a stalled/rejected one isn't
// re-grabbed).
func (c *Coordinator) dropBlockedBook(ctx context.Context, bookID int64, releases []indexer.Release) []indexer.Release {
	blocked := c.blockedSetBook(ctx, bookID)
	if len(blocked) == 0 {
		return releases
	}
	out := releases[:0]
	for _, rel := range releases {
		if !blocked[normTitle(rel.Title)] {
			out = append(out, rel)
		}
	}
	return out
}

// detectStalledBook fails over a stalled book grab: blocklist the release, remove it, re-search.
func (c *Coordinator) detectStalledBook(ctx context.Context, g grab, queue []download.Item) {
	if c.books == nil {
		c.setGrabStatus(ctx, g.ID, "failed")
		return
	}
	b, err := c.books.Get(ctx, g.MovieID) // book id is stored in movie_id on the shared grabs table
	if err != nil {
		c.setGrabStatus(ctx, g.ID, "failed")
		return
	}
	if b.HasFile { // an edition landed
		c.setGrabStatus(ctx, g.ID, "imported")
		return
	}
	if g.StallMinutes <= 0 {
		return
	}
	window := time.Duration(g.StallMinutes) * time.Minute
	if time.Since(parseTime(g.GrabbedAt)) < window {
		return
	}
	item, found := findQueued(queue, g)
	if !c.stalledInQueue(g, item, found, window) {
		return
	}
	c.log.Info("automation: book download stalled, failing over", "book", g.MovieID, "release", g.Title)
	c.addBlockBook(ctx, g.MovieID, g.Title, g.Indexer, fmt.Sprintf("stalled after %d min", g.StallMinutes))
	if found {
		_ = c.downloads.Remove(ctx, item.Hash, true)
	}
	c.setGrabStatus(ctx, g.ID, "failed")
	_ = c.SearchBookNow(ctx, g.MovieID)
}

// RSSSyncBooks polls indexer RSS feeds for freshly-uploaded releases matching a wanted book
// edition and grabs them — the fast path between the slower targeted missing-sweep runs.
func (c *Coordinator) RSSSyncBooks(ctx context.Context) {
	if c.books == nil {
		return
	}
	all, err := c.books.List(ctx)
	if err != nil {
		return
	}
	res, err := c.indexers.Recent(ctx, 100)
	if err != nil || len(res.Releases) == 0 {
		return
	}
	queue, qerr := c.downloads.Queue(ctx)
	if qerr != nil {
		// Same reasoning as SearchBooksMissing: an unreadable queue reads as empty,
		// which would let this path stack duplicate grabs. Skip the cycle.
		c.log.Warn("rss: couldn't read the download queue — skipping the book RSS sync this cycle", "err", qerr)
		return
	}
	for _, b := range all {
		if !b.Monitored {
			continue
		}
		if c.bookDownloading(ctx, queue, b.ID) {
			continue // already downloading for this book — don't stack another grab
		}
		sp := c.bookProfile(ctx, b.QualityProfile)
		matched := c.dropBlockedBook(ctx, b.ID, releasesForBook(res.Releases, b))
		if len(matched) == 0 {
			continue
		}
		// DB pending-grab guard (belt to bookDownloading's braces): never re-grab a
		// release that's already been grabbed for this book and is still pending.
		matched = dropPendingBook(matched, c.pendingBookGrabTitles(ctx, b.ID))
		if len(matched) == 0 {
			continue
		}
		// Grab any wanted edition the book still lacks.
		for _, kind := range []string{books.KindEbook, books.KindAudiobook} {
			if (kind == books.KindEbook && b.Ebook != nil) || (kind == books.KindAudiobook && b.Audiobook != nil) {
				continue // already have this edition
			}
			best := pickBestBookForKind(sp, matched, kind)
			if best == nil {
				continue
			}
			hash, err := c.grabTo(ctx, best.Indexer, best.DownloadURL, best.Title, bookCategory)
			if err != nil {
				continue
			}
			c.recordBookGrab(ctx, b.ID, best.Title, best.Indexer, b.QualityProfile, hash)
			c.log.Info("rss: grabbing book", "title", b.Title, "edition", kind, "release", best.Title)
		}
	}
}

// releasesForBook keeps releases whose normalized name contains the book's title (and, when a
// distinctive author is set, the author) — a conservative match for the messy world of book names.
func releasesForBook(releases []indexer.Release, b books.Book) []indexer.Release {
	title := normTitle(b.Title)
	author := normTitle(b.Author)
	var out []indexer.Release
	for _, rel := range releases {
		n := normTitle(rel.Title)
		if title != "" && strings.Contains(n, title) && (author == "" || strings.Contains(n, author)) {
			out = append(out, rel)
		}
	}
	return out
}
