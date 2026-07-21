package convert

import (
	"strconv"
	"strings"
	"testing"
)

// ---- B1: Matroska cannot mux mov_text/tx3g ----

// An MKV target must transcode MP4-family text subs to SRT (Matroska has no mapping for
// mov_text — `-c:s copy` fails at header write), while PGS and native text subs still copy.
func TestMKVTranscodesMovTextSubs(t *testing.T) {
	mi := &MediaInfo{
		VideoCodec: "h264", Height: 1080,
		Subs: []SubStream{
			{SubIndex: 0, Codec: "mov_text", Lang: "eng", Text: true},
			{SubIndex: 1, Codec: "hdmv_pgs_subtitle", Lang: "eng"},
			{SubIndex: 2, Codec: "subrip", Lang: "eng", Text: true},
		},
	}
	args := compileOutputArgs(cpuEncoder("hevc"), mi, mkvPlan(), false, 4, false)
	joined := strings.Join(args, " ")

	if !argsHas(args, "-c:s", "copy") {
		t.Errorf("subs must default to copy: %s", joined)
	}
	if !argsHas(args, "-c:s:0", "srt") {
		t.Errorf("mov_text must be transcoded to srt for MKV: %s", joined)
	}
	if strings.Contains(joined, "-c:s:1 srt") || strings.Contains(joined, "-c:s:2 srt") {
		t.Errorf("PGS and subrip must stay copied: %s", joined)
	}
}

// ---- B5: safe-mode retry must not strip HDR metadata ----

func TestStripTuningKeysKeepsHDRAndPools(t *testing.T) {
	in := "aq-mode=3:psy-rd=2.0:psy-rdoq=1.0:no-sao=1:bframes=8:rc-lookahead=40:pools=none:" +
		"hdr10=1:repeat-headers=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:" +
		"master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400"
	got := stripTuningKeys(in)
	for _, want := range []string{"hdr10=1", "repeat-headers=1", "colorprim=bt2020",
		"transfer=smpte2084", "colormatrix=bt2020nc", "master-display=G(13250,", "max-cll=1000,400", "pools=none"} {
		if !strings.Contains(got, want) {
			t.Errorf("stripTuningKeys dropped %q, which must survive: %s", want, got)
		}
	}
	for _, gone := range []string{"aq-mode", "psy-rd", "no-sao", "bframes", "rc-lookahead"} {
		if strings.Contains(got, gone) {
			t.Errorf("stripTuningKeys kept tuning key %q: %s", gone, got)
		}
	}
	// SVT-AV1 spellings survive too.
	if got := stripTuningKeys("tune=0:lp=8:mastering-display=G(0.2650,0.6900):content-light=1000,400"); !strings.Contains(got, "mastering-display=") || !strings.Contains(got, "content-light=") || strings.Contains(got, "tune=") {
		t.Errorf("SVT keys mishandled: %s", got)
	}
	// Pure tuning strips to empty.
	if got := stripTuningKeys("aq-mode=3:psy-rd=2.0"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// The args-level strip keeps the -x265-params flag when it still carries HDR/pools keys, and
// drops the pair only when nothing but tuning was in it.
func TestStripTuningParamsPreservesHDRPortion(t *testing.T) {
	args := []string{"-c:v", "libx265", "-preset", "slow", "-crf", "18",
		"-x265-params", "aq-mode=3:hdr10=1:transfer=smpte2084", "out.mkv"}
	joined := strings.Join(stripTuningParams(args), " ")
	if !strings.Contains(joined, "-x265-params hdr10=1:transfer=smpte2084") {
		t.Errorf("HDR portion must survive safe mode: %s", joined)
	}
	if strings.Contains(joined, "aq-mode") {
		t.Errorf("tuning must be gone: %s", joined)
	}
}

// ---- B7: the encoder itself must respect the core budget ----

func TestX265PoolsBoundsCoreBudget(t *testing.T) {
	mi := &MediaInfo{VideoCodec: "h264", Height: 1080}
	got := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, mkvPlan(), false, 6, false), " ")
	if !strings.Contains(got, "pools=6") {
		t.Errorf("x265 must be pool-bounded to the core budget (-threads only bounds the decoder): %s", got)
	}
	// The NUMA workaround takes precedence — pools=none, not a numbered pool.
	blocked := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, mkvPlan(), false, 6, true), " ")
	if !strings.Contains(blocked, "pools=none") || strings.Contains(blocked, "pools=6") {
		t.Errorf("NUMA-blocked encodes must use pools=none: %s", blocked)
	}
}

