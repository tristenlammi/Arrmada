package books

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Authors.
//
// An author is a row of its own (authors), and a book points at it (books.author_id).
// books.author stays the display string and what indexer searches carry; author_id says
// which person that string means. The link is made by the catalogue's id for the person
// first (catalogue_key: Open Library "OL23919A", Hardcover "hc:a:123") and by name only
// when no key decides it:
//
//   - two spellings of one name are one author: "J.K. Rowling", "J. K. Rowling" and
//     "Rowling, J.K." read the same (authorKey), and their initials agree;
//   - two names whose initials disagree are two people ("J. Smith" is not "K. Smith");
//   - two authors whose keys differ in the same catalogue are two people, whatever their
//     names (keys from different catalogues can't be compared, so they don't split);
//   - a name that could be more than one existing author is left unlinked. A wrong link
//     would put one person's books on another's page; an unlinked book still shows under
//     its name.

// ErrAuthorNotFound is returned for an author id that doesn't exist.
var ErrAuthorNotFound = errors.New("author not found")

// ErrBadAuthorKey is returned for a catalogue key that is no author key Arrmada knows.
var ErrBadAuthorKey = errors.New("not a catalogue author key")

// Author is one person books are credited to.
type Author struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	CatalogueKey string `json:"catalogue_key"`
	ImageURL     string `json:"image_url,omitempty"`
	Bio          string `json:"bio,omitempty"`
	// Monitored / MonitorNew / QualityProfile are for following an author: new books by a
	// monitored author with MonitorNew set are added on a schedule, with QualityProfile
	// (the default book profile when empty). WorksCheckedAt is when that last ran.
	Monitored      bool   `json:"monitored"`
	MonitorNew     bool   `json:"monitor_new"`
	QualityProfile string `json:"quality_profile"`
	AddedAt        string `json:"added_at,omitempty"`
	CheckedAt      string `json:"checked_at,omitempty"`
	WorksCheckedAt string `json:"works_checked_at,omitempty"`
	// NewBooksFound is how many books the release check has added for this author.
	NewBooksFound int `json:"new_books_found"`
	// worksSeeded: the release check has had its first look (authorreleases.go).
	worksSeeded bool
	// Books is how many library books are linked to the author; Owned how many of those
	// have a file; Wanted how many are monitored and have none yet.
	Books  int `json:"books"`
	Owned  int `json:"owned"`
	Wanted int `json:"wanted"`
	// Names are the spellings on the author's books, most used first.
	Names []string `json:"names,omitempty"`
}

var (
	olAuthorKeyRe = regexp.MustCompile(`^OL\d+A$`)
	hcAuthorKeyRe = regexp.MustCompile(`^hc:a:\d+$`)
)

// ValidAuthorKey reports whether key is a catalogue author key: Open Library "OL…A" or
// Hardcover "hc:a:<id>".
func ValidAuthorKey(key string) bool {
	return olAuthorKeyRe.MatchString(key) || hcAuthorKeyRe.MatchString(key)
}

// authorCatalogue names the catalogue an author key is from ("" for none).
func authorCatalogue(key string) string {
	switch {
	case hcAuthorKeyRe.MatchString(key):
		return "hardcover"
	case olAuthorKeyRe.MatchString(key):
		return "openlibrary"
	}
	return ""
}

// keysConflict: two keys from the same catalogue that differ name two people. Keys from
// different catalogues say nothing about each other.
func keysConflict(a, b string) bool {
	return a != "" && b != "" && a != b && authorCatalogue(a) == authorCatalogue(b)
}

// authorName is a name read for comparison: its words (authorKey) and its initials in
// the order written ("J.K. Rowling" → "rowling" + "jk").
type authorName struct{ words, initials string }

func authorNameOf(name string) authorName {
	var initials strings.Builder
	for _, w := range strings.Fields(wordKey(name)) {
		if len(w) == 1 {
			initials.WriteString(w)
		}
	}
	return authorName{words: authorKey(name), initials: initials.String()}
}

// sameAs: the same words, and initials that don't disagree — one set may be missing or
// shorter ("Rowling" and "J. Rowling" can be "J.K. Rowling"), but "J." is not "K.".
func (a authorName) sameAs(b authorName) bool {
	if a.words == "" || a.words != b.words {
		return false
	}
	return strings.HasPrefix(a.initials, b.initials) || strings.HasPrefix(b.initials, a.initials)
}

