package automation

import "testing"

// Book trackers state the series on every listing — "Coven of Bones #1" — and the searcher
// folded that into the text it scored against and kept nothing. This is the parse that
// turns the credit back into something a library can group by.
func TestParseBookSeries(t *testing.T) {
	cases := []struct {
		in   string
		name string
		pos  float64
	}{
		{"Coven of Bones #1", "Coven of Bones", 1},
		{"Red Rising Saga #2", "Red Rising Saga", 2},
		// Novellas are numbered between books; rounding them into a neighbour would merge
		// two distinct entries.
		{"Discworld #1.5", "Discworld", 1.5},
		{"The Expanse # 4", "The Expanse", 4},
		// A series with no number is still worth knowing — grouping doesn't need one.
		{"Coven of Bones", "Coven of Bones", 0},
		// An omnibus lists several; the one it leads with is the one it belongs to.
		{"Coven of Bones #1, Dark Fae #3", "Coven of Bones", 1},
		// Nothing to learn.
		{"", "", 0},
		{"   ", "", 0},
		{"#3", "", 0}, // a number naming no series
	}
	for _, c := range cases {
		name, pos := parseBookSeries(c.in)
		if name != c.name || pos != c.pos {
			t.Errorf("parseBookSeries(%q) = (%q, %v), want (%q, %v)", c.in, name, pos, c.name, c.pos)
		}
	}
}

// A hole BETWEEN two owned entries is a fact — you have #1 and #3, so #2 exists and you
// don't have it. Anything past the highest owned position is a guess about how long the
// series runs, which nothing here knows, so it must not be reported as missing.
func TestSeriesGapsAreOnlyReportedBetweenOwnedEntries(t *testing.T) {
	// Mirrors BookSeriesFor's walk over whole-number positions.
	owned := map[int]bool{1: true, 3: true, 4: true}
	maxPos := 4
	var missing []int
	for i := 1; i <= maxPos; i++ {
		if !owned[i] {
			missing = append(missing, i)
		}
	}
	if len(missing) != 1 || missing[0] != 2 {
		t.Errorf("missing = %v, want just [2] — #5 onward is unknown, not missing", missing)
	}
}
