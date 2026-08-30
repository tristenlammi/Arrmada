package convert

import "testing"

// The whole model in one table: a file is listed when it doesn't match the target, and
// the reason says which part. Before this, "candidate" meant wrong codec and nothing
// else — so a library already in the target codec looked finished while every file
// still carried thirty subtitle tracks.
func TestNeedsReasonAndPlanChoice(t *testing.T) {
	for _, tc := range []struct {
		name      string
		n         Needs
		any       bool
		remuxOnly bool
		reason    string
	}{
		{"already on target", Needs{}, false, false, ""},
		{"wrong codec only", Needs{Video: true}, true, false, "video"},
		{"subtitles only", Needs{Subs: true}, true, true, "subtitles"},
		{"audio only", Needs{Audio: true}, true, true, "audio"},
		{"both tracks", Needs{Subs: true, Audio: true}, true, true, "subtitles + audio"},
		{"codec and tracks", Needs{Video: true, Subs: true}, true, false, "video + tracks"},
	} {
		if got := tc.n.Any(); got != tc.any {
			t.Errorf("%s: Any() = %v, want %v", tc.name, got, tc.any)
		}
		// RemuxOnly is what decides minutes versus hours, so it has to be exactly right:
		// a file with the wrong codec must never be "fixed" by copying its video.
		if got := tc.n.RemuxOnly(); got != tc.remuxOnly {
			t.Errorf("%s: RemuxOnly() = %v, want %v", tc.name, got, tc.remuxOnly)
		}
		if got := tc.n.Reason(); got != tc.reason {
			t.Errorf("%s: Reason() = %q, want %q", tc.name, got, tc.reason)
		}
	}
}

// The case that motivated the redesign: an HEVC file in an HEVC library, carrying
// subtitle languages the target doesn't keep. Its codec is right, so the old model said
// "efficient" and never offered to touch it.
func TestOnTargetCodecStillNeedsTrackWork(t *testing.T) {
	n := Needs{Video: false, Subs: true}
	if !n.Any() {
		t.Fatal("a file with unwanted subtitle tracks was reported as already on target")
	}
	if !n.RemuxOnly() {
		t.Error("fixing it would re-encode video that is already correct")
	}
	if n.Reason() != "subtitles" {
		t.Errorf("reason = %q, want it to say which part is wrong", n.Reason())
	}
}
