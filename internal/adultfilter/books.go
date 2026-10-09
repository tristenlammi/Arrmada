package adultfilter

import (
	"regexp"
	"strings"
)

// Books need their own rule. The studio and sex-act lists above are built for video
// release names and film titles, so an explicit novel almost never trips them on its
// title alone. What a book catalogue does carry is tags (Open Library subjects,
// Hardcover genres and tags, Google Books categories), and erotica is labelled there.
//
// Only clear labels block. "Adult Fiction", "New Adult", "Young Adult" and plain
// "Adult" are reading-age shelves, not content warnings, and ordinary romance must stay
// visible, so none of them is on the list.
var adultBookTags = []string{
	"erotica", "erotic", "erotic romance", "erotic fiction", "bdsm", "pornography", "porn", "xxx",
}

// reAdultBookTag matches a listed label as whole words anywhere in a tag, so
// "Fiction / Romance / Erotica" and "Erotic literature" count while "Eroticism in art"
// doesn't.
var reAdultBookTag = regexp.MustCompile(`(?i)\b(` + strings.Join(adultBookTags, "|") + `)\b`)

// BookIsAdult reports whether a book is adult content, judged by its title (the same
// matcher as every other surface) and its catalogue tags.
func BookIsAdult(title string, tags []string) bool {
	if Matches(title) {
		return true
	}
	for _, t := range tags {
		if reAdultBookTag.MatchString(t) {
			return true
		}
	}
	return false
}

// FilterBooks drops the adult entries from a list of books. fields reads an entry's
// title and every tag it has (taking an accessor keeps this package free of the
// metadata types, which already import it).
func FilterBooks[T any](in []T, fields func(T) (title string, tags []string)) []T {
	out := make([]T, 0, len(in))
	for _, b := range in {
		if title, tags := fields(b); !BookIsAdult(title, tags) {
			out = append(out, b)
		}
	}
	return out
}
