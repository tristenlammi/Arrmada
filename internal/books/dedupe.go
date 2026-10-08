package books

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// Duplicate handling.
//
// The library used to be unique only on the catalogue key. Open Library files the same
// novel under several "works", the Google Books fallback issues its own keys, and the
// disk scan matches by whatever key the lookup returned — so one book ended up in the
// library three times. A book is now also identified by what it is: a normalised
// title (subtitle and edition notes stripped) plus the author's surname-ish words.

// DedupeKey identifies a book by title and author, tolerant of the differences between
// catalogues: punctuation, case, a leading article, a subtitle after a colon, an
// edition note in brackets, initials vs full first names.
func DedupeKey(title, author string) string {
	t := titleKey(title)
	if t == "" {
		return ""
	}
	return t + "|" + authorKey(author)
}

func titleKey(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	// "Dune: Deluxe Edition", "Dune (Dune Chronicles, #1)", "Dune - The Graphic Novel"
	for _, sep := range []string{":", " (", " [", " - ", " — "} {
		if i := strings.Index(t, sep); i > 0 {
			t = t[:i]
		}
	}
	return titlePartKey(t)
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

// titleKeys is every way a title can be read: its main title first, then the subtitle
// when it has one. "Mistborn: The Final Empire" is the same book as "The Final Empire".
func titleKeys(title string) []string {
	keys := []string{titleKey(title)}
	t := strings.TrimSpace(title)
	for _, sep := range []string{":", " - ", " — "} {
		if i := strings.Index(t, sep); i > 0 {
			if sub := titlePartKey(t[i+len(sep):]); sub != "" && sub != keys[0] {
				keys = append(keys, sub)
			}
			break
		}
	}
	return keys
}

func keysOverlap(a, b []string) bool {
	for _, x := range a {
		if x == "" {
			continue
		}
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
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
	key := DedupeKey(title, author)
	if key == "" {
		return Book{}, false
	}
	list, err := s.repo.List(ctx)
	if err != nil {
		return Book{}, false
	}
	for _, b := range list {
		if DedupeKey(b.Title, b.Author) == key && (strings.TrimSpace(author) == "") == (strings.TrimSpace(b.Author) == "") {
			return b, true
		}
	}
	return Book{}, false
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
