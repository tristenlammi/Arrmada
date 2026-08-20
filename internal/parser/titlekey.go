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
