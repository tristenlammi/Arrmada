package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Service is the Books module's application logic.
type Service struct {
	repo *Repo
	meta metadata.BookProvider
	log  *slog.Logger

	upgrade upgradeState // the "upgrade library to Hardcover" job (upgrade.go)

	items         ItemRekeyer    // moves listening places in a merge (merge.go)
	announceMerge MergeAnnouncer // queues the kept book's follow-up work (merge.go)
}

// NewService wires the module.
func NewService(db store.Pool, meta metadata.BookProvider, log *slog.Logger) *Service {
	return &Service{repo: NewRepo(db), meta: meta, log: log}
}

// MetadataAvailable reports whether the book provider is usable (Open Library always is).
func (s *Service) MetadataAvailable() bool { return s.meta.Available() }

// Lookup searches the current catalogue for books to add.
func (s *Service) Lookup(ctx context.Context, query string) ([]metadata.BookResult, error) {
	return s.meta.SearchBooks(ctx, query)
}

// sourceLookup is what a provider that can be asked for a specific catalogue offers.
type sourceLookup interface {
	SearchBooksFrom(ctx context.Context, source, query string) ([]metadata.BookResult, error)
	Source() string
}

// LookupFrom searches a named catalogue ("openlibrary" / "hardcover") when the provider
// can, else the usual one — the "show Open Library results too" button.
func (s *Service) LookupFrom(ctx context.Context, query, source string) ([]metadata.BookResult, error) {
	if sl, ok := s.meta.(sourceLookup); ok && source != "" {
		return sl.SearchBooksFrom(ctx, source, query)
	}
	return s.Lookup(ctx, query)
}

// VerifyHardcover runs a live check of the Hardcover key, when the provider has one.
func (s *Service) VerifyHardcover(ctx context.Context) (string, error) {
	if v, ok := s.meta.(interface {
		VerifyHardcover(context.Context) (string, error)
	}); ok {
		return v.VerifyHardcover(ctx)
	}
	return "", fmt.Errorf("Hardcover is not configured")
}

// MetadataSource names the catalogue searches currently go to.
func (s *Service) MetadataSource() string {
	if sl, ok := s.meta.(sourceLookup); ok {
		return sl.Source()
	}
	return metadata.SourceOpenLibrary
}

// SearchAuthors finds authors by name (Discover).
func (s *Service) SearchAuthors(ctx context.Context, query string) ([]metadata.AuthorResult, error) {
	return s.meta.SearchAuthors(ctx, query)
}

// AuthorWorks returns an author's catalogue (Discover).
func (s *Service) AuthorWorks(ctx context.Context, key string, limit int) ([]metadata.BookResult, error) {
	return s.meta.AuthorWorks(ctx, key, limit)
}

// Trending returns books trending this week (Discover).
func (s *Service) Trending(ctx context.Context) ([]metadata.BookResult, error) {
	return s.meta.TrendingBooks(ctx)
}

// BySubject returns books for a subject/genre (Discover).
func (s *Service) BySubject(ctx context.Context, subject string, limit int) ([]metadata.BookResult, error) {
	return s.meta.BooksBySubject(ctx, subject, limit)
}

// Detail fetches full metadata (description, subjects) for a work — used by the Discover
// request modal.
func (s *Service) Detail(ctx context.Context, key string) (*metadata.BookDetails, error) {
	return s.meta.GetBook(ctx, key)
}

// List returns the library, each book with its extra audio versions.
func (s *Service) List(ctx context.Context) ([]Book, error) {
	list, err := s.repo.List(ctx)
	if err != nil || len(list) == 0 {
		return list, err
	}
	if all, verr := s.repo.AllAudioVersions(ctx); verr == nil && len(all) > 0 {
		for i := range list {
			list[i].AudioVersions = all[list[i].ID]
		}
	}
	return list, nil
}

