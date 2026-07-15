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
	for _, b := range all {
		if !b.Monitored {
			continue
		}
		if err := c.SearchBookNow(ctx, b.ID); err != nil {
			c.log.Warn("book: search failed", "title", b.Title, "err", err)
		}
	}
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
	best := pickBestBookForKind(sp, res.Releases, kind)
	if best == nil {
		c.log.Info("book: no matching-format release", "title", b.Title, "edition", kind)
		return
	}
	if err := c.grabTo(ctx, best.Indexer, best.DownloadURL, best.Title, bookCategory); err != nil {
		c.log.Warn("book: grab failed", "title", b.Title, "err", err)
		return
	}
	c.recordBookGrab(ctx, b.ID, best.Title, best.Indexer, b.QualityProfile)
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
	bestScore := 0
	for i := range releases {
		if books.EditionOf(detectBookFormat(releases[i].Title)) != kind {
			continue
		}
		score, ok := bookRelScore(sp, releases[i].Title, releases[i].Seeders)
		if !ok {
			continue
		}
		if best == nil || score > bestScore {
			bestScore, best = score, &releases[i]
		}
	}
	return best
}

// bookRelScore ranks a book release under a profile — format score plus keyword
// score (so preferences like "graphic audio +100" combine with format
// preference), seeders as the low-order tiebreak. ok=false when the format isn't
// wanted (score ≤ 0 or unknown) or a hard-reject term is present.
func bookRelScore(sp quality.StoredProfile, title string, seeders int) (int, bool) {
	f := detectBookFormat(title)
	if f == "" {
		return 0, false
	}
	fs, ok := sp.FormatScores[f]
	if !ok || fs <= 0 {
		return 0, false
	}
	if quality.Rejects(sp.Rejected, title) {
		return 0, false
	}
	return (fs+quality.KeywordScore(sp.Keywords, title))*1_000_000 + seeders, true
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
		return
	}
	for _, it := range completed {
		if it.ContentPath == "" {
			continue
		}
		b, ok := c.books.MatchByRelease(ctx, it.Name)
		if !ok {
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
		c.importBookEdition(ctx, b, books.KindEbook, ebooks)
		c.importBookEdition(ctx, b, books.KindAudiobook, audio)
		if len(ebooks) > 0 || len(audio) > 0 {
			c.recordImportedHash(ctx, it.Hash, it.Name, it.SizeBytes) // drop it from the downloads view
		}
	}
}

func (c *Coordinator) importBookEdition(ctx context.Context, b books.Book, kind string, files []library.FoundFile) {
	if len(files) == 0 {
		return
	}
	bi, err := c.imp.ImportBookEdition(b.Author, b.Title, files)
	if err != nil {
		c.log.Warn("book: import failed", "title", b.Title, "edition", kind, "err", err)
		return
	}
	if err := c.books.MarkImported(ctx, b.ID, kind, bi.TargetPath, bi.Format, bi.SizeBytes, bi.FileCount); err == nil {
		c.log.Info("book: imported", "title", b.Title, "edition", kind, "format", bi.Format, "files", bi.FileCount)
		c.markBookGrabsImported(ctx, b.ID)
		c.bus.Publish("book.imported", map[string]any{"title": b.Title, "id": b.ID, "edition": kind})
	}
}

// GrabForBook grabs a chosen release for a book (interactive search) into the book category.
func (c *Coordinator) GrabForBook(ctx context.Context, bookID int64, indexerName, downloadURL, title string) error {
	if err := c.grabTo(ctx, indexerName, downloadURL, title, bookCategory); err != nil {
		return err
	}
	if b, err := c.books.Get(ctx, bookID); err == nil {
		c.recordBookGrab(ctx, bookID, title, indexerName, b.QualityProfile)
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
	seen := map[string]bool{}
	var all []indexer.Release
	for _, q := range []string{query, query + " audiobook"} {
		res, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: q, MediaType: indexer.MediaBook, Limit: 60})
		if err != nil {
			continue
		}
		for _, rel := range res.Releases {
			if seen[rel.Title] {
				continue
			}
			seen[rel.Title] = true
			all = append(all, rel)
		}
	}
	out := make([]RankedRelease, 0, len(all))
	for _, rel := range all {
		f := detectBookFormat(rel.Title)
		if f == "" {
			continue // not an identifiable book release
		}
		_, eligible := bookRelScore(sp, rel.Title, rel.Seeders)
		edition := books.EditionOf(f)
		narrator := ""
		if edition == books.KindAudiobook {
			narrator = parseNarrator(rel.Title + " " + rel.Description)
		}
		out = append(out, RankedRelease{
			Title: rel.Title, Indexer: rel.Indexer, DownloadURL: rel.DownloadURL, InfoURL: rel.InfoURL,
			SizeGB: rel.SizeGB(), Seeders: rel.Seeders, Summary: summarizeBook(f),
			Eligible: eligible, Edition: edition, Format: f, Narrator: narrator,
		})
	}
	// Order by the profile's preference so the best-format / best-keyword release
	// leads (eligible first, then by combined score).
	sort.SliceStable(out, func(i, j int) bool {
		si, oi := bookRelScore(sp, out[i].Title, out[i].Seeders)
		sj, oj := bookRelScore(sp, out[j].Title, out[j].Seeders)
		if oi != oj {
			return oi // eligible sorts before ineligible
		}
		return si > sj
	})
	// Mark the best eligible release of each edition as the recommended pick.
	recommended := map[string]bool{}
	for i := range out {
		if out[i].Eligible && !recommended[out[i].Edition] {
			out[i].Recommended = true
			recommended[out[i].Edition] = true
		}
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

// recordBookGrab tracks a book grab for seed cleanup (media_type=book, movie_id=bookID).
func (c *Coordinator) recordBookGrab(ctx context.Context, bookID int64, title, indexer, profile string) {
	seedEnabled, seedRatio, seedHours := c.seedRules(ctx, indexer)
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, version_id, title, indexer, quality_profile, stall_minutes, seed_enabled, seed_ratio, seed_hours, media_type)
		 VALUES (?, 0, ?, ?, ?, 0, ?, ?, ?, 'book')`,
		bookID, title, indexer, profile, boolToInt(seedEnabled), seedRatio, seedHours)
	if err != nil {
		c.log.Warn("book: record grab failed", "err", err)
	}
}

func (c *Coordinator) markBookGrabsImported(ctx context.Context, bookID int64) {
	_, _ = c.db.ExecContext(ctx, `UPDATE grabs SET status = 'imported' WHERE movie_id = ? AND status = 'grabbed' AND media_type = 'book'`, bookID)
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
	if time.Since(parseTime(g.GrabbedAt)) < time.Duration(g.StallMinutes)*time.Minute {
		return
	}
	item, found := findQueued(queue, g.Title)
	stalled := !found ||
		item.State == "error" || item.State == "stalledDL" || item.State == "missingFiles" ||
		(item.Progress < 1.0 && item.DownSpeed == 0)
	if !stalled {
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
	for _, b := range all {
		if !b.Monitored {
			continue
		}
		sp := c.bookProfile(ctx, b.QualityProfile)
		matched := c.dropBlockedBook(ctx, b.ID, releasesForBook(res.Releases, b))
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
			if err := c.grabTo(ctx, best.Indexer, best.DownloadURL, best.Title, bookCategory); err != nil {
				continue
			}
			c.recordBookGrab(ctx, b.ID, best.Title, best.Indexer, b.QualityProfile)
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
