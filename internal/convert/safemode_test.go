package convert

import (
	"strings"
	"testing"
)

// The safe-mode retry drops the tuning parameters and keeps everything else — the stream
// mapping, the codec, the preset and the quality target all have to survive, or the retry
// would produce a different file rather than the same one encoded more conservatively.
func TestStripTuningParamsKeepsEverythingElse(t *testing.T) {
	args := []string{
		"-y", "-i", "in.mkv",
		"-map", "0:v:0", "-map", "0:a?", "-c:a", "copy", "-map", "0:t?", "-c:t", "copy",
		"-c:v", "libx265", "-preset", "slow", "-crf", "18",
		"-x265-params", "aq-mode=3:psy-rd=2.0",
		"-map_metadata", "0", "out.mkv",
	}
	got := stripTuningParams(args)
	joined := strings.Join(got, " ")

	if strings.Contains(joined, "x265-params") || strings.Contains(joined, "aq-mode") {
		t.Errorf("tuning params should be gone: %s", joined)
	}
	for _, want := range []string{
		"-c:v libx265", "-preset slow", "-crf 18",
		"-map 0:t?", "-c:t copy", "-c:a copy", "-map_metadata 0", "out.mkv",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("safe mode dropped %q, which it must keep: %s", want, joined)
		}
	}
}

// With nothing to strip, the args are unchanged — the caller uses that to tell that the
// failure was something other than the tuning, and skips a pointless identical retry.
func TestStripTuningParamsNoopWhenAbsent(t *testing.T) {
	args := []string{"-i", "in.mkv", "-c:v", "libx265", "-crf", "18", "out.mkv"}
	if got := stripTuningParams(args); len(got) != len(args) {
		t.Errorf("expected no change, got %v", got)
	}
}

// SVT-AV1's params are stripped the same way.
func TestStripTuningParamsHandlesAV1(t *testing.T) {
	args := []string{"-c:v", "libsvtav1", "-preset", "5", "-svtav1-params", "tune=0:lp=8", "out.mkv"}
	joined := strings.Join(stripTuningParams(args), " ")
	if strings.Contains(joined, "svtav1-params") || strings.Contains(joined, "tune=0") {
		t.Errorf("svtav1 params should be gone: %s", joined)
	}
	if !strings.Contains(joined, "-preset 5") {
		t.Errorf("preset must survive: %s", joined)
	}
}

// When NUMA pool binding is denied, x265 must be told not to use pools at all — the
// default behaviour logs a warning per pool and can crash partway into the encode.
func TestNoNumaPoolsAddsPoolsNone(t *testing.T) {
	mi := &MediaInfo{VideoCodec: "h264", Height: 1080}

	blocked := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, mkvPlan(), false, 4, true), " ")
	if !strings.Contains(blocked, "pools=none") {
		t.Errorf("expected pools=none when NUMA pools are blocked: %s", blocked)
	}

	// And it must NOT appear otherwise — unpooled costs threading efficiency, so it's
	// only worth paying when the environment actually requires it.
	ok := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, mkvPlan(), false, 4, false), " ")
	if strings.Contains(ok, "pools=none") {
		t.Errorf("pools=none should only appear when needed: %s", ok)
	}
}

// AV1 quantizes on a 0-255 index while our quality targets are CRF-scale (0-63). Feeding a
// CRF number straight to a hardware AV1 encoder made it ignore the value and use its own
// default, producing files larger than the source — every AV1 conversion was then thrown
// away by the never-grow guard.
func TestAV1QIndexMapping(t *testing.T) {
	cases := []struct{ crf, want int }{
		{24, 96},  // the AV1 default
		{18, 72},  // the HEVC-style high-quality target
		{0, 1},    // never zero: 0 means lossless and would be enormous
		{63, 252}, // top of the CRF scale stays inside the qindex range
		{99, 255}, // clamped
	}
	for _, c := range cases {
		if got := av1QIndex(c.crf); got != c.want {
			t.Errorf("av1QIndex(%d) = %d, want %d", c.crf, got, c.want)
		}
	}
}

// The AV1 hardware path must use -global_quality (av1_vaapi has no -qp), while HEVC keeps
// -qp on its own 0-52 scale.
func TestVAAPIQualityOptionPerCodec(t *testing.T) {
	mi := &MediaInfo{VideoCodec: "h264", Height: 1080}
	vaapi := Encoder{Codec: "av1", Name: "av1_vaapi", Kind: "vaapi", Hardware: true}

	// CRF 24 -> tightened to 20 for hardware -> qindex 80.
	av1 := strings.Join(compileOutputArgs(vaapi, mi, Plan{VideoCodec: "av1", Quality: 24, Container: "mkv"}, false, 4, false), " ")
	if !strings.Contains(av1, "-global_quality 80") {
		t.Errorf("AV1 VAAPI should use the tightened, rescaled global_quality: %s", av1)
	}
	if strings.Contains(av1, "-qp ") {
		t.Errorf("av1_vaapi has no -qp option; it must not be passed: %s", av1)
	}

	hevcEnc := Encoder{Codec: "hevc", Name: "hevc_vaapi", Kind: "vaapi", Hardware: true}
	hevc := strings.Join(compileOutputArgs(hevcEnc, mi, Plan{VideoCodec: "hevc", Quality: 24, Container: "mkv"}, false, 4, false), " ")
	if !strings.Contains(hevc, "-qp 20") {
		t.Errorf("HEVC VAAPI should tighten the CRF target for hardware: %s", hevc)
	}
}

// Hardware encoders need a tighter quantizer than software for the same picture, so the
// software CRF target is lowered before it reaches them. Without this, hardware output
// lands far softer than "maximum quality retention" implies — the first real conversion
// cut a 1080p episode from 12.1 to 2.1 Mb/s.
func TestHardwareQualityIsTightenedAndClamped(t *testing.T) {
	cases := []struct{ crf, want int }{
		{24, 20}, // the AV1 default
		{18, 14}, // an already-high target goes higher still
		{4, 1},   // never below 1: 0 means lossless
		{1, 1},
	}
	for _, c := range cases {
		if got := hardwareQuality(c.crf); got != c.want {
			t.Errorf("hardwareQuality(%d) = %d, want %d", c.crf, got, c.want)
		}
	}
	// The CPU path must be untouched — it doesn't need the correction.
	mi := &MediaInfo{VideoCodec: "h264", Height: 1080}
	cpu := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, Plan{VideoCodec: "hevc", Quality: 24, Container: "mkv"}, false, 4, false), " ")
	if !strings.Contains(cpu, "-crf 24") {
		t.Errorf("CPU encodes must use the target as given: %s", cpu)
	}
}