// ByKeysOrIDs returns the books under any of these catalogue keys (current or former) or
// with any of these ids, each with its extra audio versions: a request for the audiobook
// is fulfilled by one of those as much as by the standard edition.
func (s *Service) ByKeysOrIDs(ctx context.Context, olKeys []string, ids []int64) ([]Book, error) {
	list, err := s.repo.ByKeysOrIDs(ctx, olKeys, ids)
	if err != nil || len(list) == 0 {
		return list, err
	}
	bookIDs := make([]int64, 0, len(list))
	for _, b := range list {
		bookIDs = append(bookIDs, b.ID)
	}
	if vs, verr := s.repo.AudioVersionsFor(ctx, bookIDs); verr == nil {
		for i := range list {
			list[i].AudioVersions = vs[list[i].ID]
		}
	}
	return list, nil
}

// Get returns one book with its extra audio versions.
func (s *Service) Get(ctx context.Context, id int64) (Book, error) {
	b, err := s.repo.Get(ctx, id)
	if err != nil {
		return b, err
	}
	b.AudioVersions, _ = s.repo.ListAudioVersions(ctx, id)
	return b, nil
}

// Add pulls details for an Open Library work id and adds it. fallback supplies the
// year/author/cover/title from the search result — the work endpoint doesn't carry the
// publish year, so we backfill from what the lookup already knew.
//
// Every key the book is reached by is kept: the one asked for, the catalogue's canonical
// one, and — when the book is already in the library — the asked-for key as one more of
// the existing row's keys, so the card that carried it reads In library from now on.
func (s *Service) Add(ctx context.Context, olKey, qualityProfile string, monitored bool, fallback metadata.BookResult) (Book, error) {
	// A key the library already knows, current or former, is that book: no fetch needed.
	if existing, ok := s.findByKey(ctx, olKey); ok {
		return existing, ErrExists
	}
	d, err := s.meta.GetBook(ctx, olKey)
	if err != nil {
		return Book{}, fmt.Errorf("fetch metadata: %w", err)
	}
	if d.Key == "" {
		d.Key = olKey
	}
	if d.Key != olKey {
		if existing, ok := s.findByKey(ctx, d.Key); ok {
			s.aliasKey(ctx, olKey, existing.ID)
			return existing, ErrExists
		}
	}
	authorKey := d.AuthorKey
	if d.Author == "" {
		authorKey = fallback.AuthorKey // the author came from the search result
	}
	b := Book{
		OLKey: d.Key, Title: orStr(d.Title, fallback.Title), Author: orStr(d.Author, fallback.Author),
		Year: d.Year, CoverURL: orStr(d.CoverURL, fallback.CoverURL),
		Description: d.Description, Subjects: d.Subjects, Monitored: monitored, QualityProfile: qualityProfile,
		ReleaseDate: orStr(d.ReleaseDate, fallback.ReleaseDate),
	}
	if b.Year == 0 {
		b.Year = fallback.Year
	}
	// The same book under another catalogue's key is the same book. Hand back the
	// existing row with ErrExists so callers that just want "the library's copy" (the
	// disk scan, a request) can use it.
	if existing, ok := s.findDuplicate(ctx, b.Title, b.Author); ok {
		s.aliasKey(ctx, olKey, existing.ID)
		s.aliasKey(ctx, b.OLKey, existing.ID)
		return existing, ErrExists
	}
	created, err := s.repo.Create(ctx, b)
	if errors.Is(err, ErrExists) {
		if existing, ok := s.findByKey(ctx, b.OLKey); ok {
			s.aliasKey(ctx, olKey, existing.ID)
			return existing, ErrExists
		}
		return Book{}, err
	}
	if err != nil {
		return Book{}, err
	}
	// The catalogue answered with its canonical key for the one asked for: keep both.
	s.aliasKey(ctx, olKey, created.ID)
	if d.SeriesName != "" {
		_ = s.repo.SetSeriesRef(ctx, created.ID, d.SeriesName, d.SeriesPosition, d.SeriesKey)
		created.SeriesName, created.SeriesPosition, created.SeriesKey = d.SeriesName, d.SeriesPosition, d.SeriesKey
	}
	created.AuthorID = s.linkAuthor(ctx, created.ID, created.Author, authorKey, false)
	s.log.Info("book added", "title", created.Title, "author", created.Author)
	s.repo.AddEvent(ctx, created.ID, "added", "Added to library")
	return created, nil
}

