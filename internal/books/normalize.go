package books

import (
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// Book titles and release names are compared as runs of whole words. Both sides go
// through the shared title fold (parser.FoldTitle, the same rules the movie and series
// matchers use), so:
//
//   - accents fold: "Pokémon Adventures" is "pokemon adventures", "García Márquez" is
//     "garcia marquez" — releases are named in ASCII;
//   - "&" and "and" agree, and the standalone word "and" is dropped, so "Pride &
//     Prejudice", "Pride and Prejudice" and a release that deleted the ampersand all
//     read "pride prejudice";
//   - curly and ASCII apostrophes are one character.
//
// Keeping only ASCII letters and digits, as this used to, turned "Pokémon" into "pok mon"
// and "Ender's Game" into "ender s game", so releases named "Pokemon" or "Enders.Game"
// were thrown away as "no release matched this title".

// wordKey reduces s to its folded words joined by single spaces, an apostrophe counting
// as a break between words ("ender's" → "ender s"). Word boundaries are kept so matching
// can require whole-word hits ("dune" must not match inside "dunemessiah").
func wordKey(s string) string {
	return strings.Join(parser.TitleWords(s), " ")
}

// joinedKey is wordKey with apostrophes dropped instead ("ender's" → "enders",
// "l'étranger" → "letranger").
func joinedKey(s string) string {
	return strings.Join(parser.TitleWords(strings.ReplaceAll(parser.FoldTitle(s), "'", "")), " ")
}

// foldVariants returns the forms s is compared in: the joined form first, and the split
// form when an apostrophe makes them differ. Release groups write "Ender's Game" as
// "Enders.Game" and as "Ender s Game [M4B]"; the two forms between them meet both.
func foldVariants(s string) []string {
	joined := joinedKey(s)
	if !hasApostrophe(s) {
		return []string{joined}
	}
	if split := wordKey(s); split != joined {
		return []string{joined, split}
	}
	return []string{joined}
}

func hasApostrophe(s string) bool { return strings.ContainsAny(s, "'’‘ʼ`´") }

// nameForms is a release or file name, folded once for matching against many titles.
type nameForms struct {
	forms []string // foldVariants: [0] joined, then split when there is an apostrophe
}

func formsOf(name string) nameForms { return nameForms{forms: foldVariants(name)} }

func (n nameForms) empty() bool { return len(n.forms) == 0 || n.forms[0] == "" }

// titleForms is a library title or author, folded once.
type titleForms struct {
	forms      []string
	apostrophe bool
}

func titleFormsOf(s string) titleForms {
	return titleForms{forms: foldVariants(s), apostrophe: hasApostrophe(s)}
}

// key is the title's primary (joined) form; "" when it has no words to match on.
func (t titleForms) key() string {
	if len(t.forms) == 0 {
		return ""
	}
	return t.forms[0]
}

// hay is which of a name's forms a title is looked for in. A title with no apostrophe
// is only looked for in the joined form: the split form of "It's Not Summer" holds a
// stray "it", and that is not Stephen King's It. A title with one is looked for in both,
// so "Ender's Game" meets "Enders.Game" and "Ender s Game".
func (t titleForms) hay(n nameForms) []string {
	if t.apostrophe || len(n.forms) < 2 {
		return n.forms
	}
	return n.forms[:1]
}

// in reports whether the title appears, as whole words, in the name.
func (t titleForms) in(n nameForms) bool {
	if t.key() == "" {
		return false
	}
	for _, h := range t.hay(n) {
		for _, f := range t.forms {
			if f != "" && containsWords(h, f) {
				return true
			}
		}
	}
	return false
}

// anyIn is in() for an author: any form of the author in any form of the name. The
// author only ranks candidates, so the looser reading is the safe one here.
func (t titleForms) anyIn(n nameForms) bool {
	if t.key() == "" {
		return false
	}
	for _, h := range n.forms {
		for _, f := range t.forms {
			if f != "" && containsWords(h, f) {
				return true
			}
		}
	}
	return false
}
