package convert

import (
	"strings"
	"testing"
)

func joined(a []string) string { return strings.Join(a, " ") }

func argValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func TestX265Args(t *testing.T) {
	a := cpuVideoArgs("libx265", "hevc", 20, 8, "hdr10=1:master-display=X", false)
	got := joined(a)
	for _, want := range []string{"-preset slow", "-crf 20", "-pix_fmt yuv420p10le", "-dolbyvision 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("x265 args missing %q: %s", want, got)
		}
	}
	params := argValue(a, "-x265-params")
	for _, want := range []string{"aq-mode=3", "pools=8", "hdr10=1", "master-display=X"} {
		if !strings.Contains(params, want) {
			t.Errorf("x265 params missing %q: %s", want, params)
		}
	}
	if strings.Count(got, "-x265-params") != 1 {
		t.Error("HDR params must be merged into ONE -x265-params (ffmpeg keeps only the last)")
	}
	if p := argValue(cpuVideoArgs("libx265", "hevc", 20, 8, "", true), "-x265-params"); !strings.Contains(p, "pools=none") {
		t.Errorf("blocked NUMA pools must run unpooled: %s", p)
	}
}

// AV1's quality settings carry the dark-scene protection that fixes crushed blacks.
func TestSVTAV1Args(t *testing.T) {
	a := cpuVideoArgs("libsvtav1", "av1", 24, 6, "mastering-display=G(0.2650,0.6900)", false)
	got := joined(a)
	for _, want := range []string{"-preset 5", "-crf 24", "-pix_fmt yuv420p10le", "-dolbyvision 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("SVT-AV1 args missing %q: %s", want, got)
		}
	}
	params := argValue(a, "-svtav1-params")
	for _, want := range []string{"tune=0", "enable-variance-boost=1", "luminance-qp-bias=20", "lp=6", "mastering-display="} {
		if !strings.Contains(params, want) {
			t.Errorf("SVT-AV1 params missing %q: %s", want, params)
		}
	}
}

func TestStripTuningParams(t *testing.T) {
	args := []string{"-c:v", "libx265", "-x265-params", "aq-mode=3:psy-rd=2.0:pools=4:hdr10=1:master-display=X",
		"-c:a", "copy"}
	got := stripTuningParams(args)
	if p := argValue(got, "-x265-params"); p != "pools=4:hdr10=1:master-display=X" {
		t.Errorf("safe mode must keep HDR + pools, drop tuning: %q", p)
	}
	av1 := []string{"-svtav1-params", av1QualityParams + ":lp=4"}
	if p := argValue(stripTuningParams(av1), "-svtav1-params"); p != av1QualityParams+":lp=4" {
		t.Errorf("AV1's dark-scene protection must survive safe mode: %q", p)
	}
	hw := []string{"-c:v", "hevc_qsv", "-global_quality", "16", "-look_ahead_depth", "40", "-extbrc", "1", "-adaptive_i", "1"}
	if got := joined(stripTuningParams(hw)); got != "-c:v hevc_qsv -global_quality 16" {
		t.Errorf("hardware tuning flags must be stripped for the plain retry: %s", got)
	}
	plain := []string{"-c:v", "libx265", "-crf", "20"}
	if joined(stripTuningParams(plain)) != joined(plain) {
		t.Error("nothing to strip must leave the command unchanged")
	}
}

func TestHardwareQualityTargets(t *testing.T) {
	sdr := film("h264", 1920, 1080, 12000)
	vaapiHEVC := videoArgs(Encoder{Name: "hevc_vaapi", Kind: "vaapi"}, sdr, Plan{VideoCodec: "hevc", Quality: 20}, false, 4, false)
	if argValue(vaapiHEVC, "-qp") != "16" {
		t.Errorf("VAAPI HEVC qp = %q, want 16 (CRF 20 tightened by 4)", argValue(vaapiHEVC, "-qp"))
	}
	vaapiAV1 := videoArgs(Encoder{Name: "av1_vaapi", Kind: "vaapi"}, sdr, Plan{VideoCodec: "av1", Quality: 24}, false, 4, false)
	if argValue(vaapiAV1, "-global_quality") != "80" || argValue(vaapiAV1, "-qp") != "" {
		t.Errorf("VAAPI AV1 must use a 0-255 qindex via -global_quality: %v", vaapiAV1)
	}
	qsvAV1 := videoArgs(Encoder{Name: "av1_qsv", Kind: "qsv"}, sdr, Plan{VideoCodec: "av1", Quality: 24}, false, 4, false)
	if argValue(qsvAV1, "-global_quality") != "20" {
		t.Errorf("QSV quality is ICQ on a CRF-like scale for every codec: %v", qsvAV1)
	}
	nvenc := videoArgs(Encoder{Name: "hevc_nvenc", Kind: "nvenc"}, sdr, Plan{VideoCodec: "hevc", Quality: 20}, false, 4, false)
	if argValue(nvenc, "-b:v") != "0" {
		t.Error("NVENC must be uncapped (-b:v 0) or its 2 Mb/s default overrides the quality target")
	}
	if hardwareQuality(2) != 1 || av1QIndex(80) != 255 {
		t.Error("quality mappings must clamp")
	}
}