// Covers returns candidate cover images for a book (Open Library editions + Google Books)
// for the cover picker.
func (s *Service) Covers(ctx context.Context, id int64) ([]string, error) {
	b, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.meta.Covers(ctx, b.OLKey, b.Title, b.Author)
}

// SetCover changes a book's cover image (a picked remote cover, or the served path of a
// custom upload).
func (s *Service) SetCover(ctx context.Context, id int64, coverURL string) error {
	return s.repo.SetCoverURL(ctx, id, coverURL)
}

// FillCover sets a cover only if the book still has none (a library scan's fill-in from a
// Calibre folder's cover.jpg); it reports whether the book took it.
func (s *Service) FillCover(ctx context.Context, id int64, coverURL string) (bool, error) {
	return s.repo.FillCoverURL(ctx, id, coverURL)
}

// FillSeries records a series only if the book still has none (a library scan's fill-in
// from a Calibre metadata.opf); it reports whether the book took it.
func (s *Service) FillSeries(ctx context.Context, id int64, name string, position float64) (bool, error) {
	return s.repo.FillSeries(ctx, id, name, position)
}

// FillISBN records an ISBN only if the catalogue's value has none (a library scan's
// fill-in from a Calibre metadata.opf); one that fails its check digit is ignored.
func (s *Service) FillISBN(ctx context.Context, id int64, isbn string) (bool, error) {
	return s.repo.FillISBN(ctx, id, metadata.NormalizeISBN(isbn))
}

// ErrNarratorInvalid is a narrator name that can't be stored.
var ErrNarratorInvalid = errors.New("invalid narrator")

// CleanNarrator tidies a narrator typed by hand: spaces collapsed, at most 200
// characters. "" is allowed and means the audio files' tags say.
func CleanNarrator(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 200 {
		return "", fmt.Errorf("%w: keep the narrator under 200 characters", ErrNarratorInvalid)
	}
	return s, nil
}

// SetNarrator records who reads the book's standard audiobook, typed by hand ("" hands
// it back to the files' tags). The narrator is one of the book's edited details.
func (s *Service) SetNarrator(ctx context.Context, id int64, narrator string) error {
	n, err := CleanNarrator(narrator)
	if err != nil {
		return err
	}
	req := EditRequest{Reset: []string{FieldNarrator}}
	if n != "" {
		req = EditRequest{Set: map[string]json.RawMessage{FieldNarrator: jsonOf(n)}}
	}
	_, err = s.EditDetails(ctx, id, req)
	return err
}

// TouchMeta marks a book's details as changed now (see Repo.TouchMeta), so listening
// apps fetch its cover again.
func (s *Service) TouchMeta(ctx context.Context, id int64) error { return s.repo.TouchMeta(ctx, id) }

// OverrideMetadata applies a manual metadata correction (title/author/year/description/
// cover) — the older all-at-once form of EditDetails, kept for API callers. The details
// become hand edits (the catalogue's values stay underneath); the cover is set as the
// cover picker sets it.
func (s *Service) OverrideMetadata(ctx context.Context, id int64, title, author string, year int, description, coverURL string) error {
	before, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.EditDetails(ctx, id, EditRequest{Set: map[string]json.RawMessage{
		FieldTitle: jsonOf(title), FieldAuthor: jsonOf(author), FieldYear: jsonOf(year), FieldDescription: jsonOf(description),
	}}); err != nil {
		return err
	}
	if coverURL != before.CoverURL {
		return s.repo.SetCoverURL(ctx, id, coverURL)
	}
	return nil
}

