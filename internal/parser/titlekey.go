package parser

import (
	"strings"
	"unicode"
)

// TitleKey collapses a title to a comparison key: lowercase, accents folded, the
// conjunction dropped however it was spelled, and everything but letters/digits removed.
//
// A title joined by "and" reaches us three ways, and all three must key the same:
//
//	Bride & Prejudice     the library title, from the metadata provider
//	Bride and Prejudice   a release that spelled the word out
//	Bride Prejudice       a release that dropped it — "&" is awkward in a filename, so
//	                      YTS and friends simply delete it
//
// Expanding "&" to "and" handles the first two and misses the third: "brideandprejudice"
// against "brideprejudice" reads as a different film, which held a correctly-grabbed
// download for review and eventually filed it under a folder named without the ampersand.
// Removing the conjunction outright is the only form all three agree on.
//
// Only the standalone word goes — "Andrew" and "Bandit" keep theirs. Two genuinely
// different titles separated solely by the word "and" would now collide, which is a
// theoretical cost against a failure that happens in practice.
//
// Accents fold too. unicode.IsLetter accepts 'é', so without folding "Pokémon" keeps
// its diacritic and never matches a release named "Pokemon" — releases are named in
// ASCII. The searcher already folds the outbound query, so the search finds the
// releases; the match side has to fold the same way or it throws every one away.
func TitleKey(s string) string {
	lower := strings.ReplaceAll(strings.ToLower(FoldAccents(s)), "&", " and ")
	// Split on everything that isn't a letter or digit, so "and" is only recognised as a
	// whole word. Doing this after the "&" expansion means the symbol and the spelled-out
	// word take the same path.
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var b strings.Builder
	b.Grow(len(lower))
	for _, w := range words {
		if w == "and" {
			continue
		}
		b.WriteString(w)
	}
	return b.String()
}

// TitleWords is TitleKey's word list rather than its concatenation, for callers that
// need to compare titles a word at a time.
//
// TitleKey glues the words together, which loses the boundaries — "bleach" is a prefix
// of "bleachers" once the gaps are gone, but ["bleach"] is not a prefix of
// ["bleachers"]. Anything doing prefix work has to use this.
func TitleWords(s string) []string {
	lower := strings.ReplaceAll(strings.ToLower(FoldAccents(s)), "&", " and ")
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(words))
	for _, w := range words {
		if w == "and" {
			continue
		}
		out = append(out, w)
	}
	return out
}

// TitleHasPrefix reports whether title begins with prefix, compared whole word by whole
// word. An exact match counts.
//
// This is how an alias survives the things release groups append to an arc's name: a
// per-cour subtitle ("Thousand-Year Blood War The Calamity"), or junk the parser failed
// to strip ("Thousand-Year Blood War [BD Remux 1080p ...]"). Comparing on the glued key
// requires the whole title to be identical, and neither of those is.
//
// The word boundary is what makes this safe. "Bleach" does not match "Bleachers", and
// "Below Deck" would not match "Below Deck Mediterranean" — which is exactly why the
// series' OWN title is still compared for equality. Only user-declared aliases get
// prefix treatment, where "match this arc whatever they suffix it with" is the point.
func TitleHasPrefix(title, prefix string) bool {
	p := TitleWords(prefix)
	if len(p) == 0 {
		return false
	}
	t := TitleWords(title)
	if len(t) < len(p) {
		return false
	}
	for i, w := range p {
		if t[i] != w {
			return false
		}
	}
	return true
}
