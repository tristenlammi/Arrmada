package automation

import (
	"reflect"
	"testing"
)

// The absolute-number follow-up is capped at 3 queries a sweep over a list sorted
// ascending, so without rotation it re-asked about the same three oldest gaps forever and
// a later season's episodes were never reached at all.
func TestRotateKeys(t *testing.T) {
	keys := []epKey{{1, 1}, {1, 2}, {13, 5}, {17, 14}}

	if got := rotateKeys(keys, 0); !reflect.DeepEqual(got, keys) {
		t.Errorf("cursor 0 = %v, want the list unchanged", got)
	}

	// Resuming mid-list puts the cursor's episode first and wraps the earlier ones behind
	// it, so nothing is dropped — just deferred.
	want := []epKey{{13, 5}, {17, 14}, {1, 1}, {1, 2}}
	if got := rotateKeys(keys, epCursor(epKey{13, 5})); !reflect.DeepEqual(got, want) {
		t.Errorf("rotate at S13E05 = %v, want %v", got, want)
	}

	// A cursor pointing at an episode that has since been filled resumes at the next one
	// still missing, rather than falling back to the start.
	want = []epKey{{17, 14}, {1, 1}, {1, 2}, {13, 5}}
	if got := rotateKeys(keys, epCursor(epKey{14, 1})); !reflect.DeepEqual(got, want) {
		t.Errorf("rotate at a filled S14E01 = %v, want %v", got, want)
	}

	// Past every remaining key → wrap to the beginning.
	if got := rotateKeys(keys, epCursor(epKey{99, 1})); !reflect.DeepEqual(got, keys) {
		t.Errorf("cursor past the end = %v, want the list unchanged", got)
	}
}

// epCursor has to keep the (season, episode) ordering it packs, or a resume point
// compares wrong and the rotation skips seasons.
func TestEpCursorOrdering(t *testing.T) {
	if epCursor(epKey{1, 99}) >= epCursor(epKey{2, 1}) {
		t.Error("a late episode of season 1 must sort before season 2")
	}
	if epCursor(epKey{17, 13}) >= epCursor(epKey{17, 14}) {
		t.Error("episodes within a season must stay ordered")
	}
}