// AddWorks bulk-adds a list of works (an author's catalogue) to the library, skipping any
// already present. Rows are created directly from the provided metadata — no per-book
// network fetch — so this stays fast for a big catalogue; descriptions/subjects fill in on
// the next refresh. Returns the books actually added and how many were skipped as dupes.
func (s *Service) AddWorks(ctx context.Context, works []metadata.BookResult, profile string, monitored bool) ([]Book, int) {
	return s.addWorks(ctx, works, profile, monitored, 0)
}

// AddAuthorWorks is AddWorks for one author's catalogue ("Add author", a followed
// author's new books): every added book is linked to that author, whoever the
// catalogue lists first on a co-written book, and books by them already in the library
// that aren't linked to anyone are linked too.
func (s *Service) AddAuthorWorks(ctx context.Context, authorID int64, works []metadata.BookResult, profile string, monitored bool) ([]Book, int) {
	return s.addWorks(ctx, works, profile, monitored, authorID)
}

func (s *Service) addWorks(ctx context.Context, works []metadata.BookResult, profile string, monitored bool, authorID int64) ([]Book, int) {
	return s.addWorksNoted(ctx, works, profile, monitored, authorID, "Added from the author's catalogue")
}

// addWorksNoted is addWorks with the timeline line each added book gets ("New from
// <author>" for a followed author's new book).
func (s *Service) addWorksNoted(ctx context.Context, works []metadata.BookResult, profile string, monitored bool, authorID int64, note string) ([]Book, int) {
	var author Author
	if authorID > 0 {
		var err error
		if author, err = s.repo.getAuthor(ctx, s.repo.db, authorID); err != nil {
			s.log.Warn("add author: unknown author row", "author_id", authorID, "err", err)
			authorID = 0
		}
	}
	var added []Book
	skipped := 0
	// One pass over the library for the title-and-author check, then each incoming
	// work is checked against the library AND the works already taken from this list:
	// an author's catalogue routinely lists the same novel several times.
	list, _ := s.repo.List(ctx)
	have := NewIdentityIndex(list)
	for _, wk := range works {
		if wk.Key == "" || wk.Title == "" {
			continue
		}
		if held, ok := have.Find(wk.Title, wk.Author); ok {
			skipped++
			if authorID > 0 && SameAuthorName(held.Author, author.Name) {
				if cur, err := bookAuthorID(ctx, s.repo.db, held.ID); err == nil && cur == 0 {
					_ = setBookAuthor(ctx, s.repo.db, held.ID, authorID)
				}
			}
			continue
		}
		created, err := s.repo.Create(ctx, Book{
			OLKey: wk.Key, Title: wk.Title, Author: wk.Author, Year: wk.Year,
			CoverURL: wk.CoverURL, Monitored: monitored, QualityProfile: profile, ReleaseDate: wk.ReleaseDate,
		})
		if errors.Is(err, ErrExists) {
			skipped++
			continue
		}
		if err != nil {
			s.log.Warn("add author: create failed", "title", wk.Title, "err", err)
			continue
		}
		if authorID > 0 {
			if err := setBookAuthor(ctx, s.repo.db, created.ID, authorID); err == nil {
				created.AuthorID = authorID
			}
		} else {
			created.AuthorID = s.linkAuthor(ctx, created.ID, created.Author, wk.AuthorKey, false)
		}
		have.Add(created)
		s.repo.AddEvent(ctx, created.ID, "added", note)
		added = append(added, created)
	}
	if len(added) > 0 {
		s.log.Info("author catalogue added", "added", len(added), "skipped", skipped)
	}
	return added, skipped
}

// SetMonitored toggles a book.
func (s *Service) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	return s.repo.SetMonitored(ctx, id, monitored)
}

// SetKeepCatalogue tells the Hardcover re-match to leave a book as it is (or to try
// it again).
func (s *Service) SetKeepCatalogue(ctx context.Context, id int64, keep bool) error {
	if err := s.repo.SetKeepCatalogue(ctx, id, keep); err != nil {
		return err
	}
	if keep {
		s.repo.AddEvent(ctx, id, "kept", "Left on its current catalogue entry — the Hardcover re-match skips it")
		s.forgetUnmatched(id)
	}
	return nil
}