// x265 encodes at 10-bit even from 8-bit sources — Main10 avoids the banding the 8-bit path
// introduces in gradients.
func TestX265AlwaysTenBit(t *testing.T) {
	mi := &MediaInfo{VideoCodec: "h264", Height: 1080, TenBit: false}
	got := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, mkvPlan(), false, 4, false), " ")
	if !strings.Contains(got, "-pix_fmt yuv420p10le") {
		t.Errorf("8-bit sources must still encode 10-bit on x265: %s", got)
	}
}

// ---- B11: NVENC constant quality must not be capped by the default average bitrate ----

func TestNVENCUncapsBitrate(t *testing.T) {
	mi := &MediaInfo{VideoCodec: "h264", Height: 1080}
	enc := Encoder{Codec: "hevc", Name: "hevc_nvenc", Kind: "nvenc", Hardware: true}
	args := compileOutputArgs(enc, mi, mkvPlan(), false, 4, false)
	if !argsHas(args, "-b:v", "0") {
		t.Errorf("-b:v 0 must accompany -rc vbr -cq, or the 2Mb/s default caps quality: %s", strings.Join(args, " "))
	}
}

// ---- B14: av1_qsv takes ICQ (1-51), not the 0-255 AV1 qindex ----

func TestQSVAV1QualityIsICQScale(t *testing.T) {
	mi := &MediaInfo{VideoCodec: "h264", Height: 1080}
	enc := Encoder{Codec: "av1", Name: "av1_qsv", Kind: "qsv", Hardware: true}
	args := compileOutputArgs(enc, mi, Plan{VideoCodec: "av1", Quality: 24, Container: "mkv"}, false, 4, false)
	// CRF 24 → hardware-tightened 20, NOT rescaled to qindex 80 (which clamps to worst).
	if !argsHas(args, "-global_quality", "20") {
		t.Errorf("av1_qsv wants ICQ 1-51, got: %s", strings.Join(args, " "))
	}
	// VAAPI AV1 genuinely is 0-255 and keeps the rescale.
	vaapi := Encoder{Codec: "av1", Name: "av1_vaapi", Kind: "vaapi", Hardware: true}
	vargs := compileOutputArgs(vaapi, mi, Plan{VideoCodec: "av1", Quality: 24, Container: "mkv"}, false, 4, false)
	if !argsHas(vargs, "-global_quality", "80") {
		t.Errorf("av1_vaapi keeps the qindex rescale, got: %s", strings.Join(vargs, " "))
	}
}

// ---- B10: VideoToolbox honors the plan's quality and 10-bit sources ----

func TestVideoToolboxQuality(t *testing.T) {
	for _, c := range []struct{ crf, want int }{{24, 52}, {20, 60}, {18, 64}, {0, 52} /* default CRF 24 */, {60, 1}} {
		plan := Plan{VideoCodec: "hevc", Quality: c.crf, Container: "mkv"}
		mi := &MediaInfo{VideoCodec: "h264", Height: 1080}
		enc := Encoder{Codec: "hevc", Name: "hevc_videotoolbox", Kind: "videotoolbox", Hardware: true}
		args := compileOutputArgs(enc, mi, plan, false, 4, false)
		if !argsHas(args, "-q:v", strconv.Itoa(c.want)) {
			t.Errorf("crf %d: want -q:v %d in: %s", c.crf, c.want, strings.Join(args, " "))
		}
	}
	// 10-bit source → main10 + p010le.
	mi := &MediaInfo{VideoCodec: "hevc", Height: 2160, TenBit: true}
	enc := Encoder{Codec: "hevc", Name: "hevc_videotoolbox", Kind: "videotoolbox", Hardware: true}
	joined := strings.Join(compileOutputArgs(enc, mi, mkvPlan(), false, 4, false), " ")
	if !strings.Contains(joined, "-profile:v main10") || !strings.Contains(joined, "-pix_fmt p010le") {
		t.Errorf("10-bit source must request 10-bit VT output: %s", joined)
	}
}