// SameAuthorName reports whether two author strings can name the same person.
func SameAuthorName(a, b string) bool { return authorNameOf(a).sameAs(authorNameOf(b)) }

// --- repo ---

const authorCols = `a.id, a.name, a.catalogue_key, a.image_url, a.bio, a.monitored, a.monitor_new,
	a.quality_profile, a.added_at, a.checked_at, a.works_checked_at, a.new_books_found, a.works_seeded`

func scanAuthor(row interface{ Scan(...any) error }, extra ...any) (Author, error) {
	var a Author
	var mon, monNew, seeded int
	dest := append([]any{&a.ID, &a.Name, &a.CatalogueKey, &a.ImageURL, &a.Bio, &mon, &monNew,
		&a.QualityProfile, &a.AddedAt, &a.CheckedAt, &a.WorksCheckedAt, &a.NewBooksFound, &seeded}, extra...)
	if err := row.Scan(dest...); err != nil {
		return Author{}, err
	}
	a.Monitored, a.MonitorNew, a.worksSeeded = mon != 0, monNew != 0, seeded != 0
	return a, nil
}

// authorCountsSQL is the per-author book counts, joined onto authors as `a`.
const authorCountsSQL = `COUNT(b.id), COALESCE(SUM(CASE WHEN b.has_file = 1 THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN b.monitored = 1 AND b.has_file = 0 THEN 1 ELSE 0 END), 0)`

// listAuthors returns every author with their counts and spellings. An author with no
// books is left out unless monitored: a book re-matched to someone else leaves its old
// author behind, and nobody needs to see that.
func (r *Repo) listAuthors(ctx context.Context, ex store.Execer) ([]Author, error) {
	rows, err := ex.QueryContext(ctx, `SELECT `+authorCols+`, `+authorCountsSQL+`
		FROM authors a LEFT JOIN books b ON b.author_id = a.id
		GROUP BY a.id HAVING COUNT(b.id) > 0 OR a.monitored = 1
		ORDER BY `+store.FoldASCII(ex, "a.name")+`, a.id`)
	if err != nil {
		return nil, err
	}
	var out []Author
	for rows.Next() {
		var a Author
		var books, owned, wanted int
		if a, err = scanAuthor(rows, &books, &owned, &wanted); err != nil {
			rows.Close()
			return nil, err
		}
		a.Books, a.Owned, a.Wanted = books, owned, wanted
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names, err := r.authorSpellings(ctx, ex)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Names = names[out[i].ID]
	}
	return out, nil
}

// allAuthors is every author row with its spellings, for name matching (no counts).
func (r *Repo) allAuthors(ctx context.Context, ex store.Execer) ([]Author, error) {
	rows, err := ex.QueryContext(ctx, `SELECT `+authorCols+` FROM authors a ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	var out []Author
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names, err := r.authorSpellings(ctx, ex)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Names = names[out[i].ID]
	}
	return out, nil
}

// authorSpellings maps author id → the author strings on its books, most used first.
func (r *Repo) authorSpellings(ctx context.Context, ex store.Execer) (map[int64][]string, error) {
	rows, err := ex.QueryContext(ctx, `SELECT author_id, author, COUNT(*) AS n FROM books
		WHERE author_id IS NOT NULL AND TRIM(author) != ''
		GROUP BY author_id, author ORDER BY author_id, n DESC, author`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		var name string
		var n int
		if err := rows.Scan(&id, &name, &n); err != nil {
			return nil, err
		}
		out[id] = append(out[id], name)
	}
	return out, rows.Err()
}

// getAuthor is one author row, without counts.
func (r *Repo) getAuthor(ctx context.Context, ex store.Execer, id int64) (Author, error) {
	a, err := scanAuthor(ex.QueryRowContext(ctx, `SELECT `+authorCols+` FROM authors a WHERE a.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Author{}, ErrAuthorNotFound
	}
	return a, err
}