func TestHardwareEncodeAssertsColourTags(t *testing.T) {
	mi := film("h264", 1920, 1080, 12000)
	mi.ColorPrimaries, mi.ColorTransfer, mi.ColorSpace = "bt709", "bt709", "bt709"
	got := joined(videoArgs(Encoder{Name: "hevc_qsv", Kind: "qsv"}, mi, Plan{VideoCodec: "hevc"}, false, 4, false))
	if !strings.Contains(got, "-color_primaries bt709") || !strings.Contains(got, "-colorspace bt709") {
		t.Errorf("hardware encodes must re-assert colour tags: %s", got)
	}
}

func TestCompileMapsTheRealVideoAndDeinterlaces(t *testing.T) {
	mi := film("mpeg2video", 720, 576, 6000, aud("ac3", "eng", 2))
	mi.VideoIndex, mi.Interlaced = 1, true
	got := joined(compileOutputArgs(cpuEncoder("hevc"), mi, Plan{VideoCodec: "hevc"}, false, 4, false))
	if !strings.HasPrefix(got, "-map 0:v:1") {
		t.Errorf("must map the movie, not the cover art: %s", got)
	}
	if !strings.Contains(got, "-vf "+deintFilter) {
		t.Errorf("interlaced source must be deinterlaced: %s", got)
	}
	if !strings.Contains(got, "-map_metadata 0 -map_chapters 0") {
		t.Errorf("metadata and chapters must be kept: %s", got)
	}
	copyArgs := joined(compileOutputArgs(cpuEncoder("hevc"), mi, Plan{}, false, 4, false))
	if !strings.Contains(copyArgs, "-c:v copy") || strings.Contains(copyArgs, "libx265") {
		t.Errorf("a track-only plan must copy the video: %s", copyArgs)
	}
}

func TestHDRParams(t *testing.T) {
	pq := film("hevc", 3840, 2160, 60000)
	pq.HDR = "HDR10"
	pq.HDR10 = &HDR10Meta{MasterDisplay: "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,50)", MaxCLL: "1000,400"}
	params, tags := hdr10Params(pq)
	if !strings.Contains(params, "transfer=smpte2084") || !strings.Contains(params, "max-cll=1000,400") {
		t.Errorf("HDR10 params: %s", params)
	}
	if joined(tags) != "-color_primaries bt2020 -color_trc smpte2084 -colorspace bt2020nc" {
		t.Errorf("HDR10 tags: %v", tags)
	}
	av1, _ := av1HDRParams(pq)
	if !strings.Contains(av1, "mastering-display=G(0.2650,0.6900)") || !strings.Contains(av1, "L(1000.0000,0.0050)") {
		t.Errorf("SVT-AV1 wants float mastering display: %s", av1)
	}

	hlg := film("hevc", 3840, 2160, 60000)
	hlg.HDR, hlg.HDR10 = "HLG", pq.HDR10
	params, _ = hdr10Params(hlg)
	if !strings.Contains(params, "transfer=arib-std-b67") || strings.Contains(params, "master-display") {
		t.Errorf("HLG keeps its own curve and carries no mastering data: %s", params)
	}

	// Dolby Vision is dropped and its base layer kept: an 8.4 file is HLG underneath.
	dv := film("hevc", 3840, 2160, 60000)
	dv.HDR, dv.DVBase = "Dolby Vision", "HLG"
	if params, _ = hdr10Params(dv); !strings.Contains(params, "arib-std-b67") {
		t.Errorf("a DV file must keep its base layer's transfer curve: %s", params)
	}
}

func TestPlanWarnings(t *testing.T) {
	mi := film("hevc", 3840, 2160, 60000)
	mi.HDR, mi.DVBase, mi.HasCC = "Dolby Vision", "HDR10", true
	got := strings.Join(planWarnings(mi, Plan{VideoCodec: "hevc"}), " | ")
	if !strings.Contains(got, "closed captions") || !strings.Contains(got, "Dolby Vision layer dropped — kept as HDR10") {
		t.Errorf("warnings: %s", got)
	}
	if len(planWarnings(mi, Plan{})) != 0 {
		t.Error("a copy loses nothing")
	}
}

func TestDetectVFR(t *testing.T) {
	if detectVFR(24000.0/1001, 24000.0/1001, false) {
		t.Error("CFR detected as VFR")
	}
	if !detectVFR(30, 24, false) {
		t.Error("VFR not detected")
	}
	if detectVFR(59.94, 29.97, true) {
		t.Error("interlaced field rate is not VFR")
	}
}

func TestSVTMasterDisplay(t *testing.T) {
	got := svtMasterDisplay("G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1)")
	if got != "G(0.2650,0.6900)B(0.1500,0.0600)R(0.6800,0.3200)WP(0.3127,0.3290)L(1000.0000,0.0001)" {
		t.Errorf("got %s", got)
	}
	if svtMasterDisplay("garbage") != "garbage" {
		t.Error("unparseable input must come back unchanged")
	}
}
