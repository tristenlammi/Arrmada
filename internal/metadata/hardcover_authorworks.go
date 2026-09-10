package metadata

import (
	"regexp"
	"strings"
	"time"
)

// An author's catalogue on Hardcover, as the raw contributions join returns it, is a
// mess for a prolific writer: books they only wrote a foreword for, anthologies they
// have a story in, every translation as its own entry, and the halves of a novel that
// was split for a paperback run. This trims the listing to the books the author
// actually wrote, in English, that are whole, so "More by", the author pages on
// Discover, and "Add author" all see the same clean list.

// hcAuthorBookFields adds to the shared book fields what the filters need: each
// contribution's role, and the language of the default editions.
const hcAuthorBookFields = hcBookFields + ` contributions { contribution author { id name } } default_physical_edition { language { code2 } } default_ebook_edition { language { code2 } }`

// mainAuthorRoles are the contribution labels that mean "wrote it". Hardcover leaves
// the role empty for the principal author and names the rest ("Illustrator",
// "Narrator", "Foreword", "Translator", "Editor", "Contributor").
var mainAuthorRoles = map[string]bool{"": true, "author": true, "writer": true, "co-author": true, "coauthor": true}

// splitPartRe reads "The Way of Kings, Part 1" / "Words of Radiance: Part Two" /
// "Oathbringer Vol. 2 of 2" — a half of a book, not a book.
var splitPartRe = regexp.MustCompile(`(?i)^(.*?\S)[\s,:\-–—]+(?:part|pt\.?|volume|vol\.?)\s*(?:\d+|one|two|three|four|i{1,3}|iv)(?:\s+of\s+\d+)?\s*$`)

// authoredBy reports whether this author is credited as a writer of the book, not
// just a contributor to it.
func (b hcBook) authoredBy(authorID int) bool {
	for _, c := range b.Contributions {
		if c.Author.ID == authorID && mainAuthorRoles[strings.ToLower(strings.TrimSpace(c.Contribution))] {
			return true
		}
	}
	return false
}

// language is the book's language from its default editions ("" when unknown).
func (b hcBook) language() string {
	for _, e := range []*hcEditionLang{b.DefaultPhysicalEdition, b.DefaultEbookEdition} {
		if e != nil && e.Language != nil && e.Language.Code2 != "" {
			return strings.ToLower(e.Language.Code2)
		}
	}
	return ""
}

// filterAuthorWorks keeps the books the author wrote, in English (or of unknown
// language), that aren't a split part of another listed book, dropping the long
// tail of entries nobody has shelved unless they are recent enough to be new.
func filterAuthorWorks(in []hcBook, authorID int, now time.Time) []BookResult {
	top := 0
	for _, b := range in {
		if b.UsersCount > top {
			top = b.UsersCount
		}
	}
	floor := top / 250
	if floor < 2 {
		floor = 2
	}
	titles := map[string]bool{}
	for _, b := range in {
		titles[strings.ToLower(strings.TrimSpace(b.Title))] = true
	}
	out := make([]BookResult, 0, len(in))
	for _, b := range in {
		r := b.result()
		if r.Title == "" || !b.authoredBy(authorID) {
			continue
		}
		if lang := b.language(); lang != "" && lang != "en" {
			continue
		}
		recent := b.ReleaseYear != nil && *b.ReleaseYear >= now.Year()-1
		if b.UsersCount < floor && !recent {
			continue
		}
		if m := splitPartRe.FindStringSubmatch(r.Title); m != nil && titles[strings.ToLower(strings.TrimSpace(m[1]))] {
			continue
		}
		out = append(out, r)
	}
	return filterBundles(out)
}
