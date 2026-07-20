package automation

import (
	"strings"
	"testing"
)

// A release that re-processes without adding anything gets blocklisted so the searcher
// stops re-grabbing it. That much is right. But the message shown to the user has to
// distinguish the two very different reasons it can happen, because one is a fault and
// the other is completely normal.
func TestIncompleteSeasonReason(t *testing.T) {
	// The normal case: a streaming-sourced pack carries fewer episodes than the metadata
	// lists for the season (TMDB says Peppa Pig S1 has 52; the service carried ~28). Every
	// file landed on a real episode, so there is nothing wrong with the release — and
	// telling the user it has "unresolved episode numbering" sends them hunting a bug that
	// doesn't exist.
	whole := incompleteSeasonReason(0)
	if strings.Contains(whole, "numbering") {
		t.Errorf("a release whose files all resolved must not be blamed for numbering: %q", whole)
	}
	if !strings.Contains(whole, "fully imported") {
		t.Errorf("reason should say the import succeeded: %q", whole)
	}

	// The actual fault: files that map to no known episode (scene numbering that doesn't
	// line up with the metadata). This one IS worth flagging as a numbering problem.
	broken := incompleteSeasonReason(7)
	if !strings.Contains(broken, "unresolved episode numbering") {
		t.Errorf("unmatched files should be reported as a numbering problem: %q", broken)
	}
	if !strings.Contains(broken, "7 files") {
		t.Errorf("reason should say how many files failed: %q", broken)
	}

	// Singular reads correctly too — "1 files" in a user-facing string is sloppy.
	if one := incompleteSeasonReason(1); !strings.Contains(one, "1 file couldn't") {
		t.Errorf("singular form: %q", one)
	}
}
