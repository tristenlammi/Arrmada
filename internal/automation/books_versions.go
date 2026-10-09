package automation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// Extra audiobook versions (books.AudioVersion) ride on the machinery the standard
// audiobook uses — the same indexer search, the same title gate, the same profile
// scoring — with three differences:
//
//   - a release counts as a version when it mentions one of the version's terms
//     ("GraphicAudio", "full cast", a narrator's name), and while a book has versions the
//     standard audiobook never takes a release that belongs to one of them;
//   - the grab row carries the version id, so the finished download is filed as that
//     version even if its name doesn't say so;
//   - a version is filed as "<Title> (<Label>)" beside the standard audiobook's folder,
//     which Audiobookshelf lists as its own book.

// defaultAudioScores is used for a version when the book's profile scores no audiobook
// format at all (an ebook-only profile): adding a version is the user asking for audio.
var defaultAudioScores = map[string]int{"M4B": 40, "M4A": 30, "MP3": 20, "AAC": 15, "FLAC": 10, "OPUS": 10, "OGG": 5}

// versionProfile adapts the book's profile for picking one version's release: audio
// formats are always acceptable, and a reject term the version itself asks for (a
// profile that rejects "GraphicAudio" for the standard audiobook) doesn't veto it.
func versionProfile(sp quality.StoredProfile, v books.AudioVersion) quality.StoredProfile {
	out := sp
	_, wantAudio := books.WantedEditions(sp.FormatScores)
	if !wantAudio {
		out.FormatScores = map[string]int{}
		for f, s := range sp.FormatScores {
			out.FormatScores[f] = s
		}
		for f, s := range defaultAudioScores {
			out.FormatScores[f] = s
		}
	}
	if len(sp.Rejected) > 0 {
		out.Rejected = make([]string, 0, len(sp.Rejected))
		for _, r := range sp.Rejected {
			if !v.Matches(r) {
				out.Rejected = append(out.Rejected, r)
			}
		}
	}
	return out
}

// dropVersionReleases removes the releases that belong to one of the book's versions,
// so the standard audiobook doesn't take a full-cast production the user wants filed
// separately. A no-op for a book without versions.
func dropVersionReleases(b books.Book, releases []indexer.Release) []indexer.Release {
	if len(b.AudioVersions) == 0 {
		return releases
	}
	out := releases[:0:0]
	for _, rel := range releases {
		if books.VersionFor(b.AudioVersions, bookScoreText(rel)) == nil {
			out = append(out, rel)
		}
	}
	return out
}

// releasesForVersion keeps the releases that mention the version's terms.
func releasesForVersion(v books.AudioVersion, releases []indexer.Release) []indexer.Release {
	out := releases[:0:0]
	for _, rel := range releases {
		if v.Matches(bookScoreText(rel)) {
			out = append(out, rel)
		}
	}
	return out
}

// versionWanted reports whether the automatic search should look for this version.
func versionWanted(v books.AudioVersion) bool {
	return v.Monitored && v.File == nil && len(v.Terms) > 0
}

// grabAudioVersionExcluding searches for one version and grabs the best release for it,
// skipping the normalized titles in exclude (a stall fail-over's stalled release). It
// returns the title it grabbed ("" for none); the error is the search's, when it couldn't
// run at all, so an outage isn't read as "not found".
func (c *Coordinator) grabAudioVersionExcluding(ctx context.Context, b books.Book, v books.AudioVersion, sp quality.StoredProfile, exclude map[string]bool) (string, error) {
	title, _, err := c.grabAudioVersionCounted(ctx, b, v, sp, exclude)
	return title, err
}