// getAuthorCounted is getAuthor with the counts and spellings filled.
func (r *Repo) getAuthorCounted(ctx context.Context, id int64) (Author, error) {
	var books, owned, wanted int
	a, err := scanAuthor(r.db.QueryRowContext(ctx, `SELECT `+authorCols+`, `+authorCountsSQL+`
		FROM authors a LEFT JOIN books b ON b.author_id = a.id WHERE a.id = ? GROUP BY a.id`, id),
		&books, &owned, &wanted)
	if errors.Is(err, sql.ErrNoRows) {
		return Author{}, ErrAuthorNotFound
	}
	if err != nil {
		return Author{}, err
	}
	a.Books, a.Owned, a.Wanted = books, owned, wanted
	rows, err := r.db.QueryContext(ctx, `SELECT author, COUNT(*) AS n FROM books
		WHERE author_id = ? AND TRIM(author) != '' GROUP BY author ORDER BY n DESC, author`, id)
	if err != nil {
		return Author{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return Author{}, err
		}
		a.Names = append(a.Names, name)
	}
	return a, rows.Err()
}

func authorIDByKey(ctx context.Context, ex store.Execer, key string) (int64, bool) {
	if key == "" {
		return 0, false
	}
	var id int64
	if err := ex.QueryRowContext(ctx, `SELECT id FROM authors WHERE catalogue_key = ?`, key).Scan(&id); err != nil {
		return 0, false
	}
	return id, true
}

func createAuthor(ctx context.Context, ex store.Execer, name, key string) (int64, error) {
	return store.InsertID(ctx, ex, `INSERT INTO authors (name, catalogue_key) VALUES (?, ?)`, name, key)
}

// namedLike returns the authors whose name, or a spelling on one of their books, can be
// the same person as name.
func namedLike(all []Author, name string) []Author {
	want := authorNameOf(name)
	if want.words == "" {
		return nil
	}
	var out []Author
	for _, a := range all {
		match := authorNameOf(a.Name).sameAs(want)
		for _, n := range a.Names {
			if match {
				break
			}
			match = authorNameOf(n).sameAs(want)
		}
		if match {
			out = append(out, a)
		}
	}
	return out
}

// resolveAuthor finds, or creates, the author a name and catalogue key mean, inside the
// caller's transaction (the pool begins IMMEDIATE, so two adds of one new author can't
// both create it). It returns 0 when the name could be more than one author and no key
// decides it: that book stays unlinked rather than guessed.
func (r *Repo) resolveAuthor(ctx context.Context, tx *store.Transaction, name, key string) (int64, error) {
	name, key = strings.TrimSpace(name), strings.TrimSpace(key)
	if !ValidAuthorKey(key) {
		key = ""
	}
	if key != "" {
		if id, ok := authorIDByKey(ctx, tx, key); ok {
			return id, nil
		}
	}
	if authorNameOf(name).words == "" {
		return 0, nil // no name to file it under (blank, or punctuation only)
	}
	all, err := r.allAuthors(ctx, tx)
	if err != nil {
		return 0, err
	}
	var cands []Author
	for _, a := range namedLike(all, name) {
		if !keysConflict(a.CatalogueKey, key) {
			cands = append(cands, a)
		}
	}
	switch {
	case len(cands) == 1:
		a := cands[0]
		// The catalogue's own id for this book's author: the author takes it, unless
		// they already have one from that catalogue.
		if key != "" && a.CatalogueKey == "" {
			if _, err := tx.ExecContext(ctx, `UPDATE authors SET catalogue_key = ? WHERE id = ?`, key, a.ID); err != nil {
				return 0, err
			}
		}
		return a.ID, nil
	case len(cands) > 1 && key == "":
		return 0, nil // more than one person by that name, nothing to tell them apart
	}
	// Nobody by that name yet — or a key that none of the namesakes can be (it differs
	// from theirs, or several of them could hold it): a new author under that key.
	return createAuthor(ctx, tx, name, key)
}