// ---- B15: SVT-AV1 mastering display uses floats ----

func TestSVTMasterDisplayFloats(t *testing.T) {
	in := "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1)"
	want := "G(0.2650,0.6900)B(0.1500,0.0600)R(0.6800,0.3200)WP(0.3127,0.3290)L(1000.0000,0.0001)"
	if got := svtMasterDisplay(in); got != want {
		t.Errorf("svtMasterDisplay:\n got %s\nwant %s", got, want)
	}
	// Unparseable input passes through unmangled.
	if got := svtMasterDisplay("garbage"); got != "garbage" {
		t.Errorf("garbage in should be garbage out unchanged, got %s", got)
	}
}

func TestAV1HDRParamsUsesFloatMasteringDisplay(t *testing.T) {
	mi := &MediaInfo{HDR: "HDR10", HDR10: &HDR10Meta{
		MasterDisplay: "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1)",
		MaxCLL:        "1000,400",
	}}
	params, _ := av1HDRParams(mi)
	if !strings.Contains(params, "mastering-display=G(0.2650,0.6900)") {
		t.Errorf("SVT params must carry the float form: %s", params)
	}
	if !strings.Contains(params, "content-light=1000,400") {
		t.Errorf("content light missing: %s", params)
	}
}

// ---- B16/B9: silent degradations become warnings ----

func TestPlanWarnings(t *testing.T) {
	mi := &MediaInfo{
		VideoCodec: "h264", Height: 1080, HasCC: true,
		Audio: []AudioStream{
			{AudIndex: 0, Codec: "truehd", Lang: "eng", Channels: 8},
			{AudIndex: 1, Codec: "aac", Lang: "eng", Channels: 2},
		},
		Subs: []SubStream{
			{SubIndex: 0, Codec: "hdmv_pgs_subtitle", Lang: "eng"},
			{SubIndex: 1, Codec: "ass", Lang: "eng", Text: true},
		},
	}
	w := planWarnings(mi, Plan{VideoCodec: "hevc", Container: "mp4"})
	joined := strings.Join(w, " | ")
	for _, want := range []string{"lossless TRUEHD", "AAC 256k", "image subtitle", "ASS/SSA", "closed captions"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing warning about %q: %s", want, joined)
		}
	}
	if len(w) != 4 {
		t.Errorf("expected 4 warnings, got %d: %s", len(w), joined)
	}
	// The compliant AAC track must not be warned about.
	if strings.Contains(joined, "AAC 2.0") {
		t.Errorf("AAC track needs no warning: %s", joined)
	}
	// MKV target loses nothing container-wise; only the CC note (video is re-encoded) remains.
	w = planWarnings(mi, Plan{VideoCodec: "hevc", Container: "mkv"})
	if len(w) != 1 || !strings.Contains(w[0], "closed captions") {
		t.Errorf("MKV should only warn about CC: %v", w)
	}
	// Remux (no video transcode) keeps CC — no warnings at all.
	if w = planWarnings(mi, Plan{Container: "mkv"}); len(w) != 0 {
		t.Errorf("remux should warn about nothing: %v", w)
	}
}

// ---- B17: the real video stream is mapped, not cover art ----

func TestVideoIndexMapping(t *testing.T) {
	mi := &MediaInfo{VideoCodec: "h264", VideoIndex: 1, Height: 1080}
	args := compileOutputArgs(cpuEncoder("hevc"), mi, mkvPlan(), false, 4, false)
	if !argsHas(args, "-map", "0:v:1") {
		t.Errorf("must map the real video stream index: %s", strings.Join(args, " "))
	}
}

// ---- B12: VFR detection must not trip on field rates ----

func TestDetectVFR(t *testing.T) {
	cases := []struct {
		name       string
		r, avg     float64
		interlaced bool
		want       bool
	}{
		{"CFR film", 23.976, 23.976, false, false},
		{"true VFR", 60, 24, false, true},
		{"field rate on interlaced is CFR", 59.94, 29.97, true, false},
		{"2x on progressive is real VFR", 59.94, 29.97, false, true},
		{"incoherent avg is not evidence", 23.976, 0, false, false},
		{"no rates at all", 0, 0, false, false},
		{"interlaced but not a 2x ratio", 60, 20, true, true},
	}
	for _, c := range cases {
		if got := detectVFR(c.r, c.avg, c.interlaced); got != c.want {
			t.Errorf("%s: detectVFR(%v, %v, %v) = %v, want %v", c.name, c.r, c.avg, c.interlaced, got, c.want)
		}
	}
}