// grabAudioVersionCounted is grabAudioVersionExcluding that also counts what it saw.
func (c *Coordinator) grabAudioVersionCounted(ctx context.Context, b books.Book, v books.AudioVersion, sp quality.StoredProfile, exclude map[string]bool) (string, editionStats, error) {
	var st editionStats
	res, err := c.searchBook(ctx, b, books.KindAudiobook)
	if err != nil {
		return "", st, err
	}
	st.returned = len(res.Releases)
	if len(res.Releases) == 0 {
		return "", st, nil
	}
	rels := releasesForVersion(v, c.releasesForThisBook(ctx, b, res.Releases))
	st.matching = len(rels)
	if len(rels) == 0 {
		c.log.Info("book: no release matched this audiobook version", "title", b.Title, "version", v.Label, "terms", strings.Join(v.Terms, ", "))
		return "", st, nil
	}
	rels, err = c.dropUngrabbableBook(ctx, b.ID, rels)
	if err != nil {
		c.skipUnreadable(b.Title, err)
		return "", st, err
	}
	rels = dropPendingBook(rels, exclude) // same normalized-title filter
	st.usable = len(rels)
	best := pickBestBookForKind(versionProfile(sp, v), rels, books.KindAudiobook)
	if best == nil {
		c.log.Info("book: no acceptable release for this audiobook version", "title", b.Title, "version", v.Label)
		return "", st, nil
	}
	hash, err := c.grabTo(ctx, best.Indexer, best.DownloadURL, best.Title, bookCategory)
	if err != nil {
		c.log.Warn("book: grab failed", "title", b.Title, "version", v.Label, "err", err)
		return "", st, nil
	}
	c.recordBookGrab(ctx, b.ID, v.ID, best.Title, best.Indexer, b.QualityProfile, hash)
	c.books.AddEvent(ctx, b.ID, "grabbed", fmt.Sprintf("Grabbed the %q audiobook from %s: %s", v.Label, best.Indexer, best.Title))
	c.log.Info("book: grabbing audiobook version", "title", b.Title, "version", v.Label, "release", best.Title)
	return best.Title, st, nil
}

// searchVersion searches one audio version and adds what it found to out.
func (c *Coordinator) searchVersion(ctx context.Context, b books.Book, v books.AudioVersion, sp quality.StoredProfile, out *SearchOutcome) error {
	title, st, err := c.grabAudioVersionCounted(ctx, b, v, sp, nil)
	out.Searched = true
	out.Returned += st.returned
	out.Matching += st.matching
	out.Usable += st.usable
	if title != "" {
		out.Grabbed++
		out.GrabbedTitles = append(out.GrabbedTitles, title)
	}
	return err
}

// SearchAudioVersionNow searches for one version right away (the version's Search
// button, and straight after a version is added) and says what it found. The book's
// claim is taken, so it never overlaps a search of the whole book.
func (c *Coordinator) SearchAudioVersionNow(ctx context.Context, bookID, versionID int64) (SearchOutcome, error) {
	if c.books == nil {
		return SearchOutcome{}, errBooksNotReady
	}
	b, err := c.books.Get(ctx, bookID)
	if err != nil {
		return SearchOutcome{}, err
	}
	for _, v := range b.AudioVersions {
		if v.ID != versionID {
			continue
		}
		if len(v.Terms) == 0 {
			return SearchOutcome{}, fmt.Errorf("the %q version has no search words — add some, or grab a release for it by hand", v.Label)
		}
		release, ok := c.claims.claim(bookKey(bookID))
		if !ok {
			return SearchOutcome{Reason: ReasonAlreadySearching}, ErrAlreadySearching
		}
		defer release()
		var out SearchOutcome
		err := c.searchVersion(ctx, b, v, c.bookProfile(ctx, b.QualityProfile), &out)
		out.settle()
		return out, err
	}
	return SearchOutcome{}, books.ErrVersionNotFound
}

