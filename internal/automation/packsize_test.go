package automation

import "testing"

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
