package books

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Duplicate handling.
//
// The library used to be unique only on the catalogue key. Open Library files the same
// novel under several "works", the Google Books fallback issues its own keys, and the
// disk scan matches by whatever key the lookup returned — so one book ended up in the
// library three times. A book is now also identified by what it is: its whole title
// (edition and series notes stripped) plus the author's surname-ish words.
//
// The title is never cut at the first colon. That used to make 'Thrawn' and 'Thrawn:
// Alliances' one book, so the second could not be added, requested, or found missing
// from a series.

// Identity is what a book is, read from its title and author. Full is the whole title,
// normalised; Sub is the subtitle alone when it is distinctive enough to be another
// catalogue's whole title ("Mistborn: The Final Empire" is listed as "The Final Empire").
type Identity struct {
	Full, Sub string
	Author    string
	NoAuthor  bool
}

// titleSeps split a main title from its subtitle, in the forms catalogues use.
var titleSeps = []string{":", " - ", " — ", " – "}

// editionNoteRe is a subtitle that describes the edition, not the book: "Dune: Deluxe
// Edition" and "Gone Girl: A Novel" are those books.
var editionNoteRe = regexp.MustCompile(`\b(edition|anniversary|illustrated|deluxe|unabridged|abridged|annotated|collector'?s|special|revised|expanded|a novel|a thriller|a mystery|a memoir|a novella)\b`)

// seriesNoteRe is a subtitle that places the book in its series: "Book One of the Dune
// Chronicles", "The Stormlight Archive, Book 1", "Stormlight Archive #1". A bare
// "Book 2" or "Vol. 3" is NOT a note — it is the volume, and "Saga: Volume 1" and
// "Saga: Volume 2" are two books — so it stays in the title as its number.
var seriesNoteRe = regexp.MustCompile(`^(book|volume|vol\.?|part|bk\.?)\s+(\d+|one|two|three|four|five|six|seven|eight|nine|ten)\s+of\b|\S.*#\s*\d+$|\S.*,\s*(book|volume|vol\.?)\s*\d+$`)

// IdentityOf reads a book's identity from its title and author.
func IdentityOf(title, author string) Identity {
	id := Identity{Author: authorKey(author), NoAuthor: strings.TrimSpace(author) == ""}
	t := strings.ToLower(strings.TrimSpace(title))
	// Peel notes off the end until none is left: "(Dune Chronicles, #1)", "[Illustrated]",
	// ": Deluxe Edition", ": Book One of the Dune Chronicles". Working from the end means
	// "Mistborn: The Final Empire: 10th Anniversary Edition" keeps "The Final Empire".
	for {
		before := t
		t = stripTrailingNote(t)
		if i, sep := lastSep(t); i > 0 {
			if tail := strings.TrimSpace(t[i+len(sep):]); editionNoteRe.MatchString(tail) || seriesNoteRe.MatchString(tail) {
				t = strings.TrimSpace(t[:i])
			}
		}
		if t == before {
			break
		}
	}
	main, sub := t, ""
	if i, sep := firstSep(t); i > 0 {
		main, sub = t[:i], strings.TrimSpace(t[i+len(sep):])
	}
	if sub == "" {
		id.Full = titlePartKey(main)
		return id
	}
	id.Full = titlePartKey(main + " " + sub)
	// Only a subtitle that could be a title on its own: never a bare volume number.
	if s := titlePartKey(sub); len(s) >= 4 && strings.IndexFunc(s, unicode.IsLetter) >= 0 {
		id.Sub = s
	}
	return id
}

// stripTrailingNote drops one trailing "(…)" or "[…]", unless it is the whole title.
func stripTrailingNote(t string) string {
	for _, p := range [][2]string{{"(", ")"}, {"[", "]"}} {
		if strings.HasSuffix(t, p[1]) {
			if i := strings.LastIndex(t, p[0]); i > 0 {
				return strings.TrimSpace(t[:i])
			}
		}
	}
	return t
}

func firstSep(t string) (int, string) {
	at, which := -1, ""
	for _, sep := range titleSeps {
		if i := strings.Index(t, sep); i > 0 && (at < 0 || i < at) {
			at, which = i, sep
		}
	}
	return at, which
}

func lastSep(t string) (int, string) {
	at, which := -1, ""
	for _, sep := range titleSeps {
		if i := strings.LastIndex(t, sep); i > 0 && i > at {
			at, which = i, sep
		}
	}
	return at, which
}

// SameTitle reports whether two identities name the same title: the same whole title,
// or one's subtitle is the other's whole title ("The Final Empire" / "Mistborn: The
// Final Empire"). A subtitle is never compared with a subtitle, so 'Heir to the Empire:
// Star Wars' and 'Dark Force Rising: Star Wars' stay two books; a main title alone is
// never compared, so 'Thrawn' is not 'Thrawn: Alliances'.
func (a Identity) SameTitle(b Identity) bool {
	if a.Full == "" || b.Full == "" {
		return false
	}
	return a.Full == b.Full || (a.Sub != "" && a.Sub == b.Full) || (b.Sub != "" && b.Sub == a.Full)
}

// sameAuthor: the same author words, and an author on both sides or on neither —
// "Beloved" by nobody is not "Beloved" by Toni Morrison.
func (a Identity) sameAuthor(b Identity) bool {
	return a.Author == b.Author && a.NoAuthor == b.NoAuthor
}

// SameBook reports whether two title/author pairs are the same book.
func SameBook(aTitle, aAuthor, bTitle, bAuthor string) bool {
	a, b := IdentityOf(aTitle, aAuthor), IdentityOf(bTitle, bAuthor)
	return a.SameTitle(b) && a.sameAuthor(b)
}

