package download

import "testing"

// Phase is what lets a dead torrent read differently from a live one. Every state string
// qBittorrent 4.x and 5.x report is listed, so a renamed or new state shows up here.
func TestItemPhase(t *testing.T) {
	cases := map[string]string{
		"metaDL":             "metadata",
		"forcedMetaDL":       "metadata", // v5
		"queuedDL":           "queued",
		"queuedUP":           "queued",
		"stalledDL":          "stalled",
		"checkingDL":         "checking",
		"checkingUP":         "checking",
		"checkingResumeData": "checking",
		"moving":             "moving",
		"allocating":         "allocating",
		"downloading":        "downloading",
		"forcedDL":           "downloading",
		"uploading":          "seeding",
		"stalledUP":          "seeding",
		"forcedUP":           "seeding",
		"pausedDL":           "paused",
		"pausedUP":           "paused",
		"stoppedDL":          "paused", // v5 renamed paused → stopped
		"stoppedUP":          "paused",
		"error":              "error",
		"missingFiles":       "error",
	}
	for raw, want := range cases {
		it := Item{RawState: raw, State: normalizeState(raw)}
		if got := it.Phase(); got != want {
			t.Errorf("Phase(%q) = %q, want %q", raw, got, want)
		}
	}
	// A state this build doesn't know, or a client that reports none, falls back to State.
	if got := (Item{RawState: "someFutureState", State: "downloading"}).Phase(); got != "downloading" {
		t.Errorf("unknown raw state: Phase = %q, want the normalized state", got)
	}
	if got := (Item{State: "seeding"}).Phase(); got != "seeding" {
		t.Errorf("no raw state: Phase = %q, want seeding", got)
	}
}

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