// bookAuthorID returns a book's author_id (0 for none).
func bookAuthorID(ctx context.Context, ex store.Execer, bookID int64) (int64, error) {
	var id sql.NullInt64
	if err := ex.QueryRowContext(ctx, `SELECT author_id FROM books WHERE id = ?`, bookID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return id.Int64, nil
}

func setBookAuthor(ctx context.Context, ex store.Execer, bookID, authorID int64) error {
	var v any
	if authorID > 0 {
		v = authorID
	}
	_, err := ex.ExecContext(ctx, `UPDATE books SET author_id = ? WHERE id = ?`, v, bookID)
	return err
}

// linkBook points a book at its author. relink says the book's author string changed,
// so whatever it was linked to no longer holds; otherwise a linked book keeps its link,
// and only an author without a key from that catalogue learns this one.
func (r *Repo) linkBook(ctx context.Context, bookID int64, name, key string, relink bool) (int64, error) {
	var linked int64
	err := store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		cur, err := bookAuthorID(ctx, tx, bookID)
		if err != nil {
			return err
		}
		if cur > 0 && !relink {
			linked = cur
			return r.adoptKey(ctx, tx, cur, name, key)
		}
		if cur > 0 {
			// Unlink first: the book's new spelling would otherwise count as one of its
			// old author's names and lead straight back to them.
			if err := setBookAuthor(ctx, tx, bookID, 0); err != nil {
				return err
			}
			cur = 0
		}
		id, err := r.resolveAuthor(ctx, tx, name, key)
		if err != nil {
			return err
		}
		linked = id
		if id == cur {
			return nil
		}
		return setBookAuthor(ctx, tx, bookID, id)
	})
	return linked, err
}

