package automation

import "testing"

// Scene releases carry no punctuation, so searching with a title verbatim can hide an
// entire show. "Teen Titans Go!" ships as "Teen.Titans.Go.S07..." — the exclamation mark
// alone was enough to make its season packs invisible.
func TestIndexerQuery(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Teen Titans Go!", "Teen Titans Go"},
		// "&" becomes "and" — that's how releases spell it, and searching "Love Death"
		// instead matched "Love, Death & Robots" so well it crowded out the real show.
		{"Love & Death", "Love and Death"},
		{"Will & Grace", "Will and Grace"},
		{"Marvel's Agents of S.H.I.E.L.D.", "Marvels Agents of S H I E L D"},
		{"Bob's Burgers", "Bobs Burgers"},
		{"Law & Order: SVU", "Law and Order SVU"},
		{"Pokémon Heroes", "Pokemon Heroes"},
		{"Dexter's Laboratory (1996)", "Dexters Laboratory 1996"},
		{"Teen.Titans.Go", "Teen Titans Go"},
		{"  spaced   out  ", "spaced out"},
		{"Normal Title", "Normal Title"},
	}
	for _, c := range cases {
		if got := indexerQuery(c.in); got != c.want {
			t.Errorf("indexerQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
