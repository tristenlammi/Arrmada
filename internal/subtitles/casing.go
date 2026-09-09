package subtitles

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Whisper occasionally drifts into a lowercase, unpunctuated style for a stretch of
// audio — one window decodes that way and, conditioned on its own output, the rest of
// the chunk follows ("get some help get some serious help i mean i don't know"). The
// decoder is steered away from it (no context between windows, a punctuated prompt),
// and what still slips through is tidied here: a lone "i" and its contractions are
// capitalised in English, and the first letter of a sentence is upper-cased — after a
// full stop, question or exclamation mark, at the very start, or after a pause long
// enough to be a new sentence. Nothing else is touched: a cue that starts mid-sentence
// stays lowercase, as subtitles do.

const casingPauseBreak = 1500 * time.Millisecond

// englishI maps the lowercase first-person forms whisper leaves behind.
var englishI = map[string]string{
	"i": "I", "i'm": "I'm", "i'll": "I'll", "i've": "I've", "i'd": "I'd",
	"i’m": "I’m", "i’ll": "I’ll", "i’ve": "I’ve", "i’d": "I’d",
}

// fixCasing tidies the cues in place and returns them. english enables the "i" rule.
func fixCasing(cues []cue, english bool) []cue {
	sentenceStart := true
	var prevEnd time.Duration
	for i := range cues {
		if i > 0 && cues[i].start-prevEnd >= casingPauseBreak {
			sentenceStart = true
		}
		words := strings.Fields(cues[i].text)
		for j, w := range words {
			core, lead, trail := splitPunct(w)
			if core == "" {
				continue
			}
			if english {
				if up, ok := englishI[strings.ToLower(core)]; ok && core == strings.ToLower(core) {
					core = up
				}
			}
			if sentenceStart {
				core = upperFirst(core)
			}
			words[j] = lead + core + trail
			sentenceStart = closesSentence(trail)
		}
		cues[i].text = strings.Join(words, " ")
		prevEnd = cues[i].end
	}
	return cues
}

// splitPunct separates a token into leading punctuation (quotes, dashes, brackets),
// the word, and trailing punctuation.
func splitPunct(w string) (core, lead, trail string) {
	start := 0
	for start < len(w) {
		r, n := utf8.DecodeRuneInString(w[start:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			break
		}
		start += n
	}
	end := len(w)
	for end > start {
		r, n := utf8.DecodeLastRuneInString(w[start:end])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			break
		}
		end -= n
	}
	return w[start:end], w[:start], w[end:]
}

// closesSentence reports whether trailing punctuation closes a sentence. An ellipsis is
// a trailing-off, not an end, so what follows stays as it is.
func closesSentence(trail string) bool {
	t := strings.TrimRight(trail, `"'”’)]`)
	if strings.HasSuffix(t, "...") || strings.HasSuffix(t, "…") {
		return false
	}
	return strings.HasSuffix(t, ".") || strings.HasSuffix(t, "?") || strings.HasSuffix(t, "!")
}

// upperFirst capitalises the first letter of a word that starts lowercase.
func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if !unicode.IsLower(r) {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