// DedupeKey is a book's whole-title-and-author key, for logs and tests. Matching goes
// through SameBook or an IdentityIndex, which also read a subtitle as a title.
func DedupeKey(title, author string) string {
	id := IdentityOf(title, author)
	if id.Full == "" {
		return ""
	}
	return id.Full + "|" + id.Author
}

// IdentityIndex finds the library row that is the same book as a title and author,
// without a pass over the library per lookup.
type IdentityIndex struct {
	byFull map[string][]Book
	bySub  map[string][]Book
	ids    map[int64]Identity
}

// NewIdentityIndex indexes a list of library rows.
func NewIdentityIndex(list []Book) *IdentityIndex {
	x := &IdentityIndex{byFull: map[string][]Book{}, bySub: map[string][]Book{}, ids: map[int64]Identity{}}
	for _, b := range list {
		x.Add(b)
	}
	return x
}

// Add indexes one more row — a book created mid-pass, so a catalogue that lists the
// same novel twice still adds it once.
func (x *IdentityIndex) Add(b Book) {
	id := IdentityOf(b.Title, b.Author)
	if id.Full == "" {
		return
	}
	x.ids[b.ID] = id
	x.byFull[id.Full+"|"+id.Author] = append(x.byFull[id.Full+"|"+id.Author], b)
	if id.Sub != "" {
		x.bySub[id.Sub+"|"+id.Author] = append(x.bySub[id.Sub+"|"+id.Author], b)
	}
}

// Find returns the library row that is the same book. The whole title is tried first,
// then this title's subtitle as a library title, then this title as a library
// subtitle. Among equal matches a row with files wins, then the oldest.
func (x *IdentityIndex) Find(title, author string) (Book, bool) {
	q := IdentityOf(title, author)
	if q.Full == "" {
		return Book{}, false
	}
	probes := [][]Book{x.byFull[q.Full+"|"+q.Author]}
	if q.Sub != "" {
		probes = append(probes, x.byFull[q.Sub+"|"+q.Author])
	}
	probes = append(probes, x.bySub[q.Full+"|"+q.Author])
	for _, rows := range probes {
		var best Book
		found := false
		for _, b := range rows {
			if !x.ids[b.ID].sameAuthor(q) {
				continue
			}
			if !found || (b.HasFile && !best.HasFile) || (b.HasFile == best.HasFile && b.ID < best.ID) {
				best, found = b, true
			}
		}
		if found {
			return best, true
		}
	}
	return Book{}, false
}

// FindAll returns every library row that is the same book — for callers that must know
// whether a match is unique before acting on it.
func (x *IdentityIndex) FindAll(title, author string) []Book {
	q := IdentityOf(title, author)
	if q.Full == "" {
		return nil
	}
	seen := map[int64]bool{}
	var out []Book
	add := func(rows []Book) {
		for _, b := range rows {
			if !seen[b.ID] && x.ids[b.ID].sameAuthor(q) {
				seen[b.ID] = true
				out = append(out, b)
			}
		}
	}
	add(x.byFull[q.Full+"|"+q.Author])
	if q.Sub != "" {
		add(x.byFull[q.Sub+"|"+q.Author])
	}
	add(x.bySub[q.Full+"|"+q.Author])
	return out
}

// volumeRe is the marker before a volume number that catalogues render every way:
// "White Sand, Vol. 1", "White Sand Volume 1", "White Sand #1", "White Sand 1".
var volumeRe = regexp.MustCompile(`\b(?:vol\.?|volume|no\.?|number|bk\.?|book)\s*(\d+)\b|#\s*(\d+)\b`)

// titlePartKey normalises one part of a title (the main title or a subtitle): the
// leading article goes, "&" reads as "and", a volume marker keeps only its number.
func titlePartKey(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	for _, art := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(t, art) && len(t) > len(art) {
			t = t[len(art):]
			break
		}
	}
	t = strings.ReplaceAll(t, "&", " and ")
	t = volumeRe.ReplaceAllString(t, "$1$2")
	return NormKey(t)
}

// authorKey keeps the author's words of two or more letters, sorted — so "J. K.
// Rowling", "Rowling, J.K." and "J.K. Rowling" agree, as do "George R. R. Martin" and
// "Martin, George R.R.".
func authorKey(author string) string {
	words := strings.Fields(wordKey(author))
	var keep []string
	for _, w := range words {
		if len(w) >= 2 {
			keep = append(keep, w)
		}
	}
	if len(keep) == 0 {
		keep = words
	}
	sort.Strings(keep)
	return strings.Join(keep, " ")
}

// findDuplicate returns the library book that is the same title and author, if any.
// An author is required on both sides unless both are blank: "Beloved" by nobody is
// not the same as "Beloved" by Toni Morrison.
func (s *Service) findDuplicate(ctx context.Context, title, author string) (Book, bool) {
	list, err := s.repo.List(ctx)
	if err != nil {
		return Book{}, false
	}
	return NewIdentityIndex(list).Find(title, author)
}

// findByKey returns the library book with this catalogue key, if any.
func (s *Service) findByKey(ctx context.Context, key string) (Book, bool) {
	list, err := s.repo.List(ctx)
	if err != nil {
		return Book{}, false
	}
	for _, b := range list {
		if b.OLKey == key {
			return b, true
		}
	}
	return Book{}, false
}

// Two rows that look like one book are never merged automatically. The old fold keyed
// on a title cut at the first colon, so same-author prefix siblings were "duplicates",
// and deleting the losing row took its audio versions and everyone's listening place
// with it. A merge is a decision for a person; the Hardcover upgrade flags the pair.