// SetQualityProfile changes a book's quality profile.
func (s *Service) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	return s.repo.SetQualityProfile(ctx, id, profile)
}

// MarkImported records that a book edition (ebook|audiobook) landed on disk. files is
// how many files the edition has (>1 = a folder, e.g. an mp3 audiobook).
func (s *Service) MarkImported(ctx context.Context, id int64, kind, path, format string, size int64, files int) error {
	if err := s.repo.SetEdition(ctx, id, kind, path, format, size, files); err != nil {
		return err
	}
	// A rename journal entry for the edition describes a place it no longer is.
	if err := s.ClearRenameJournal(ctx, id, 0, kind); err != nil {
		return err
	}
	s.log.Info("book imported", "id", id, "kind", kind, "format", format, "files", files)
	return nil
}

// ClearEdition forgets a book edition (after the user deletes its file).
func (s *Service) ClearEdition(ctx context.Context, id int64, kind string) error {
	if err := s.repo.ClearEdition(ctx, id, kind); err != nil {
		return err
	}
	if err := s.ClearRenameJournal(ctx, id, 0, kind); err != nil {
		return err
	}
	s.repo.AddEvent(ctx, id, "deleted", "Removed the "+kind+" edition")
	return nil
}

// Refresh re-pulls catalogue metadata (description, cover, subjects, author, series) for
// a book. A catalogue that can't answer leaves the book as it was; that isn't an error.
func (s *Service) Refresh(ctx context.Context, id int64) (Book, error) {
	b, _, err := s.refresh(ctx, id)
	return b, err
}

// refresh is Refresh that also says why the catalogue lookup failed (fetchErr), so the
// refresh job can tell "Hardcover's budget is spent, stop" from "this key is gone". The
// book is stamped as checked unless the lookup was refused for budget or cut short, so
// the job doesn't come straight back to a book it just tried.
func (s *Service) refresh(ctx context.Context, id int64) (_ Book, fetchErr, err error) {
	b, err := s.repo.Get(ctx, id)
	if err != nil {
		return Book{}, nil, err
	}
	d, derr := s.meta.GetBook(ctx, b.OLKey)
	if derr == nil && d == nil {
		derr = errors.New("the catalogue returned no book")
	}
	switch {
	case derr == nil:
	case errors.Is(derr, metadata.ErrHardcoverBudget) || ctx.Err() != nil:
		b, err = s.repo.Get(ctx, id)
		return b, derr, err
	default:
		// The provider's own words, so a query Hardcover rejects shows why in the log.
		s.log.Info("book refresh: catalogue lookup failed", "title", b.Title, "key", b.OLKey, "err", derr)
	}
	if err := s.repo.StampMetaChecked(ctx, id); err != nil {
		s.log.Warn("book refresh: couldn't record the check", "title", b.Title, "err", err)
	}
	if derr == nil {
		// Compared and written as the catalogue has the book: a detail edited by hand
		// keeps its edit, and the catalogue's new value only goes underneath it.
		shown := b
		b = b.Matched()
		if d.Description != "" {
			b.Description = d.Description
		}
		if d.CoverURL != "" {
			b.CoverURL = d.CoverURL
		}
		if len(d.Subjects) > 0 {
			b.Subjects = d.Subjects
		}
		_ = s.repo.UpdateMeta(ctx, b.ID, b.Description, b.CoverURL, b.Subjects)
		// Existing books pick up their release date here (and through the refresh-books
		// job); a catalogue that doesn't know one leaves the stored date alone.
		_ = s.repo.SetReleaseDate(ctx, b.ID, d.ReleaseDate)
		// The catalogue's author wins over the one on file: a book filed under its
		// illustrator (the catalogue once listed that contributor first) is put right
		// here, and every indexer search from then on carries the right name.
		authorChanged := false
		if d.Author != "" && !strings.EqualFold(d.Author, b.Author) {
			old := b.Author
			if err := s.repo.SetMatched(ctx, b.ID, map[string]any{FieldAuthor: d.Author}); err == nil {
				b.Author = d.Author
				authorChanged = true
				if shown.Edited(FieldAuthor) {
					s.repo.AddEvent(ctx, b.ID, "refreshed", "The catalogue's author is now "+d.Author+" (your edit, "+shown.Author+", is kept)")
				} else {
					s.repo.AddEvent(ctx, b.ID, "refreshed", "Author corrected: "+old+" → "+d.Author)
				}
			}
		}
		// The catalogue's key for the author on file links the book to its author by
		// identity; a corrected author is relinked. An author edited by hand decides the
		// link itself (EditDetails), so the catalogue's doesn't move it.
		if !shown.Edited(FieldAuthor) {
			key := ""
			if d.Author != "" && strings.EqualFold(d.Author, b.Author) {
				key = d.AuthorKey
			}
			s.linkAuthor(ctx, b.ID, b.Author, key, authorChanged)
		}
		// A series the catalogue knows outranks one guessed from a release name, and
		// books added before series keys existed pick theirs up here.
		if d.SeriesName != "" && (b.SeriesKey == "" || d.SeriesKey != "") {
			_ = s.repo.SetSeriesRef(ctx, b.ID, d.SeriesName, d.SeriesPosition, d.SeriesKey)
		}
	}
	b, err = s.repo.Get(ctx, id)
	return b, derr, err
}

