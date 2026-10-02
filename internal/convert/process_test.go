package convert

import (
	"strings"
	"testing"
)

// The check before a converted file may replace the original: the whole film, every track
// the plan keeps, and a saving worth a generation of compression.
func TestVerifyOutput(t *testing.T) {
	src := withSubs(film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6), aud("ac3", "fre", 6)), srt("eng"), srt("ger"))
	plan := Plan{VideoCodec: "hevc", Audio: AudioPlan{KeepLangs: []string{"en"}}}
	good := &MediaInfo{VideoCodec: "hevc", DurationSec: 7200.4, AudioTracks: 1, SubTracks: 2}
	half := src.SizeBytes / 2

	if kind, reason := verifyOutput(src, good, half, plan); reason != "" {
		t.Fatalf("a good encode was refused: %s %s", kind, reason)
	}
	cases := []struct {
		name     string
		out      MediaInfo
		size     int64
		kind     string
		contains string
	}{
		{"ten minutes short", MediaInfo{VideoCodec: "hevc", DurationSec: 6600, AudioTracks: 1, SubTracks: 2}, half, "", "runs 1:50:00 instead of 2:00:00"},
		{"lost the audio", MediaInfo{VideoCodec: "hevc", DurationSec: 7200, AudioTracks: 0, SubTracks: 2}, half, "", "0 audio"},
		{"lost a subtitle", MediaInfo{VideoCodec: "hevc", DurationSec: 7200, AudioTracks: 1, SubTracks: 1}, half, "", "1 subtitle"},
		{"no video", MediaInfo{DurationSec: 7200, AudioTracks: 1, SubTracks: 2}, half, "", "no video"},
		{"only 10% smaller", *good, src.SizeBytes * 9 / 10, SkipNotSmaller, "only 10% smaller"},
	}
	for _, c := range cases {
		out := c.out
		kind, reason := verifyOutput(src, &out, c.size, plan)
		if reason == "" || kind != c.kind || !strings.Contains(reason, c.contains) {
			t.Errorf("%s: got (%q, %q), want kind %q containing %q", c.name, kind, reason, c.kind, c.contains)
		}
	}
	// A track tidy-up keeps the video, so it saves little — that's fine, as long as it
	// doesn't grow.
	copyPlan := Plan{Audio: plan.Audio}
	if _, reason := verifyOutput(src, good, src.SizeBytes*99/100, copyPlan); reason != "" {
		t.Errorf("a track tidy-up that saved 1%% was refused: %s", reason)
	}
}