// audioVersionForDownload decides which version, if any, a finished download's audio
// belongs to: the version it was grabbed for, else the version its name mentions.
//
// The versions are read here rather than trusted from b: the import finds its book
// by matching the download's name against the whole library, and that list is loaded
// without versions — so a download grabbed "as Narrator" was filed as the standard
// audiobook, over the one already there.
func (c *Coordinator) audioVersionForDownload(ctx context.Context, b books.Book, hash, name string) *books.AudioVersion {
	if full, err := c.books.Get(ctx, b.ID); err == nil {
		b.AudioVersions = full.AudioVersions
	}
	if len(b.AudioVersions) == 0 {
		return nil
	}
	if hash != "" {
		var vid int64
		err := c.db.QueryRowContext(ctx,
			`SELECT version_id FROM grabs WHERE media_type = 'book' AND movie_id = ? AND lower(info_hash) = ? ORDER BY id DESC LIMIT 1`,
			b.ID, strings.ToLower(hash)).Scan(&vid)
		if err == nil && vid > 0 {
			for i := range b.AudioVersions {
				if b.AudioVersions[i].ID == vid {
					return &b.AudioVersions[i]
				}
			}
		} else if err == nil {
			return nil // grabbed for the standard audiobook
		} else if !errors.Is(err, sql.ErrNoRows) {
			c.log.Warn("book import: couldn't read the grab's version", "err", err)
		}
	}
	return books.VersionFor(b.AudioVersions, name)
}

// importAudioVersion files a download's audio as one version of the book.
func (c *Coordinator) importAudioVersion(ctx context.Context, b books.Book, v books.AudioVersion, files []library.FoundFile, infoHash, downloadName string) bool {
	if len(files) == 0 {
		return false
	}
	bi, err := c.imp.ImportBookEdition(b.Author, v.FolderTitle(b.Title), files)
	if err != nil {
		c.log.Warn("book: import failed", "title", b.Title, "version", v.Label, "err", err)
		return false
	}
	if err := c.books.SetAudioVersionFile(ctx, v.ID, bi.TargetPath, bi.Format, bi.SizeBytes, bi.FileCount); err != nil {
		c.log.Warn("book: recording the imported version failed", "title", b.Title, "version", v.Label, "err", err)
		return false
	}
	detail := fmt.Sprintf("Imported the %q audiobook (%s", v.Label, bi.Format)
	if bi.FileCount > 1 {
		detail += fmt.Sprintf(", %d files", bi.FileCount)
	}
	detail += ")"
	if downloadName != "" {
		detail += " from " + downloadName
	}
	c.log.Info("book: imported audiobook version", "title", b.Title, "version", v.Label, "format", bi.Format, "files", bi.FileCount)
	c.books.AddEvent(ctx, b.ID, "imported", detail)
	c.markBookGrabImported(ctx, b.ID, infoHash, downloadName)
	c.bookImported(ctx, b.ID, books.KindAudiobook)
	c.bus.Publish("book.imported", map[string]any{"title": b.Title, "id": b.ID, "edition": books.KindAudiobook, "version": v.Label})
	return true
}

// recordVersionFiles marks a version present from files already on disk.
func (c *Coordinator) recordVersionFiles(ctx context.Context, b books.Book, v books.AudioVersion, files []library.FoundFile, how string) {
	if len(files) == 0 {
		return
	}
	path, total := files[0].Path, files[0].Size
	if len(files) > 1 {
		path = filepath.Dir(files[0].Path)
		total = 0
		for _, f := range files {
			total += f.Size
		}
	}
	format := library.BookFileFormat(files[0].Path)
	if err := c.books.SetAudioVersionFile(ctx, v.ID, path, format, total, len(files)); err != nil {
		return
	}
	c.books.AddEvent(ctx, b.ID, "imported", fmt.Sprintf("Found the %q audiobook %s (%s, %d file%s)", v.Label, how, format, len(files), plural(len(files))))
}

// rescanAudioVersions looks in each version's folder for audio the database doesn't
// know about yet (dropped in by hand, or imported before a restore).
func (c *Coordinator) rescanAudioVersions(ctx context.Context, b books.Book) {
	for _, v := range b.AudioVersions {
		var audio []library.FoundFile
		for _, f := range c.imp.BookLibraryFiles(b.Author, v.FolderTitle(b.Title)) {
			if library.IsAudiobookFile(f.Path) {
				audio = append(audio, f)
			}
		}
		if len(audio) == 0 {
			continue
		}
		if v.File != nil {
			if _, err := os.Stat(v.File.Path); err == nil {
				continue // still where we recorded it
			}
		}
		c.recordVersionFiles(ctx, b, v, audio, "during a rescan")
	}
}