// MatchByRelease finds the book a release name refers to — used to route a finished
// download to the right book.
//
// Matching is word-boundary based: the book's title must appear in the release name
// as whole words. The old substring compare on boundary-less keys mis-routed
// releases — "Dune" matched inside "Dune Messiah", so the Messiah files imported as
// Dune — and its 3-character floor made short titles ("It") unmatchable. Every
// candidate is evaluated (not first-hit): when several titles match, ones whose
// author also appears in the release win, and the LONGEST title among those is
// chosen — the most specific claim ("Dune Messiah" beats "Dune" for a Messiah
// release; a plain "Dune" release contains no "dune messiah" word run, so only
// "Dune" matches it).
func (s *Service) MatchByRelease(ctx context.Context, releaseName string) (Book, bool) {
	all, err := s.repo.List(ctx)
	if err != nil {
		return Book{}, false
	}
	return matchRelease(all, releaseName)
}

// SearchState / RecordSearchMiss / ResetSearchMisses drive the missing-books sweep's
// backoff, mirroring the movie and series services.
func (s *Service) SearchState(ctx context.Context, bookID int64) (string, int) {
	return s.repo.SearchState(ctx, bookID)
}

func (s *Service) RecordSearchMiss(ctx context.Context, bookID int64) {
	s.repo.RecordSearchMiss(ctx, bookID)
}

func (s *Service) ResetSearchMisses(ctx context.Context, bookID int64) {
	s.repo.ResetSearchMisses(ctx, bookID)
}

// SetSeries records the series a book belongs to, learned from a matched release.
func (s *Service) SetSeries(ctx context.Context, bookID int64, name string, position float64) error {
	return s.repo.SetSeries(ctx, bookID, name, position)
}

// SeriesSiblings returns every book in the library sharing this series name, in reading
// order — the basis for showing a series on a book's page and spotting the gaps in it.
func (s *Service) SeriesSiblings(ctx context.Context, seriesName string) ([]Book, error) {
	return s.repo.SeriesSiblings(ctx, seriesName)
}

// Matcher returns a release→book matcher over ONE library snapshot, for a caller resolving
// many release names in a batch (a page of indexer results). MatchByRelease re-reads the
// whole books table per call, so filtering 60 search results with it meant 60 full table
// scans; this reads once.
func (s *Service) Matcher(ctx context.Context) func(releaseName string) (Book, bool) {
	all, err := s.repo.List(ctx)
	if err != nil {
		// No snapshot means no basis to judge a release against — report no match rather
		// than silently matching everything.
		return func(string) (Book, bool) { return Book{}, false }
	}
	return MatcherOver(all)
}