// What can be carried through a conversion, by HDR format, codec and encoder.
func TestCanPreserveHDR(t *testing.T) {
	s := newTestService(t)
	s.hdr10plusTool = "/usr/local/bin/hdr10plus_tool"
	cpuHEVC, cpuAV1 := cpuEncoder("hevc"), cpuEncoder("av1")
	gpu := Encoder{Codec: "hevc", Name: "hevc_vaapi", Kind: "vaapi", Hardware: true}
	mk := func(codec, hdr string) *MediaInfo {
		mi := film(codec, 3840, 2160, 60000)
		mi.HDR = hdr
		return mi
	}
	dv := mk("hevc", "Dolby Vision")
	dv.DVProfile, dv.DVBase = 8, "HDR10"
	dv5 := mk("hevc", "Dolby Vision")
	dv5.DVProfile, dv5.DVBase = 5, "SDR"

	cases := []struct {
		name string
		mi   *MediaInfo
		plan Plan
		enc  Encoder
		h10p bool
		want bool
	}{
		{"SDR on the GPU", mk("h264", "SDR"), Plan{VideoCodec: "hevc"}, gpu, false, true},
		{"HDR10 on the GPU", mk("h264", "HDR10"), Plan{VideoCodec: "hevc"}, gpu, false, false},
		{"HDR10 to HEVC", mk("hevc", "HDR10"), Plan{VideoCodec: "hevc"}, cpuHEVC, false, true},
		{"HDR10 to AV1", mk("hevc", "HDR10"), Plan{VideoCodec: "av1"}, cpuAV1, false, true},
		{"HLG to AV1", mk("hevc", "HLG"), Plan{VideoCodec: "av1"}, cpuAV1, false, true},
		{"HDR10+ to HEVC", mk("hevc", "HDR10"), Plan{VideoCodec: "hevc"}, cpuHEVC, true, true},
		{"HDR10+ to AV1", mk("hevc", "HDR10"), Plan{VideoCodec: "av1"}, cpuAV1, true, false},
		{"Dolby Vision 8.1 keeps its HDR10 base", dv, Plan{VideoCodec: "av1"}, cpuAV1, false, true},
		{"Dolby Vision profile 5", dv5, Plan{VideoCodec: "hevc"}, cpuHEVC, false, false},
		{"a copy always keeps everything", dv5, Plan{}, gpu, false, true},
	}
	for _, c := range cases {
		if got := s.canPreserveHDR(c.mi, c.plan, c.enc, c.h10p); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// Without the tool, a PQ grade in an HEVC stream might hide HDR10+: don't risk it.
	s.hdr10plusTool = ""
	if s.canPreserveHDR(mk("hevc", "HDR10"), Plan{VideoCodec: "hevc"}, cpuHEVC, false) {
		t.Error("HEVC HDR10 without hdr10plus_tool must not convert (HDR10+ can't be ruled out)")
	}
	if !s.canPreserveHDR(mk("h264", "HDR10"), Plan{VideoCodec: "hevc"}, cpuHEVC, false) {
		t.Error("H.264 can't carry HDR10+, so HDR10 from H.264 is fine without the tool")
	}
}

func TestEncoderChoice(t *testing.T) {
	s := newTestService(t)
	s.encoders = append(workingCPUEncoders(),
		Encoder{Codec: "hevc", Name: "hevc_vaapi", Kind: "vaapi", Label: "VAAPI (HEVC)", Hardware: true, Available: true})
	sdr := film("h264", 1920, 1080, 12000)
	hdr := film("hevc", 3840, 2160, 60000)
	hdr.HDR = "HDR10"
	if e := s.encoderChoice(sdr, "hevc", false); e.Kind != "cpu" {
		t.Errorf("GPU off: %s, want the CPU", e.Name)
	}
	if e := s.encoderChoice(sdr, "hevc", true); e.Name != "hevc_vaapi" {
		t.Errorf("GPU on: %s, want hevc_vaapi", e.Name)
	}
	if e := s.encoderChoice(hdr, "hevc", true); e.Kind != "cpu" {
		t.Errorf("HDR must go to the CPU even in GPU mode, got %s", e.Name)
	}
	if e := s.encoderChoice(sdr, "av1", true); e.Kind != "cpu" {
		t.Errorf("no hardware AV1: %s, want SVT-AV1", e.Name)
	}
	s.markHardwareBroken("hevc_vaapi", "x")
	s.markHardwareBroken("hevc_vaapi", "x")
	if e := s.encoderChoice(sdr, "hevc", true); e.Kind != "cpu" {
		t.Errorf("a broken GPU encoder must be skipped, got %s", e.Name)
	}
}

// AV1 has to beat HEVC clearly — smaller by 5%+ and as good — or HEVC wins.
func TestPickCodec(t *testing.T) {
	hevc := TrialSide{Bytes: 1000, SSIM: 0.985}
	for name, c := range map[string]struct {
		av1  TrialSide
		want string
	}{
		"AV1 20% smaller, same quality":   {TrialSide{Bytes: 800, SSIM: 0.985}, "av1"},
		"AV1 only 3% smaller":             {TrialSide{Bytes: 970, SSIM: 0.986}, "hevc"},
		"AV1 smaller but visibly worse":   {TrialSide{Bytes: 700, SSIM: 0.980}, "hevc"},
		"AV1 bigger":                      {TrialSide{Bytes: 1100, SSIM: 0.990}, "hevc"},
		"AV1 smaller, a hair lower score": {TrialSide{Bytes: 800, SSIM: 0.9835}, "av1"},
		"AV1 failed to encode":            {TrialSide{}, "hevc"},
	} {
		if got, why := pickCodec(hevc, c.av1); got != c.want {
			t.Errorf("%s: picked %s (%s), want %s", name, got, why, c.want)
		}
	}
}

func TestTrialClips(t *testing.T) {
	if c := trialClips(7200); len(c) != 3 || c[0].start != 1440 || c[2].dur != 10 {
		t.Errorf("a film gets three 10s clips at 20/50/80%%: %+v", c)
	}
	if c := trialClips(90); len(c) != 1 || c[0].start != 0 || c[0].dur != 60 {
		t.Errorf("a short file is tested from the start, up to a minute: %+v", c)
	}
	if trialClips(0) != nil {
		t.Error("unknown length: nothing to test")
	}
}

// A quality retry must actually raise the quality — and say so when it can't, rather than
// re-running an identical day-long encode.
func TestHigherQuality(t *testing.T) {
	cases := []struct {
		codec    string
		q, want  int
		improved bool
	}{
		{"hevc", 18, 16, true},
		{"hevc", 16, 14, true},
		{"hevc", 15, 14, true},
		{"hevc", 14, 14, false},
		{"av1", 22, 20, true},
		{"av1", 16, 16, false},
	}
	for _, c := range cases {
		if got, ok := higherQuality(c.codec, c.q); got != c.want || ok != c.improved {
			t.Errorf("higherQuality(%s, %d) = %d, %v; want %d, %v", c.codec, c.q, got, ok, c.want, c.improved)
		}
	}
}

// The size cap stops a re-encode that can't save enough; a track tidy-up has none. Scratch
// space follows it.
func TestSizeCapAndScratch(t *testing.T) {
	mi := film("hevc", 3840, 2160, 70000, aud("truehd", "eng", 8))
	reencode := Plan{VideoCodec: "hevc"}
	if got, want := sizeCap(mi, reencode), mi.SizeBytes*80/100; got != want {
		t.Errorf("size cap = %d, want %d (80%% of the source)", got, want)
	}
	if sizeCap(mi, Plan{}) != 0 {
		t.Error("a track tidy-up copies the video and must not be capped")
	}
	if n := scratchNeeded(mi, reencode, true); n < 2*sizeCap(mi, reencode) {
		t.Errorf("HDR10+ scratch %d must hold the stream and its injected copy", n)
	}
	if n := scratchNeeded(mi, Plan{}, false); n < mi.SizeBytes {
		t.Errorf("tidy-up scratch %d must hold a whole copy", n)
	}
}

// A re-encode must not carry the original's Matroska statistics (bitrate, byte count) on
// its new video track, and must be capped; a copy keeps both untouched.
func TestReencodeArgsClearStatsAndCap(t *testing.T) {
	mi := film("h264", 1920, 1080, 30000, aud("ac3", "eng", 6))
	args := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, Plan{VideoCodec: "hevc", Quality: 20}, false, 4, false), " ")
	for _, want := range []string{"-metadata:s:v:0 BPS=", "-metadata:s:v:0 NUMBER_OF_BYTES=", "-metadata:s:v:0 BPS-eng="} {
		if !strings.Contains(args, want) {
			t.Errorf("re-encode args lack %q: %s", want, args)
		}
	}
	if copyArgs := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, Plan{}, false, 4, false), " "); strings.Contains(copyArgs, "BPS=") {
		t.Errorf("a copied video's stats are still true and must be kept: %s", copyArgs)
	}
}
