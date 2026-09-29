package books

import (
	"context"
	"strings"
)

// A file inside a download often names more than one library book: "Fire & Blood (HBO
// Tie-in Edition) - 300 Years Before A Game of Thrones", "Mockingjay (The Final Book of
// The Hunger Games)". The release matcher's rule — the longest matching title wins — is
// right for a release name (it keeps "Dune Messiah" off "Dune"), but for these files it
// picked the book named in the subtitle: Fire & Blood's audiobook was filed as A Game of
// Thrones, and Mockingjay's ebook as The Hunger Games.
//
// A file is named after the book it is, first. So the lead title wins:
//   - a title found inside a longer one at the same place gives way to it ("Dune" inside
//     "Dune Messiah");
//   - a title followed straight away by a number is a series label, not the book ("Red
//     Rising 2 - Golden Son" is Golden Son) — unless nothing else matches;
//   - of what's left, an author-confirmed match beats one without, then the title that
//     starts earliest in the name.

// FileMatcher returns a matcher for file names inside a download, reading the library once.
func (s *Service) FileMatcher(ctx context.Context) func(name string) (Book, bool) {
	all, err := s.repo.List(ctx)
	if err != nil {
		return func(string) (Book, bool) { return Book{}, false }
	}
	return func(name string) (Book, bool) { return matchFileName(all, name) }
}

type fileHit struct {
	book     Book
	start    int // word index where the title begins
	length   int // words in the title
	numbered bool
	author   bool
}

func matchFileName(all []Book, name string) (Book, bool) {
	words := strings.Fields(wordKey(name))
	if len(words) == 0 {
		return Book{}, false
	}
	rel := strings.Join(words, " ")
	var hits []fileHit
	for _, b := range all {
		tw := strings.Fields(wordKey(b.Title))
		if len(tw) == 0 {
			continue
		}
		start := wordIndex(words, tw)
		if start < 0 {
			continue
		}
		end := start + len(tw)
		numbered := end < len(words) && isNumberWord(words[end])
		a := wordKey(b.Author)
		hits = append(hits, fileHit{book: b, start: start, length: len(tw), numbered: numbered,
			author: a != "" && containsWords(rel, a)})
	}
	if len(hits) == 0 {
		return Book{}, false
	}
	// Drop titles that sit inside a longer matching title.
	kept := hits[:0:0]
	for i, h := range hits {
		inside := false
		for j, o := range hits {
			if i != j && o.length > h.length && h.start >= o.start && h.start+h.length <= o.start+o.length {
				inside = true
				break
			}
		}
		if !inside {
			kept = append(kept, h)
		}
	}
	// A series label ("Red Rising 2 …") steps aside for the book it labels.
	var named []fileHit
	for _, h := range kept {
		if !h.numbered {
			named = append(named, h)
		}
	}
	if len(named) > 0 {
		kept = named
	}
	best := kept[0]
	for _, h := range kept[1:] {
		switch {
		case h.author != best.author:
			if h.author {
				best = h
			}
		case h.start != best.start:
			if h.start < best.start {
				best = h
			}
		case h.length > best.length:
			best = h
		}
	}
	return best.book, true
}

// wordIndex returns where needle's words first appear contiguously in hay, or -1.
func wordIndex(hay, needle []string) int {
outer:
	for i := 0; i+len(needle) <= len(hay); i++ {
		for j := range needle {
			if hay[i+j] != needle[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

func isNumberWord(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