// adoptKey gives a keyless author the catalogue's id for them, learned from one of their
// books — when the name agrees and no other author holds that key already.
func (r *Repo) adoptKey(ctx context.Context, tx *store.Transaction, authorID int64, name, key string) error {
	if !ValidAuthorKey(key) {
		return nil
	}
	if _, taken := authorIDByKey(ctx, tx, key); taken {
		return nil
	}
	a, err := r.getAuthor(ctx, tx, authorID)
	if err != nil {
		return err
	}
	if a.CatalogueKey != "" || !SameAuthorName(a.Name, name) {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE authors SET catalogue_key = ? WHERE id = ?`, key, authorID)
	return err
}

// --- service ---

// ListAuthors returns the library's authors with owned and wanted counts.
func (s *Service) ListAuthors(ctx context.Context) ([]Author, error) {
	return s.repo.listAuthors(ctx, s.repo.db)
}

// GetAuthor returns one author with counts and spellings.
func (s *Service) GetAuthor(ctx context.Context, id int64) (Author, error) {
	return s.repo.getAuthorCounted(ctx, id)
}

// AuthorsNamed returns the authors a name can mean — what an old /books/author/<name>
// link resolves through: one is that author, several need a person to choose.
func (s *Service) AuthorsNamed(ctx context.Context, name string) ([]Author, error) {
	all, err := s.repo.listAuthors(ctx, s.repo.db)
	if err != nil {
		return nil, err
	}
	return namedLike(all, name), nil
}

// UpsertByKey returns the author with this catalogue key, creating one (or giving the
// key to the one keyless author of that name) when nobody has it.
func (s *Service) UpsertByKey(ctx context.Context, key, name string) (int64, error) {
	if !ValidAuthorKey(key) {
		return 0, ErrBadAuthorKey
	}
	name = strings.TrimSpace(name)
	var id int64
	err := store.WithTx(ctx, s.repo.db, func(tx *store.Transaction) error {
		var err error
		if id, err = s.repo.resolveAuthor(ctx, tx, name, key); err == nil && id == 0 {
			// No usable name to file the key under: the key is the name until the
			// catalogue's detail fills it in.
			id, err = createAuthor(ctx, tx, orStr(name, key), key)
		}
		return err
	})
	return id, err
}

// UpsertByName returns the author a name means, creating one when nobody has it. 0 (no
// error) means the name could be more than one author.
func (s *Service) UpsertByName(ctx context.Context, name string) (int64, error) {
	var id int64
	err := store.WithTx(ctx, s.repo.db, func(tx *store.Transaction) error {
		var err error
		id, err = s.repo.resolveAuthor(ctx, tx, name, "")
		return err
	})
	return id, err
}

// SetMonitoring sets whether an author is followed, whether their new books are added,
// and the profile those books get. Starting to add an author's new books (again) makes
// the release check's next look a first look: what the catalogue lists by then is
// recorded, not added, so books that came out while nobody was following aren't swept in.
func (s *Service) SetMonitoring(ctx context.Context, id int64, monitored, monitorNew bool, profile string) error {
	// SET expressions read the row as it was, so "not followed before" is the old flags.
	restart := `?1 = 1 AND ?2 = 1 AND NOT (monitored = 1 AND monitor_new = 1)`
	res, err := s.repo.db.ExecContext(ctx,
		`UPDATE authors SET monitored = ?1, monitor_new = ?2, quality_profile = ?3,
			works_seeded = CASE WHEN `+restart+` THEN 0 ELSE works_seeded END,
			works_checked_at = CASE WHEN `+restart+` THEN '' ELSE works_checked_at END
		 WHERE id = ?4`,
		b2i(monitored), b2i(monitorNew), strings.TrimSpace(profile), id)
	err = affected(res, err)
	if errors.Is(err, ErrNotFound) {
		return ErrAuthorNotFound
	}
	return err
}

// SetCatalogueKey records which catalogue author this is — the "Which author is this?"
// pick. When another author row already has that key, the two rows are one person: this
// one's books move to it and this row goes, and the returned id is the one that stays.
// An empty key clears the link. The photo and biography are filled from the catalogue
// when it has them (best effort, after the write).
func (s *Service) SetCatalogueKey(ctx context.Context, id int64, key string) (int64, error) {
	key = strings.TrimSpace(key)
	if key != "" && !ValidAuthorKey(key) {
		return 0, ErrBadAuthorKey
	}
	keep := id
	merged := 0
	err := store.WithTx(ctx, s.repo.db, func(tx *store.Transaction) error {
		cur, err := s.repo.getAuthor(ctx, tx, id)
		if err != nil {
			return err
		}
		if other, taken := authorIDByKey(ctx, tx, key); taken && other != id {
			keep = other
			res, err := tx.ExecContext(ctx, `UPDATE books SET author_id = ? WHERE author_id = ?`, other, id)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			merged = int(n)
			// The release check's record of this row's works goes with its books (the
			// delete below would cascade it away), and the next check of the row that
			// stays is a first look: its record now mixes two rows' catalogues.
			if _, err := tx.ExecContext(ctx, `INSERT INTO author_works_seen (author_id, work_key, first_seen)
				SELECT ?, work_key, first_seen FROM author_works_seen WHERE author_id = ? ON CONFLICT DO NOTHING`, other, id); err != nil {
				return err
			}
			// Following either row means following the person.
			if _, err := tx.ExecContext(ctx, `UPDATE authors SET
				monitored = `+store.ScalarMax(tx, "monitored", "?")+`, monitor_new = `+store.ScalarMax(tx, "monitor_new", "?")+`,
				quality_profile = CASE WHEN quality_profile = '' THEN ? ELSE quality_profile END,
				new_books_found = new_books_found + ?, works_seeded = 0, works_checked_at = ''
				WHERE id = ?`, b2i(cur.Monitored), b2i(cur.MonitorNew), cur.QualityProfile, cur.NewBooksFound, other); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM authors WHERE id = ?`, id)
			return err
		}
		// Another catalogue entry is another list of works: the release check's next look
		// at it is a first look, so the new list's keys don't all read as new books.
		_, err = tx.ExecContext(ctx, `UPDATE authors SET catalogue_key = ?1, checked_at = '',
			works_seeded = CASE WHEN catalogue_key = ?1 THEN works_seeded ELSE 0 END,
			works_checked_at = CASE WHEN catalogue_key = ?1 THEN works_checked_at ELSE '' END WHERE id = ?2`, key, id)
		return err
	})
	if err != nil {
		return 0, err
	}
	if keep != id {
		s.log.Info("books: two author rows were one person", "kept", keep, "removed", id, "books_moved", merged)
	}
	if key != "" {
		s.fillAuthorDetail(ctx, keep, key)
	}
	return keep, nil
}

// fillAuthorDetail stores the catalogue's name, photo and biography for an author whose
// key a person just chose. Nothing is lost on failure: the page asks the catalogue itself.
func (s *Service) fillAuthorDetail(ctx context.Context, id int64, key string) {
	ex, ok := s.extras()
	if !ok {
		return
	}
	d, err := ex.AuthorDetail(ctx, key)
	if err != nil || d == nil {
		if err != nil && !errors.Is(err, metadata.ErrNotSupported) {
			s.log.Debug("books: author detail lookup failed", "author_key", key, "err", err)
		}
		return
	}
	_, _ = s.repo.db.ExecContext(ctx, `UPDATE authors SET
		name = CASE WHEN ? != '' THEN ? ELSE name END, image_url = ?, bio = ?, checked_at = ?
		WHERE id = ? AND catalogue_key = ?`,
		strings.TrimSpace(d.Name), strings.TrimSpace(d.Name), d.ImageURL, d.Bio,
		time.Now().UTC().Format("2006-01-02 15:04:05"), id, key)
}

