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

// The Ted Lasso case: 38 HEVC episodes, each carrying 42 subtitle tracks against a
// keep-English target. The episode list called every one of them fixable while the show
// roll-up above it said "all efficient ✓" — because the roll-up asked about the codec
// alone. One definition now answers for both; this pins it.
func TestRollupAndListAgreeOnTheSameFile(t *testing.T) {
	dp := Plan{Subs: SubPlan{KeepLangs: []string{"en"}}}
	mi := &MediaInfo{
		VideoCodec: "hevc",
		Subs: []SubStream{
			{SubIndex: 0, Codec: "subrip", Lang: "eng", Text: true},
			{SubIndex: 1, Codec: "subrip", Lang: "fre", Text: true},
			{SubIndex: 2, Codec: "subrip", Lang: "spa", Text: true},
		},
	}

	n := needsOf(mi, dp, "hevc", false)
	if n.Video {
		t.Error("HEVC against an HEVC target was marked as needing a re-encode")
	}
	if !n.Subs {
		t.Fatal("42 subtitle tracks against a keep-English target read as already on target")
	}
	if !n.Any() {
		t.Error("the file was reported as needing nothing — this is the 'all efficient' bug")
	}
	if !n.RemuxOnly() {
		t.Error("fixing it would re-encode video that is already correct")
	}
}

// A file that genuinely matches must stay quiet, or every list fills with rows that have
// nothing to do.
func TestOnTargetFileIsNotListed(t *testing.T) {
	dp := Plan{Subs: SubPlan{KeepLangs: []string{"en"}}}
	mi := &MediaInfo{
		VideoCodec: "hevc",
		Subs:       []SubStream{{SubIndex: 0, Codec: "subrip", Lang: "eng", Text: true}},
	}
	if n := needsOf(mi, dp, "hevc", false); n.Any() {
		t.Errorf("a file matching the target in every respect was listed: %+v", n)
	}
}

// With no language filter configured, only the codec decides — the behaviour every
// existing install had before tracks were part of the target.
func TestWithoutLanguageFilterOnlyCodecCounts(t *testing.T) {
	mi := &MediaInfo{
		VideoCodec: "hevc",
		Subs: []SubStream{
			{SubIndex: 0, Codec: "subrip", Lang: "fre", Text: true},
			{SubIndex: 1, Codec: "subrip", Lang: "spa", Text: true},
		},
	}
	if n := needsOf(mi, Plan{}, "hevc", false); n.Any() {
		t.Errorf("with nothing configured to keep, a file was still listed: %+v", n)
	}
	// H.264 against an HEVC target is the wasteful source worth re-encoding.
	h264 := &MediaInfo{VideoCodec: "h264"}
	if n := needsOf(h264, Plan{}, "hevc", false); !n.Video {
		t.Error("H.264 against an HEVC target should want a re-encode")
	}
	// HEVC → AV1 stays refused even here: the gain is modest and it costs a second
	// generation of loss on already-lossy video. Asserted so a future change to the
	// target-spec model can't quietly re-enable it.
	if n := needsOf(mi, Plan{}, "av1", false); n.Video {
		t.Error("HEVC was offered for re-encoding to AV1 — that trade is deliberately refused")
	}
}