// MatcherOver is Matcher over a library snapshot the caller already holds, so a sweep that
// has just listed the books doesn't read the table a second time to match against it.
func MatcherOver(all []Book) func(releaseName string) (Book, bool) {
	lib := foldLibrary(all)
	return func(name string) (Book, bool) { return lib.match(name) }
}

// foldedBook is a library book with its title and author folded once, so matching a
// page of releases doesn't refold the whole library per release.
type foldedBook struct {
	book          Book
	title, author titleForms
	// The catalogue's title and author when they were edited by hand (Book.CatalogueNames:
	// the title only when it's a variant of the edited one), since releases may still be
	// named the catalogue's way.
	matchedTitle, matchedAuthor *titleForms
}

type foldedLibrary []foldedBook

func foldLibrary(all []Book) foldedLibrary {
	out := make(foldedLibrary, 0, len(all))
	for _, b := range all {
		fb := foldedBook{book: b, title: titleFormsOf(b.Title), author: titleFormsOf(b.Author)}
		if c, ok := b.CatalogueNames(); ok {
			if t := titleFormsOf(c.Title); c.Title != b.Title && t.key() != "" {
				fb.matchedTitle = &t
			}
			if a := titleFormsOf(c.Author); c.Author != b.Author && a.key() != "" {
				fb.matchedAuthor = &a
			}
		}
		out = append(out, fb)
	}
	return out
}

// titleIn reports whether the book's title — or the catalogue's, when usable — is in the
// name, how long the matched one is (the longest wins between books), and whether it was
// the shown title that matched (which wins a tie over a catalogue title).
func (fb foldedBook) titleIn(rel nameForms) (ok bool, n int, shown bool) {
	if fb.title.in(rel) {
		ok, n, shown = true, len(fb.title.key()), true
	}
	if fb.matchedTitle != nil && fb.matchedTitle.in(rel) && len(fb.matchedTitle.key()) > n {
		ok, n, shown = true, len(fb.matchedTitle.key()), false
	}
	return ok, n, shown
}

func (fb foldedBook) authorIn(rel nameForms) bool {
	return fb.author.anyIn(rel) || (fb.matchedAuthor != nil && fb.matchedAuthor.anyIn(rel))
}

// matchRelease is MatchByRelease's pure core (separated so it's table-testable).
func matchRelease(all []Book, releaseName string) (Book, bool) {
	return foldLibrary(all).match(releaseName)
}

func (lib foldedLibrary) match(releaseName string) (Book, bool) {
	rel := formsOf(releaseName)
	if rel.empty() {
		return Book{}, false
	}
	var best Book
	var bestScore matchScore
	found := false
	for _, fb := range lib {
		in, n, shown := fb.titleIn(rel)
		if !in {
			continue
		}
		score := matchScore{author: fb.authorIn(rel), titleLen: n, shown: shown}
		if !found || score.beats(bestScore) {
			best, bestScore, found = fb.book, score, true
		}
	}
	return best, found
}

// matchScore is how strongly a release names one library book.
type matchScore struct {
	author   bool // the book's author is in the name too
	titleLen int  // how much title matched
	shown    bool // it was the title the book shows, not the catalogue's kept under an edit
}

// beats ranks candidates: author-confirmed beats not, then the longer (more specific)
// title wins, then a book's shown title beats another book's catalogue title — a release
// named "Dune" is the book called Dune, not one only catalogued as Dune underneath an edit.
func (s matchScore) beats(o matchScore) bool {
	if s.author != o.author {
		return s.author
	}
	if s.titleLen != o.titleLen {
		return s.titleLen > o.titleLen
	}
	return s.shown && !o.shown
}

// containsWords reports whether needle's words appear, contiguously and on word
// boundaries, in hay. Both must already be wordKey-normalized.
func containsWords(hay, needle string) bool {
	return strings.Contains(" "+hay+" ", " "+needle+" ")
}

