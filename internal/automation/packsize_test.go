package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

func seasons(ns ...int) map[int]bool {
	m := map[int]bool{}
	for _, n := range ns {
		m[n] = true
	}
	return m
}

// Covering what's needed isn't sufficient — a six-season complete pack "covers" a single
// missing episode in season 3, which is how an entire show got downloaded to fill one gap.
func TestPackProportionality(t *testing.T) {
	cases := []struct {
		name   string
		needed map[int]bool
		pack   map[int]bool
		want   bool
	}{
		{"one season needed from a six-season pack", seasons(3), seasons(1, 2, 3, 4, 5, 6), false},
		{"two of six", seasons(3, 4), seasons(1, 2, 3, 4, 5, 6), false},
		{"three of six is the boundary", seasons(1, 2, 3), seasons(1, 2, 3, 4, 5, 6), true},
		{"five of six", seasons(1, 2, 3, 4, 5), seasons(1, 2, 3, 4, 5, 6), true},
		{"whole show missing", seasons(1, 2, 3), seasons(1, 2, 3), true},
		{"single-season pack is always fine", seasons(4), seasons(4), true},
		{"one of two", seasons(2), seasons(1, 2), true}, // half, so still worth it
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := packIsProportionate(c.needed, c.pack); got != c.want {
				t.Errorf("packIsProportionate(%v, %v) = %v, want %v", c.needed, c.pack, got, c.want)
			}
		})
	}
}

// A pack has to bring mostly things you want. Judging only by "covers the most needed
// episodes" meant a four-season pack won for a single missing episode — and even a single
// season pack is a poor trade for one gap: 38 episodes downloaded to obtain one.
func TestPackIsWorthIt(t *testing.T) {
	// Dexter's Laboratory: four seasons, the exact case that prompted this.
	all := seasons(1, 2, 3, 4)
	counts := map[int]int{1: 38, 2: 108, 3: 36, 4: 38}

	four := parser.Parse("Dexters Laboratory S01-S04 1080p WEB-DL DD2 0 H 264-squalor")
	if packIsWorthIt(four, 1, all, counts) {
		t.Error("a four-season pack must not be taken for one missing episode")
	}
	if !packIsWorthIt(four, 200, all, counts) {
		t.Error("a four-season pack IS worth it when most of the show is missing")
	}

	one := parser.Parse("Dexters Laboratory S01 1080p WEB-DL DD2 0 H 264-squalor")
	if packIsWorthIt(one, 1, all, counts) {
		t.Error("a 38-episode season pack must not be taken for one missing episode")
	}
	if !packIsWorthIt(one, 30, all, counts) {
		t.Error("a season pack IS worth it when most of the season is missing")
	}

	// Missing episode counts must not block a grab — better an inefficient pack than none.
	if !packIsWorthIt(one, 1, all, map[int]int{}) {
		t.Error("unknown episode counts should not veto a pack")
	}
}
