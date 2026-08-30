package convert

import (
	"encoding/json"
	"strings"
	"testing"
)

// The Tracks screen crashed to a blank page because splitCSV returns a NIL slice for an
// empty setting, and a nil slice marshals to JSON null rather than []. The client then
// read .length off null — on exactly the state this screen exists to explain, "you
// haven't configured any languages yet".
func TestTrackCleanupMarshalsEmptyListsAsArrays(t *testing.T) {
	b, err := json.Marshal(TrackCleanup{
		KeepSubs:  nonEmpty(nil),
		KeepAudio: nonEmpty(nil),
		Movies:    []TrackCleanupItem{},
		Series:    []TrackCleanupSeries{},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, "null") {
		t.Fatalf("a null reached the client — this is what blanked the page:\n%s", got)
	}
	for _, want := range []string{`"keep_subs":[]`, `"keep_audio":[]`, `"movies":[]`, `"series":[]`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

func TestNonEmptyKeepsRealValues(t *testing.T) {
	if got := nonEmpty([]string{"en"}); len(got) != 1 || got[0] != "en" {
		t.Errorf("nonEmpty mangled a real value: %v", got)
	}
}
