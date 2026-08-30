package convert

import (
	"strings"
	"testing"
)

func subs(specs ...SubStream) *MediaInfo {
	return &MediaInfo{VideoCodec: "hevc", Container: "mkv", Subs: specs}
}

// A WEB-DL with thirty-odd subtitle tracks is the case this exists for.
func TestKeptSubsFiltersByLanguage(t *testing.T) {
	mi := subs(
		SubStream{SubIndex: 0, Codec: "ass", Lang: "chi", Text: true},
		SubStream{SubIndex: 1, Codec: "subrip", Lang: "eng", Text: true},
		SubStream{SubIndex: 2, Codec: "subrip", Lang: "fre", Text: true},
		SubStream{SubIndex: 3, Codec: "subrip", Lang: "", Text: true}, // untagged
	)
	got := keptSubs(mi, Plan{Subs: SubPlan{KeepLangs: []string{"en"}}})
	if len(got) != 2 {
		t.Fatalf("kept %d tracks, want the English one plus the untagged one: %+v", len(got), got)
	}
	if got[0].Lang != "eng" || got[1].Lang != "" {
		t.Errorf("kept %+v, want eng and the untagged track", got)
	}

	// No filter configured: nothing is touched.
	if all := keptSubs(mi, Plan{}); len(all) != 4 {
		t.Errorf("with no filter, kept %d of 4 — the default must be untouched", len(all))
	}
}

// Wrong or unexpected tags must not produce a file with no subtitles at all. Keeping
// the clutter is recoverable; shipping none is not, since the original is replaced.
func TestKeptSubsNeverStripsEverything(t *testing.T) {
	mi := subs(
		SubStream{SubIndex: 0, Codec: "subrip", Lang: "chi", Text: true},
		SubStream{SubIndex: 1, Codec: "subrip", Lang: "jpn", Text: true},
	)
	if got := keptSubs(mi, Plan{Subs: SubPlan{KeepLangs: []string{"en"}}}); len(got) != 2 {
		t.Errorf("kept %d, want all of them back — no English track exists to keep", len(got))
	}
}

// Mapped output streams renumber from 0, so a per-stream codec override has to use the
// OUTPUT position. Using the input index would aim the override at the wrong stream.
func TestFilteredSubOverridesUseOutputIndexes(t *testing.T) {
	mi := subs(
		SubStream{SubIndex: 0, Codec: "subrip", Lang: "chi", Text: true},
		SubStream{SubIndex: 1, Codec: "subrip", Lang: "spa", Text: true},
		SubStream{SubIndex: 2, Codec: "mov_text", Lang: "eng", Text: true}, // MKV can't copy this
	)
	args := strings.Join(compileOutputArgs(Encoder{Codec: "hevc", Name: "libx265", Kind: "cpu"}, mi, Plan{
		Container: "mkv", Subs: SubPlan{KeepLangs: []string{"eng"}},
	}, false, 0, false), " ")

	if !strings.Contains(args, "-map 0:s:2") {
		t.Errorf("the English track (input sub 2) wasn't mapped:\n%s", args)
	}
	for _, unwanted := range []string{"-map 0:s:0", "-map 0:s:1", "-map 0:s?"} {
		if strings.Contains(args, unwanted) {
			t.Errorf("%q present — a filtered-out track was mapped:\n%s", unwanted, args)
		}
	}
	// It lands at output position 0, so the mov_text→srt override must say :0, not :2.
	if !strings.Contains(args, "-c:s:0 srt") {
		t.Errorf("expected the override at output index 0:\n%s", args)
	}
	if strings.Contains(args, "-c:s:2 srt") {
		t.Errorf("override used the INPUT index — it would aim at a stream that isn't there:\n%s", args)
	}
}

// The remux plan must not re-encode anything: that's the whole point for a file already
// in the target codec.
func TestRemuxPlanCopiesTheVideo(t *testing.T) {
	mi := subs(SubStream{SubIndex: 0, Codec: "subrip", Lang: "eng", Text: true})
	args := strings.Join(compileOutputArgs(Encoder{Codec: "hevc", Name: "libx265", Kind: "cpu"}, mi, Plan{Container: "mkv"}, false, 0, false), " ")
	if !strings.Contains(args, "-c:v copy") {
		t.Errorf("a plan with no video codec must copy the video:\n%s", args)
	}
}

// A remux rewrites the file and replaces the original, so a sweep has to skip files that
// are already exactly what was asked for — otherwise "clean up this show" churns every
// episode to produce identical output.
func TestNeedsTrackCleanup(t *testing.T) {
	keepEN := Plan{Subs: SubPlan{KeepLangs: []string{"en"}}}

	cluttered := subs(
		SubStream{SubIndex: 0, Codec: "subrip", Lang: "eng", Text: true},
		SubStream{SubIndex: 1, Codec: "subrip", Lang: "fre", Text: true},
	)
	if !NeedsTrackCleanup(cluttered, keepEN) {
		t.Error("a file with a French track to drop was reported as clean")
	}

	clean := subs(SubStream{SubIndex: 0, Codec: "subrip", Lang: "eng", Text: true})
	if NeedsTrackCleanup(clean, keepEN) {
		t.Error("a file that already has only English was queued for a pointless rewrite")
	}

	// No filter set: nothing would be dropped, so nothing needs doing.
	if NeedsTrackCleanup(cluttered, Plan{}) {
		t.Error("with no languages configured, a rewrite was proposed anyway")
	}

	// The never-strip-everything rule means a file with no English isn't "cleanable" —
	// keptSubs would hand every track back, so a rewrite would change nothing.
	noEnglish := subs(
		SubStream{SubIndex: 0, Codec: "subrip", Lang: "chi", Text: true},
		SubStream{SubIndex: 1, Codec: "subrip", Lang: "jpn", Text: true},
	)
	if NeedsTrackCleanup(noEnglish, keepEN) {
		t.Error("a file with no English was queued, but the filter would keep every track anyway")
	}

	// Audio counts too.
	audio := &MediaInfo{VideoCodec: "hevc", Audio: []AudioStream{
		{AudIndex: 0, Codec: "eac3", Lang: "eng"}, {AudIndex: 1, Codec: "ac3", Lang: "spa"},
	}}
	if !NeedsTrackCleanup(audio, Plan{Audio: AudioPlan{KeepLangs: []string{"en"}}}) {
		t.Error("a Spanish dub to drop was reported as clean")
	}
}
