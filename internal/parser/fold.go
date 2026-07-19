package parser

import "strings"

// foldMap maps accented Latin letters to their ASCII base. Multi-rune expansions
// ("æ" → "ae", "ß" → "ss") are why the values are strings.
var foldMap = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a", 'ă': "a", 'ą': "a",
	'æ': "ae",
	'ç': "c", 'ć': "c", 'ĉ': "c", 'ċ': "c", 'č': "c",
	'ď': "d", 'đ': "d", 'ð': "d",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ĕ': "e", 'ė': "e", 'ę': "e", 'ě': "e",
	'ĝ': "g", 'ğ': "g", 'ġ': "g", 'ģ': "g",
	'ĥ': "h", 'ħ': "h",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ĩ': "i", 'ī': "i", 'ĭ': "i", 'į': "i", 'ı': "i",
	'ĵ': "j",
	'ķ': "k",
	'ĺ': "l", 'ļ': "l", 'ľ': "l", 'ŀ': "l", 'ł': "l",
	'ñ': "n", 'ń': "n", 'ņ': "n", 'ň': "n", 'ŋ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ō': "o", 'ŏ': "o", 'ő': "o",
	'œ': "oe",
	'ŕ': "r", 'ŗ': "r", 'ř': "r",
	'ś': "s", 'ŝ': "s", 'ş': "s", 'š': "s",
	'ţ': "t", 'ť': "t", 'ŧ': "t",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ũ': "u", 'ū': "u", 'ŭ': "u", 'ů': "u", 'ű': "u", 'ų': "u",
	'ŵ': "w",
	'ý': "y", 'ÿ': "y", 'ŷ': "y",
	'ź': "z", 'ż': "z", 'ž': "z",
	'þ': "th", 'ß': "ss",
}

// FoldAccents rewrites accented Latin letters as their ASCII base, so "Pokémon"
// becomes "Pokemon". Scene and p2p releases are named in ASCII almost without
// exception, so both the indexer query and the title match have to be folded —
// otherwise a title like "Pokémon Heroes" finds (and matches) nothing. Runes with
// no mapping, including non-Latin scripts, pass through untouched.
func FoldAccents(s string) string {
	if isASCII(s) {
		return s // fast path: nothing to fold
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if repl, ok := foldMap[r]; ok {
			b.WriteString(repl)
			continue
		}
		// Uppercase accented letters fold via their lowercase mapping, preserving case.
		if lower := toLowerRune(r); lower != r {
			if repl, ok := foldMap[lower]; ok {
				b.WriteString(strings.ToUpper(repl))
				continue
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// toLowerRune lowercases a single rune (strings.ToLower on a rune-sized string).
func toLowerRune(r rune) rune {
	lowered := []rune(strings.ToLower(string(r)))
	if len(lowered) == 1 {
		return lowered[0]
	}
	return r
}
