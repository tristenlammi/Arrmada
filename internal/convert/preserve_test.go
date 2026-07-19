package convert

import (
	"strings"
	"testing"
)

// argsHas reports whether the flag/value pair appears adjacently in args.
func argsHas(args []string, flag, val string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

func mkvPlan() Plan {
	return Plan{VideoCodec: "hevc", Quality: 20, VFRToCFR: true, Container: "mkv"}
}

// The module exists to change the video codec and nothing else. Once ANY -map is passed,
// ffmpeg stops selecting streams by default, so every stream type has to be mapped
// explicitly — attachments (embedded fonts) were silently dropped from every single
// conversion, which destroys ASS/SSA subtitle typesetting.
func TestCompileKeepsAttachmentsAndStreams(t *testing.T) {
	mi := &MediaInfo{
		VideoCodec: "h264", Height: 1080, SizeBytes: 1 << 30,
		Audio: []AudioStream{{AudIndex: 0, Codec: "truehd", Lang: "eng", Channels: 8}},
		Subs:  []SubStream{{Codec: "ass", Lang: "eng"}},
	}
	args := compileOutputArgs(cpuEncoder("hevc"), mi, mkvPlan(), false)

	for _, want := range [][2]string{
		{"-map", "0:t?"}, // attachments: embedded fonts
		{"-map", "0:s?"}, // subtitles
		{"-c:t", "copy"},
		{"-c:s", "copy"},
	} {
		if !argsHas(args, want[0], want[1]) {
			t.Errorf("missing %s %s in: %s", want[0], want[1], strings.Join(args, " "))
		}
	}
}

// Lossless / object-based audio must survive untouched. Loudness normalization re-encodes
// to AAC, which would silently destroy Atmos/TrueHD/DTS-HD — the exact thing the module is
// supposed to preserve.
func TestLoudnormNeverTouchesLosslessAudio(t *testing.T) {
	plan := mkvPlan()
	plan.Audio.Loudnorm = true
	mi := &MediaInfo{
		VideoCodec: "h264", Height: 1080,
		Audio: []AudioStream{
			{AudIndex: 0, Codec: "truehd", Lang: "eng", Channels: 8}, // Atmos — must be copied
			{AudIndex: 1, Codec: "ac3", Lang: "eng", Channels: 6},    // lossy — may be normalized
		},
	}
	args := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, plan, false), " ")

	if !strings.Contains(args, "-c:a:0 copy") {
		t.Errorf("TrueHD track must be copied, got: %s", args)
	}
	if strings.Contains(args, "-filter:a:0") {
		t.Errorf("TrueHD track must not be loudnormed, got: %s", args)
	}
	if !strings.Contains(args, "-filter:a:1") {
		t.Errorf("lossy track should still be normalized when asked, got: %s", args)
	}
}

// A language filter should never discard a track whose language is unknown — untagged
// original-language and commentary tracks were being dropped whenever any other track
// matched.
func TestUntaggedAudioSurvivesLanguageFilter(t *testing.T) {
	if !langIn("", []string{"eng"}) {
		t.Error("untagged audio must be kept")
	}
	if !langIn("und", []string{"eng"}) {
		t.Error("explicitly-unknown audio must be kept")
	}
	if langIn("fre", []string{"eng"}) {
		t.Error("a tagged non-matching language should still be filtered out")
	}
	if !langIn("en", []string{"eng"}) {
		t.Error("2- and 3-letter codes must match")
	}
}
