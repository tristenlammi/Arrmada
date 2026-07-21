package parser

import (
	"strings"
	"unicode"
)

// TitleKey collapses a title to a comparison key: lowercase, accents folded,
// "&" spelled out as "and", and everything but letters/digits dropped.
//
// "&" and "and" are the same word, and releases pick either freely: a library title of
// "Love & Death" has to match "Love.and.Death..." as well as "Love.&.Death....".
// Stripping the ampersand as punctuation would make those two spellings different keys
// (lovedeath vs loveanddeath), so an entire title's releases would be rejected as
// belonging to something else. Normalize to one form before dropping the rest.
//
// Accents fold too. unicode.IsLetter accepts 'é', so without folding "Pokémon" keeps
// its diacritic and never matches a release named "Pokemon" — releases are named in
// ASCII. The searcher already folds the outbound query, so the search finds the
// releases; the match side has to fold the same way or it throws every one away.
func TitleKey(s string) string {
	lower := strings.ReplaceAll(strings.ToLower(FoldAccents(s)), "&", " and ")
	var b strings.Builder
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