// Rematch re-points a book at a different Open Library work — the fix for a book the
// providers (or the library scan, which takes the first search hit) identified wrongly.
// Metadata is re-pulled from the chosen work; the files already on disk, monitoring and the
// quality profile are kept.
//
// fallback carries what the picker already knew (title/author/year/cover), because the work
// endpoint doesn't return a publish year and often has no cover.
//
// Details edited by hand stay edited unless clearEdits (the person's answer when the book
// was re-matched to a different book): the new work's details go underneath them.
func (s *Service) Rematch(ctx context.Context, id int64, olKey string, fallback metadata.BookResult, clearEdits bool) (Book, error) {
	shown, err := s.repo.Get(ctx, id)
	if err != nil {
		return Book{}, err
	}
	// The old key's identity is what the catalogue said, not the edits.
	before := shown.Matched()
	d, err := s.meta.GetBook(ctx, olKey)
	if err != nil {
		return Book{}, fmt.Errorf("fetch metadata: %w", err)
	}
	authorKey := d.AuthorKey
	if d.Author == "" {
		authorKey = fallback.AuthorKey
	}
	next := Book{
		OLKey:       orStr(d.Key, olKey),
		Title:       orStr(d.Title, fallback.Title),
		Author:      orStr(d.Author, fallback.Author),
		Year:        d.Year,
		CoverURL:    orStr(d.CoverURL, fallback.CoverURL),
		Description: d.Description,
		Subjects:    d.Subjects,
		ReleaseDate: d.ReleaseDate,
	}
	if next.Year == 0 {
		next.Year = fallback.Year
	}
	if next.Title == "" {
		return Book{}, fmt.Errorf("the chosen work has no title")
	}
	// The old key stays one of the book's keys when the new work is the same book (the
	// usual case: moving it from one catalogue to another). When it is a different book
	// the old key was a wrong identification, and keeping it would make that other book
	// read as owned — and refuse to be added.
	keepOld := SameBook(before.Title, before.Author, next.Title, next.Author)
	if next.ReleaseDate == "" && keepOld {
		next.ReleaseDate = before.ReleaseDate // the same book; this catalogue just doesn't say
	}
	if err := s.repo.Rematch(ctx, id, next, keepOld, clearEdits); err != nil {
		return Book{}, err
	}
	// An author edited by hand (and kept) decides the author link; otherwise the new
	// work's author does.
	if clearEdits || !shown.Edited(FieldAuthor) {
		relink := !strings.EqualFold(before.Author, next.Author) || shown.Edited(FieldAuthor)
		s.linkAuthor(ctx, id, next.Author, authorKey, relink)
	}
	s.repo.AddEvent(ctx, id, "matched", fmt.Sprintf("Re-matched from %q to %q (%s)", before.Title, next.Title, next.OLKey))
	if len(shown.Edits) > 0 {
		if clearEdits {
			s.repo.AddEvent(ctx, id, "edited", "Details edited by hand were cleared with the re-match")
		} else {
			s.repo.AddEvent(ctx, id, "edited", "Details edited by hand were kept through the re-match")
		}
	}
	s.log.Info("book re-matched", "id", id, "from", before.Title, "to", next.Title, "ol_key", next.OLKey)
	return s.repo.Get(ctx, id)
}

// Delete removes a book.
func (s *Service) Delete(ctx context.Context, id int64) error { return s.repo.Delete(ctx, id) }

// AddEvent appends a timeline event for a book.
func (s *Service) AddEvent(ctx context.Context, id int64, event, detail string) {
	s.repo.AddEvent(ctx, id, event, detail)
}

// Events returns a book's activity timeline, newest first.
func (s *Service) Events(ctx context.Context, id int64, limit int) ([]Event, error) {
	return s.repo.Events(ctx, id, limit)
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// NormKey folds the title (parser.FoldTitle: accents, case, "&" as "and") and keeps only
// ASCII letters and digits — for tolerant title identity, so "García Márquez" and
// "Garcia Marquez" agree.
func NormKey(str string) string {
	var b []byte
	for _, r := range parser.FoldTitle(str) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b = append(b, byte(r))
		}
	}
	return string(b)
}
