package convert

import "testing"

// The library's rule: nothing stays in H.264, nothing modern gets re-encoded, and AV1 is
// never the destination for video that is already efficiently encoded.
func TestNeverRecodesHEVCIntoAV1(t *testing.T) {
	// The trade AV1 can't justify: a second generation of loss on already-lossy video for
	// a modest size gain. "Recode modern" must not buy it either — that switch exists for
	// re-encoding into HEVC, not for feeding HEVC to AV1.
	for _, recodeModern := range []bool{false, true} {
		if isCandidateCodec("hevc", "av1", recodeModern) {
			t.Errorf("recodeModern=%v: HEVC must never be re-encoded into AV1", recodeModern)
		}
		if isCandidateCodec("av1", "av1", recodeModern) {
			t.Errorf("recodeModern=%v: AV1 is already the target", recodeModern)
		}
		if isCandidateCodec("vp9", "av1", recodeModern) {
			t.Errorf("recodeModern=%v: VP9 is already efficient — not an AV1 source", recodeModern)
		}
	}

	// H.264 and older ARE the wasteful sources this exists for.
	for _, src := range []string{"h264", "mpeg2video", "vc1", "mpeg4"} {
		if !isCandidateCodec(src, "av1", false) {
			t.Errorf("%s → AV1 must be converted — leaving it is the thing we're fixing", src)
		}
		if !isCandidateCodec(src, "hevc", false) {
			t.Errorf("%s → HEVC must be converted", src)
		}
	}
}

// AV1 is picked for efficiency, and a GPU's fixed-function AV1 block hands most of that
// back. Software must win the selection even on a machine whose hardware AV1 works —
// the opposite of every other codec, where hardware is preferred.
func TestAV1PrefersSoftwareEncoder(t *testing.T) {
	encs := []Encoder{
		{Codec: "hevc", Name: "hevc_vaapi", Kind: "vaapi", Hardware: true, Available: true},
		{Codec: "hevc", Name: "libx265", Kind: "cpu", Available: true},
		{Codec: "av1", Name: "libsvtav1", Kind: "cpu", Available: true},
		{Codec: "av1", Name: "av1_vaapi", Kind: "vaapi", Hardware: true, Available: true},
	}
	if got := encoderFor("av1", encs).Name; got != "libsvtav1" {
		t.Errorf("AV1 encoder = %s, want libsvtav1 — the GPU block undoes AV1's whole advantage", got)
	}
	// HEVC keeps preferring hardware: the quality gap is far smaller there, and it's the
	// codec to reach for when speed is what matters.
	if got := encoderFor("hevc", encs).Name; got != "hevc_vaapi" {
		t.Errorf("HEVC encoder = %s, want hevc_vaapi", got)
	}
	// A broken SVT-AV1 build shouldn't mean no AV1 at all — fall back to the block.
	noSVT := []Encoder{
		{Codec: "av1", Name: "libsvtav1", Kind: "cpu", Available: false},
		{Codec: "av1", Name: "av1_vaapi", Kind: "vaapi", Hardware: true, Available: true},
	}
	if got := encoderFor("av1", noSVT).Name; got != "av1_vaapi" {
		t.Errorf("with no working SVT-AV1, encoder = %s, want the hardware block", got)
	}
}

// A file whose HDR metadata AV1 can't carry must be converted to HEVC, not skipped: the
// goal is a library with no H.264 in it, and HEVC preserves the metadata in full. Only
// what NEITHER codec can preserve is left alone.
func TestHDRFallbackPrefersHEVCOverSkipping(t *testing.T) {
	s := &Service{doviTool: "/usr/local/bin/dovi_tool", hdr10plusTool: "/usr/local/bin/hdr10plus_tool",
		encoders: []Encoder{{Codec: "hevc", Name: "libx265", Kind: "cpu", Available: true}}}
	av1 := Plan{VideoCodec: "av1"}

	for _, hdr := range []string{"HDR10+", "Dolby Vision"} {
		mi := &MediaInfo{HDR: hdr}
		if s.canPreserveHDR(mi, av1, cpuEncoder("av1")) {
			t.Fatalf("%s: precondition — AV1 cannot carry this, or the test proves nothing", hdr)
		}
		if got := s.hdrFallbackCodec(mi, av1); got != "hevc" {
			t.Errorf("%s: fallback = %q, want hevc — it must convert, not sit in H.264", hdr, got)
		}
	}

	// Static HDR10 and HLG ride along in AV1's own headers, so there's nothing to fall
	// back FROM — those stay on AV1.
	for _, hdr := range []string{"HDR10", "HLG"} {
		if !s.canPreserveHDR(&MediaInfo{HDR: hdr}, av1, cpuEncoder("av1")) {
			t.Errorf("%s must be preservable in AV1 — no reroute needed", hdr)
		}
	}

	// Dolby Vision profile 5 is IPT-encoded: converting the RPU can't convert the pixels,
	// so HEVC is no better and skipping is correct.
	if got := s.hdrFallbackCodec(&MediaInfo{HDR: "Dolby Vision", DVProfile: 5}, av1); got != "" {
		t.Errorf("DV profile 5 fallback = %q, want none — neither codec can preserve it", got)
	}

	// Without the injection tools HEVC can't preserve it either, so there's no reroute.
	bare := &Service{encoders: []Encoder{{Codec: "hevc", Name: "libx265", Kind: "cpu", Available: true}}}
	if got := bare.hdrFallbackCodec(&MediaInfo{HDR: "HDR10+"}, av1); got != "" {
		t.Errorf("with no hdr10plus_tool, fallback = %q, want none", got)
	}

	// An HEVC plan has nowhere better to go.
	if got := s.hdrFallbackCodec(&MediaInfo{HDR: "HDR10+"}, Plan{VideoCodec: "hevc"}); got != "" {
		t.Errorf("HEVC plan fallback = %q, want none", got)
	}
}

// The CRF scales are not shared: AV1's 24 and HEVC's 20 are different targets. A reroute
// has to re-target, or an AV1 number silently lands as a much softer HEVC encode.
func TestRerouteRetargetsQuality(t *testing.T) {
	if maxQualityCRF("av1") == maxQualityCRF("hevc") {
		t.Skip("scales coincide; nothing to guard")
	}
	if maxQualityCRF("hevc") >= maxQualityCRF("av1") {
		t.Errorf("HEVC CRF %d should be tighter (lower) than AV1's %d",
			maxQualityCRF("hevc"), maxQualityCRF("av1"))
	}
}
