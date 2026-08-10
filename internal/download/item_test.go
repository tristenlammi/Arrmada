package download

import "testing"

// Completion drove four decisions off one float — import it, judge it for stalling, allow
// seed cleanup, and consider the series free for another grab. RemainingBytes is a second
// opinion from the same payload, and both have to agree.
func TestItemComplete(t *testing.T) {
	cases := []struct {
		name string
		it   Item
		want bool
	}{
		{"done", Item{Progress: 1, RemainingBytes: 0}, true},
		{"still downloading", Item{Progress: 0.42, RemainingBytes: 500 << 20}, false},
		// The case a lone float can't see: the client says finished, its own byte counter
		// disagrees. Importing here yields a partial release and then deletes the torrent
		// once the seed goal passes.
		{"progress says done, bytes disagree", Item{Progress: 1, RemainingBytes: 12 << 20}, false},
		// Clients that report no byte counter at all keep the old behaviour rather than
		// being permanently stuck at "incomplete".
		{"no counter reported", Item{Progress: 1}, true},
	}
	for _, tc := range cases {
		if got := tc.it.Complete(); got != tc.want {
			t.Errorf("%s: Complete() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Deselected files are why a "complete" torrent can still be missing episodes — worth
// being able to say so, since amount_left counts only the files that WERE selected and so
// reaches zero regardless.
func TestItemPartiallySelected(t *testing.T) {
	const gb int64 = 1 << 30
	if !(Item{SizeBytes: 3 * gb, TotalSizeBytes: 4 * gb}).PartiallySelected() {
		t.Error("a torrent with a file excluded must report as partially selected")
	}
	if (Item{SizeBytes: 4 * gb, TotalSizeBytes: 4 * gb}).PartiallySelected() {
		t.Error("everything selected is not partial")
	}
	if (Item{SizeBytes: 4 * gb}).PartiallySelected() {
		t.Error("a client that reports no total must not read as partial")
	}
}