// ManualImportAudioVersion imports a file or folder on disk as one version.
func (c *Coordinator) ManualImportAudioVersion(ctx context.Context, bookID, versionID int64, path string) error {
	if c.books == nil || c.imp == nil {
		return errBooksNotReady
	}
	b, err := c.books.Get(ctx, bookID)
	if err != nil {
		return err
	}
	v, err := c.books.GetAudioVersion(ctx, bookID, versionID)
	if err != nil {
		return err
	}
	var audio []library.FoundFile
	for _, f := range library.FindBookFiles(path) {
		if library.IsAudiobookFile(f.Path) {
			audio = append(audio, f)
		}
	}
	if len(audio) == 0 {
		return fmt.Errorf("no audiobook files at %s", path)
	}
	if !c.importAudioVersion(ctx, b, v, audio, "", filepath.Base(path)) {
		return fmt.Errorf("import failed — see the log")
	}
	return nil
}

// DeleteAudioVersionFile removes a version's file(s) and forgets them; the version
// stays, so it is searched for again.
func (c *Coordinator) DeleteAudioVersionFile(ctx context.Context, bookID, versionID int64) error {
	if c.books == nil {
		return errBooksNotReady
	}
	v, err := c.books.GetAudioVersion(ctx, bookID, versionID)
	if err != nil {
		return err
	}
	if err := c.removeAudioFiles(ctx, bookID, v.File); err != nil {
		return err
	}
	if err := c.books.ClearAudioVersionFile(ctx, v.ID); err != nil {
		return err
	}
	c.books.AddEvent(ctx, bookID, "deleted", fmt.Sprintf("Removed the %q audiobook's files", v.Label))
	return nil
}

// RemoveAudioVersion deletes a version, and its files when asked.
func (c *Coordinator) RemoveAudioVersion(ctx context.Context, bookID, versionID int64, deleteFiles bool) error {
	if c.books == nil {
		return errBooksNotReady
	}
	v, err := c.books.GetAudioVersion(ctx, bookID, versionID)
	if err != nil {
		return err
	}
	if deleteFiles {
		if err := c.removeAudioFiles(ctx, bookID, v.File); err != nil {
			return err
		}
	}
	return c.books.DeleteAudioVersion(ctx, bookID, versionID)
}

// removeAudioFiles recycles (or, with the bin off, deletes) a version's audio and prunes
// its folder. A bin that refuses a file stops it with the error.
func (c *Coordinator) removeAudioFiles(ctx context.Context, bookID int64, f *books.BookFile) error {
	if f == nil || f.Path == "" {
		return nil
	}
	if fi, err := os.Stat(f.Path); err == nil && fi.IsDir() {
		var paths []string
		for _, af := range library.FindBookFiles(f.Path) {
			if library.IsAudiobookFile(af.Path) {
				paths = append(paths, af.Path)
			}
		}
		if err := c.removeBookFiles(ctx, bookID, paths); err != nil {
			return err
		}
		_ = os.Remove(f.Path) // only succeeds once empty
		return nil
	}
	if err := c.removeBookFile(f.Path); err != nil {
		return err
	}
	_ = os.Remove(filepath.Dir(f.Path))
	return nil
}

// versionForScanFolder reports whether a library folder is one of an existing book's
// versions ("<Title> (<Label>)"), so the scan files it there instead of treating it as
// a new book or as the standard audiobook.
func versionForScanFolder(folderTitle string, all []books.Book) (books.Book, books.AudioVersion, bool) {
	want := normTitle(folderTitle)
	if want == "" {
		return books.Book{}, books.AudioVersion{}, false
	}
	for _, b := range all {
		for _, v := range b.AudioVersions {
			if normTitle(v.FolderTitle(b.Title)) == want {
				return b, v, true
			}
		}
	}
	return books.Book{}, books.AudioVersion{}, false
}
