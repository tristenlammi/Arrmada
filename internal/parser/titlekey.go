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
//
// Bracketed groups after the title text are dropped, because they are alternate titles
// or disambiguators the other side rarely carries: "My Hero Academia (Boku no Hero
// Academia)" and "Show [2019]" key the same as "My Hero Academia" and "Show". A group
// at the very START is part of the title and only loses its brackets: "(500) Days of
// Summer" keys as "500 Days of Summer", not "Days of Summer", and "(Un)Well" as "Unwell".
//
// This is the one title key for every download, queue and library match (movies,
// series, the Downloads feed, books' folder fallback). The cases above are pinned in
// titlekey_test.go; separate copies of this rule drifting apart is how 'Love & Death'
// stopped showing its download when the torrent was named 'Love.and.Death'.
func TitleKey(s string) string {
	lower := FoldTitle(stripAltTitles(s))
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

// FoldTitle is the fold every title matcher starts from — TitleKey and TitleWords here,
// and the books matcher's word keys — so they agree on what counts as the same spelling:
//
//   - accents fold to ASCII ("Pokémon" → "pokemon", "García Márquez" → "garcia marquez");
//   - everything is lower-case;
//   - typographic apostrophes (’ ‘ ʼ ` ´) become the ASCII "'", so a caller that cares
//     about apostrophes ("Ender’s" vs "Enders" vs "Ender s") has one character to handle;
//   - "&" becomes " and ", so the symbol and the spelled-out word take the same path
//     (callers then drop the standalone word "and"; see TitleKey for why).
//
// Everything else — punctuation, brackets, non-Latin scripts — is left for the caller's
// own word split.
func FoldTitle(s string) string {
	return strings.ReplaceAll(apostrophes.Replace(strings.ToLower(FoldAccents(s))), "&", " and ")
}

var apostrophes = strings.NewReplacer("’", "'", "‘", "'", "ʼ", "'", "`", "'", "´", "'")

// stripAltTitles drops bracketed groups that follow the title text and unwraps a group
// at the very start (see TitleKey). A stray closer is dropped, as in StripBracketed.
func stripAltTitles(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	depth := 0
	leading := true // nothing but space so far: a group here is part of the title
	for _, r := range s {
		switch r {
		case '(', '[', '{':
			if leading && depth == 0 {
				continue // keep the group's words, lose the bracket
			}
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
				if !unicode.IsSpace(r) {
					leading = false
				}
			}
		}
	}
	return b.String()
}

// TitleWords is TitleKey's word list rather than its concatenation, for callers that
// need to compare titles a word at a time. Unlike TitleKey it keeps bracketed groups:
// alias matching reads the number that follows an arc's name, wherever groups sit.
//
// TitleKey glues the words together, which loses the boundaries — "bleach" is a prefix
// of "bleachers" once the gaps are gone, but ["bleach"] is not a prefix of
// ["bleachers"]. Anything doing prefix work has to use this.
func TitleWords(s string) []string {
	lower := FoldTitle(s)
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

// countryCodes are the country tags release groups put after a show's title to tell
// same-named shows apart ("The.Office.US", "Ghosts.UK"), mapped to the ISO 3166 code
// TMDB uses for origin_country. UK is GB there.
var countryCodes = map[string]string{"us": "US", "uk": "GB", "gb": "GB", "au": "AU", "nz": "NZ", "ca": "CA"}

// SplitCountry splits a trailing country tag off a title: "The Office US" and "The
// Office (US)" both give ("The Office", "US"). cc is the ISO 3166 code ("GB" for a UK
// tag); it is "" and base is the title unchanged when there is no tag, or when the tag
// would be the whole title.
//
// A tag can also be a real last word ("This Is Us"). Callers only ever use it to rule a
// show out when the show's own title doesn't end the same way — see series.FitRelease.
func SplitCountry(title string) (base, cc string) {
	t := strings.TrimRight(strings.TrimSpace(title), " .-_")
	// "(US)" / "[UK]" at the end.
	if n := len(t); n >= 4 && (t[n-1] == ')' || t[n-1] == ']') {
		open := strings.LastIndexAny(t, "([")
		if open > 0 {
			if code, ok := countryCodes[strings.ToLower(strings.TrimSpace(t[open+1:n-1]))]; ok {
				if b := strings.TrimRight(t[:open], " .-_"); b != "" {
					return b, code
				}
			}
		}
	}
	// A bare trailing word, however the words are separated.
	words := strings.FieldsFunc(t, func(r rune) bool { return r == ' ' || r == '.' || r == '_' })
	if len(words) < 2 {
		return title, ""
	}
	code, ok := countryCodes[strings.ToLower(words[len(words)-1])]
	if !ok {
		return title, ""
	}
	return strings.Join(words[:len(words)-1], " "), code
}

// IsLatin reports whether a title is written in the Latin alphabet — what release names
// use. TMDB's original_name for a Japanese show is kana or kanji (葬送のフリーレン), which
// no release carries, so it is no use as a release title; its romaji form ("Sousou no
// Frieren") is. Digits and punctuation don't count either way; a title with no letters
// at all is not Latin.
func IsLatin(title string) bool {
	letters := 0
	for _, r := range title {
		if !unicode.IsLetter(r) {
			continue
		}
		if !unicode.Is(unicode.Latin, r) {
			return false
		}
		letters++
	}
	return letters > 0
}
