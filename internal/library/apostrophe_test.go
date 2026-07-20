package library

import "testing"

// An apostrophe sits INSIDE a word, so replacing it with a space splits the word in two.
// Real examples from a library import: "Tom's Divorce" → "Tom s Divorce", "Li'l Sebastian"
// → "Li l Sebastian". It's legal on every filesystem we target, it's what the title
// actually is, and files scanned in from elsewhere keep it — so stripping it also made
// Arrmada's naming inconsistent with the rest of the library.
func TestApostrophesSurviveNaming(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Tom's Divorce", "Tom's Divorce"},
		{"Li'l Sebastian", "Li'l Sebastian"},
		{"Andy and April's Fancy Party", "Andy and April's Fancy Party"},
		{"Meet 'n' Greet", "Meet 'n' Greet"},
		{"Ben's Parents", "Ben's Parents"},
		// The typographic apostrophe TMDB often uses must survive too.
		{"Jerry’s Painting", "Jerry’s Painting"},
	}
	for _, c := range cases {
		if got := cleanTitleLoose(c.in); got != c.want {
			t.Errorf("cleanTitleLoose(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The punctuation that genuinely reads badly in a filename is still dropped — that's
// what the rule is for, and the fix must not undo it.
func TestSentencePunctuationStillDropped(t *testing.T) {
	cases := []struct{ in, want string }{
		{"tick, tick... BOOM!", "tick tick BOOM"},
		{"Gin It Up!", "Gin It Up"},
		{"Who Framed Roger Rabbit?", "Who Framed Roger Rabbit"},
		{"Mission: Impossible", "Mission Impossible"},
		{"One in 8,000", "One in 8 000"},
		// Hyphens and ampersands are deliberately kept.
		{"Spider-Man", "Spider-Man"},
		{"Fast & Furious", "Fast & Furious"},
	}
	for _, c := range cases {
		if got := cleanTitleLoose(c.in); got != c.want {
			t.Errorf("cleanTitleLoose(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