// ---- B8: interlaced content gets deinterlaced on every path ----

func TestInterlacedAddsDeinterlaceFilter(t *testing.T) {
	mi := &MediaInfo{VideoCodec: "mpeg2video", Height: 576, Interlaced: true}

	cpu := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, mkvPlan(), false, 4, false), " ")
	if !strings.Contains(cpu, "bwdif=mode=send_frame") {
		t.Errorf("CPU path must deinterlace: %s", cpu)
	}

	nv := Encoder{Codec: "hevc", Name: "hevc_nvenc", Kind: "nvenc", Hardware: true}
	nvenc := strings.Join(compileOutputArgs(nv, mi, mkvPlan(), false, 4, false), " ")
	if !strings.Contains(nvenc, "bwdif=mode=send_frame") {
		t.Errorf("NVENC path (software frames) must deinterlace: %s", nvenc)
	}

	va := Encoder{Codec: "hevc", Name: "hevc_vaapi", Kind: "vaapi", Hardware: true}
	hw := strings.Join(compileOutputArgs(va, mi, mkvPlan(), true, 4, false), " ")
	if !strings.Contains(hw, "deinterlace_vaapi") {
		t.Errorf("full-GPU VAAPI must deinterlace on the GPU: %s", hw)
	}
	sw := strings.Join(compileOutputArgs(va, mi, mkvPlan(), false, 4, false), " ")
	if !strings.Contains(sw, "bwdif=mode=send_frame,format=") {
		t.Errorf("VAAPI software-decode leg must deinterlace before upload: %s", sw)
	}

	// Progressive content must NOT be filtered.
	prog := &MediaInfo{VideoCodec: "h264", Height: 1080}
	clean := strings.Join(compileOutputArgs(cpuEncoder("hevc"), prog, mkvPlan(), false, 4, false), " ")
	if strings.Contains(clean, "bwdif") {
		t.Errorf("progressive content must not be deinterlaced: %s", clean)
	}
}

// ---- B18: bibliographic and terminological ISO-639-2 codes both match ----

func TestLanguageCodeVariants(t *testing.T) {
	cases := []struct {
		tag    string
		wanted string
	}{
		{"fra", "fr"}, {"fre", "fr"}, {"fre", "fra"}, {"fra", "fre"},
		{"deu", "ger"}, {"ger", "de"}, {"nld", "dut"}, {"zho", "chi"}, {"chi", "zh"},
		{"eng", "en"}, {"ces", "cze"},
	}
	for _, c := range cases {
		if !langIn(c.tag, []string{c.wanted}) {
			t.Errorf("track %q should match wanted %q", c.tag, c.wanted)
		}
	}
	if langIn("fre", []string{"eng"}) {
		t.Error("French must still not match an English filter")
	}
}

// ---- improvement: hardware encodes re-assert the source's colour tags ----

func TestHardwareEncodeAssertsColourTags(t *testing.T) {
	mi := &MediaInfo{VideoCodec: "h264", Height: 1080,
		ColorPrimaries: "bt709", ColorTransfer: "bt709", ColorSpace: "bt709"}
	nv := Encoder{Codec: "hevc", Name: "hevc_nvenc", Kind: "nvenc", Hardware: true}
	joined := strings.Join(compileOutputArgs(nv, mi, mkvPlan(), false, 4, false), " ")
	for _, want := range []string{"-color_primaries bt709", "-color_trc bt709", "-colorspace bt709"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q on hardware encode: %s", want, joined)
		}
	}
	// Untagged sources must not get invented tags.
	untagged := &MediaInfo{VideoCodec: "h264", Height: 1080, ColorTransfer: "unknown"}
	joined = strings.Join(compileOutputArgs(nv, untagged, mkvPlan(), false, 4, false), " ")
	if strings.Contains(joined, "-color_") || strings.Contains(joined, "-colorspace") {
		t.Errorf("untagged source must stay untagged: %s", joined)
	}
}
