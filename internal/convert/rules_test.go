package convert

import "testing"

// The conversion rules, as the plan states them. These are the behaviour users are promised
// on the format cards, so they're worth pinning down.
func TestCandidacyRules(t *testing.T) {
	cases := []struct {
		name       string
		codec      string
		target     string
		recodeHEVC bool
		want       bool
	}{
		{"h264 to hevc converts", "h264", "hevc", false, true},
		{"h264 to av1 converts", "h264", "av1", false, true},
		{"mpeg2 to hevc converts", "mpeg2video", "hevc", false, true},
		{"already hevc is left alone", "hevc", "hevc", false, false},
		{"already av1 is left alone", "av1", "av1", false, false},
		{"unprobed file is never a candidate", "", "hevc", false, false},

		// HEVC -> AV1 is a second lossy generation for ~20-30% space, so it's opt-in.
		{"hevc to av1 is skipped by default", "hevc", "av1", false, false},
		{"hevc to av1 converts when opted in", "hevc", "av1", true, true},
		// The opt-in must not resurrect no-op work.
		{"opt-in does not make av1 to av1 a candidate", "av1", "av1", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isCandidateCodec(c.codec, c.target, c.recodeHEVC); got != c.want {
				t.Errorf("isCandidateCodec(%q, %q, %v) = %v, want %v", c.codec, c.target, c.recodeHEVC, got, c.want)
			}
		})
	}
}

// What each format can actually preserve, given the bundled tools are HEVC-only.
func TestHDRPreservationByFormat(t *testing.T) {
	withTools := &Service{doviTool: "/usr/local/bin/dovi_tool", hdr10plusTool: "/usr/local/bin/hdr10plus_tool"}
	x265 := cpuEncoder("hevc")
	av1 := cpuEncoder("av1")

	cases := []struct {
		hdr    string
		target string
		enc    Encoder
		want   bool
		why    string
	}{
		// HEVC preserves everything, on the x265 path.
		{"HDR10", "hevc", x265, true, "static metadata re-passed to x265"},
		{"HLG", "hevc", x265, true, "transfer curve only"},
		{"HDR10+", "hevc", x265, true, "hdr10plus_tool injects after the encode"},
		{"Dolby Vision", "hevc", x265, true, "dovi_tool injects the RPU"},

		// AV1 handles what the encoder itself can write: colour tags and the static HDR10
		// metadata OBUs. dovi_tool and hdr10plus_tool cannot write into an AV1 bitstream,
		// so only the formats needing post-encode injection are skipped.
		{"HLG", "av1", av1, true, "transfer curve only, nothing to inject"},
		{"HDR10+", "av1", av1, false, "no AV1 injection path"},
		{"Dolby Vision", "av1", av1, false, "no AV1 injection path"},
		{"HDR10", "av1", av1, true, "SVT-AV1 takes mastering-display; verified round-tripping into the output"},
	}
	for _, c := range cases {
		mi := &MediaInfo{HDR: c.hdr, VideoCodec: "h264"}
		plan := Plan{VideoCodec: c.target}
		if got := withTools.canPreserveHDR(mi, plan, c.enc); got != c.want {
			t.Errorf("%s -> %s = %v, want %v (%s)", c.hdr, c.target, got, c.want, c.why)
		}
	}
}

// Without the tools present, HEVC must skip rather than silently flatten the picture.
func TestHDRSkippedWhenToolsMissing(t *testing.T) {
	bare := &Service{} // no dovi_tool, no hdr10plus_tool
	x265 := cpuEncoder("hevc")
	plan := Plan{VideoCodec: "hevc"}

	for _, hdr := range []string{"Dolby Vision", "HDR10+"} {
		if bare.canPreserveHDR(&MediaInfo{HDR: hdr}, plan, x265) {
			t.Errorf("%s must not be converted without its metadata tool", hdr)
		}
	}
	// HDR10 and HLG need no external tool, so they still convert.
	for _, hdr := range []string{"HDR10", "HLG"} {
		if !bare.canPreserveHDR(&MediaInfo{HDR: hdr}, plan, x265) {
			t.Errorf("%s needs no external tool and should still convert", hdr)
		}
	}
}

// HLG must keep its own transfer curve. Re-tagging it as PQ visibly breaks the picture.
func TestHLGKeepsItsTransferCurve(t *testing.T) {
	hlg, tags := hdr10Params(&MediaInfo{HDR: "HLG"})
	if want := "transfer=arib-std-b67"; !contains(hlg, want) {
		t.Errorf("HLG params = %q, want %s", hlg, want)
	}
	if contains(hlg, "smpte2084") {
		t.Errorf("HLG must not be tagged PQ: %q", hlg)
	}
	if !containsSlice(tags, "arib-std-b67") {
		t.Errorf("colour tags = %v, want the HLG transfer", tags)
	}
	// HLG is relative and self-describing — mastering metadata doesn't apply.
	withMeta := &MediaInfo{HDR: "HLG", HDR10: &HDR10Meta{MasterDisplay: "G(1,2)", MaxCLL: "1000,400"}}
	if p, _ := hdr10Params(withMeta); contains(p, "master-display") || contains(p, "max-cll") {
		t.Errorf("HLG must not carry mastering metadata: %q", p)
	}

	pq, _ := hdr10Params(&MediaInfo{HDR: "HDR10", HDR10: &HDR10Meta{MasterDisplay: "G(1,2)", MaxCLL: "1000,400"}})
	if !contains(pq, "transfer=smpte2084") || !contains(pq, "master-display=G(1,2)") || !contains(pq, "max-cll=1000,400") {
		t.Errorf("HDR10 params lost something: %q", pq)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}
func containsSlice(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// AV1 carries static HDR differently from x265: metadata OBUs via -svtav1-params, with no
// hdr10=1 / colorprim= (those are x265-only knobs). Verified against the bundled ffmpeg by
// encoding and probing the output.
func TestAV1HDRParams(t *testing.T) {
	meta := &HDR10Meta{MasterDisplay: "G(0.265,0.690)B(0.150,0.060)R(0.680,0.320)WP(0.3127,0.3290)L(1000,0.0001)", MaxCLL: "1000,400"}

	params, tags := av1HDRParams(&MediaInfo{HDR: "HDR10", HDR10: meta})
	if !contains(params, "mastering-display=G(0.265,0.690)") || !contains(params, "content-light=1000,400") {
		t.Errorf("AV1 HDR10 params = %q", params)
	}
	if contains(params, "hdr10=1") || contains(params, "colorprim") {
		t.Errorf("x265-only knobs leaked into the AV1 params: %q", params)
	}
	if !containsSlice(tags, "smpte2084") {
		t.Errorf("HDR10 colour tags = %v, want PQ", tags)
	}

	// HLG: transfer curve only, and never mastering metadata.
	hlgParams, hlgTags := av1HDRParams(&MediaInfo{HDR: "HLG", HDR10: meta})
	if hlgParams != "" {
		t.Errorf("HLG must carry no mastering metadata, got %q", hlgParams)
	}
	if !containsSlice(hlgTags, "arib-std-b67") {
		t.Errorf("HLG colour tags = %v, want the HLG transfer", hlgTags)
	}
}