// linkAuthor links a book to its author, logging (ids only) rather than failing the
// operation it is part of: an unlinked book still shows under its name.
func (s *Service) linkAuthor(ctx context.Context, bookID int64, name, key string, relink bool) int64 {
	id, err := s.repo.linkBook(ctx, bookID, name, key, relink)
	if err != nil {
		s.log.Warn("books: could not link a book to its author", "book_id", bookID, "err", err)
		return 0
	}
	return id
}

// BackfillAuthors links every book that has an author string but no author row,
// creating authors as it goes. It is idempotent: linked books are left alone, and a
// name that could be more than one author stays unlinked each run. Names with the most
// initials go first, so "J.K. Rowling" exists before a bare "Rowling" is placed; what
// the old book_authors cache knew (catalogue key, photo, biography) comes along, and two
// names that cache gave different keys are never joined. Logs counts only.
func (s *Service) BackfillAuthors(ctx context.Context) (linked, unlinked int, err error) {
	type group struct {
		name  string
		books int
		n     authorName
	}
	rows, err := s.repo.db.QueryContext(ctx, `SELECT author, COUNT(*) FROM books
		WHERE author_id IS NULL AND TRIM(author) != '' GROUP BY author`)
	if err != nil {
		return 0, 0, err
	}
	var groups []group
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.name, &g.books); err != nil {
			rows.Close()
			return 0, 0, err
		}
		g.n = authorNameOf(g.name)
		groups = append(groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if len(groups) == 0 {
		return 0, 0, nil
	}
	cached, err := s.repo.authorImages(ctx)
	if err != nil {
		cached = map[string]authorRecord{}
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if len(a.n.initials) != len(b.n.initials) {
			return len(a.n.initials) > len(b.n.initials)
		}
		if a.books != b.books {
			return a.books > b.books
		}
		return a.name < b.name
	})
	for _, g := range groups {
		if ctx.Err() != nil {
			return linked, unlinked, ctx.Err()
		}
		rec := cached[g.name]
		key := rec.Key
		if !ValidAuthorKey(key) {
			key = ""
		}
		err := store.WithTx(ctx, s.repo.db, func(tx *store.Transaction) error {
			id, err := s.repo.resolveAuthor(ctx, tx, g.name, key)
			if err != nil || id == 0 {
				if id == 0 {
					unlinked += g.books
				}
				return err
			}
			res, err := tx.ExecContext(ctx, `UPDATE books SET author_id = ? WHERE author_id IS NULL AND author = ?`, id, g.name)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			linked += int(n)
			if rec.ImageURL != "" || rec.Bio != "" {
				_, err = tx.ExecContext(ctx, `UPDATE authors SET image_url = ?, bio = ?
					WHERE id = ? AND image_url = '' AND bio = '' AND (catalogue_key = '' OR catalogue_key = ?)`,
					rec.ImageURL, rec.Bio, id, key)
			}
			return err
		})
		if err != nil {
			return linked, unlinked, err
		}
	}
	s.log.Info("books: linked books to authors", "books_linked", linked, "books_left_unlinked", unlinked)
	return linked, unlinked, nil
}

// overlayAuthorRows puts the photos stored on author rows over the book_authors cache,
// under every spelling on the author's books: a photo a person's pick brought in shows on
// their tile (and in the listening apps) without another lookup. A nil map is left alone.
func (r *Repo) overlayAuthorRows(ctx context.Context, known map[string]authorRecord) {
	if known == nil {
		return
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT b.author, a.catalogue_key, a.image_url, a.bio
		FROM books b JOIN authors a ON a.id = b.author_id
		WHERE a.image_url != '' AND TRIM(b.author) != ''`)
	if err != nil {
		return
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var name string
		var rec authorRecord
		if rows.Scan(&name, &rec.Key, &rec.ImageURL, &rec.Bio) != nil {
			return
		}
		rec.CheckedAt = now
		known[name] = rec
	}
}
