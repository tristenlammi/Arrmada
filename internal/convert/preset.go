package convert

import (
	"fmt"
	"strconv"
	"strings"
)

// isCandidate reports whether the "Save space" preset would act on this file — i.e. it's
// not already an efficient modern codec.
func isCandidate(mi *MediaInfo) bool {
	switch mi.VideoCodec {
	case "hevc", "av1", "vp9":
		return false
	}
	return mi.VideoCodec != ""
}

// langIn reports whether an audio/subtitle language tag matches any wanted language,
// tolerating 2- vs 3-letter codes for the common languages.
func langIn(lang string, wanted []string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	// An untagged or explicitly-unknown track is KEPT. Dropping it lost untagged original-
	// language and commentary tracks whenever any other track matched the filter, which is
	// the opposite of what "keep these languages" should do to a track of unknown language.
	if l == "" || l == "und" {
		return true
	}
	for _, w := range wanted {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == l || twoToThree[w] == l || twoToThree[l] == w {
			return true
		}
	}
	return false
}

// losslessAudio reports whether a codec carries lossless or object-based audio — TrueHD
// (Atmos), DTS-HD MA / DTS:X, FLAC, PCM. Re-encoding these to AAC is an irreversible loss of
// exactly the thing this module exists to preserve.
func losslessAudio(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "truehd", "mlp", "flac", "alac", "dts", "dtshd", "dts-hd", "pcm_s16le", "pcm_s24le", "pcm_bluray", "pcm_dvd":
		return true
	}
	return false
}

// twoToThree maps common ISO 639-1 codes to 639-2/T so "en" matches an "eng" track.
var twoToThree = map[string]string{
	"en": "eng", "es": "spa", "fr": "fre", "de": "ger", "it": "ita", "pt": "por",
	"nl": "dut", "sv": "swe", "pl": "pol", "ru": "rus", "tr": "tur", "ar": "ara",
	"hi": "hin", "ja": "jpn", "ko": "kor", "zh": "chi",
}

// vaapiDevice is the default DRM render node VAAPI encodes through. On a box with
// both an iGPU and a discrete card there are several (renderD128, renderD129, …);
// the Convert → VAAPI device setting picks which one.
const vaapiDevice = "/dev/dri/renderD128"

// globalArgs returns ffmpeg options that must appear before the input (device init). Only
// VAAPI needs one today. With hwDecode, the GPU also decodes the source (frames stay on the
// GPU as VAAPI surfaces — no CPU decode or upload); otherwise ffmpeg decodes in software and
// the filter chain uploads each frame for the hardware encoder. device is the render node.
func globalArgs(enc Encoder, hwDecode bool, device string) []string {
	if enc.Kind == "vaapi" {
		if device == "" {
			device = vaapiDevice
		}
		if hwDecode {
			return []string{"-hwaccel", "vaapi", "-hwaccel_device", device, "-hwaccel_output_format", "vaapi"}
		}
		return []string{"-vaapi_device", device}
	}
	return nil
}

// av1QIndex converts a CRF-scale quality target (0-63, what SVT-AV1 uses) into AV1's
// quantizer index (0-255), which is what the hardware AV1 encoders take. They're the same
// scale stretched by 4, so a CRF of 24 becomes a qindex of 96.
func av1QIndex(crf int) int {
	q := crf * 4
	if q < 1 {
		q = 1
	}
	if q > 255 {
		q = 255
	}
	return q
}

// crfDefault is the quality target for a codec when the plan doesn't set one. The scales
// differ per codec (AV1's CRF runs higher for the same perceived quality), so each has its own
// baseline. HEVC's 24 preserves the pre-R5 "Save space" behavior.
func crfDefault(codec string) int {
	switch codec {
	case "h264":
		return 23
	case "av1":
		return 32
	default: // hevc
		return 24
	}
}

// mp4Audio reports whether an audio codec can be copied into an MP4 container as-is. Anything
// else (TrueHD/DTS/FLAC/PCM…) is re-encoded to AAC so MP4 output never fails to mux.
func mp4Audio(codec string) bool {
	switch strings.ToLower(codec) {
	case "aac", "ac3", "eac3", "mp3", "alac":
		return true
	}
	return false
}

// compileOutputArgs turns a Plan into the ffmpeg output options: re-encode (or copy) the video
// to the target codec, optionally downscaling; keep/convert/downmix/normalize the wanted audio
// (container-safe); extract or repackage subtitles; set the container. This is the generalized
// compiler (Rules v2 R1, extended in R5) — every Plan runs through here.
func compileOutputArgs(enc Encoder, mi *MediaInfo, plan Plan, hwDecode bool, cores int, noNumaPools bool) []string {
	container := plan.Container
	if container == "" {
		container = "mkv"
	}
	mp4 := container == "mp4"
	a := []string{"-map", "0:v:0"}

	// Audio: keep the wanted-language tracks (all if no filter / nothing matched), each copied
	// (or loudnorm-/container-re-encoded), plus an optional AAC 2.0 stereo downmix for surround.
	var keepAud []AudioStream
	if len(plan.Audio.KeepLangs) > 0 {
		for _, au := range mi.Audio {
			if langIn(au.Lang, plan.Audio.KeepLangs) {
				keepAud = append(keepAud, au)
			}
		}
	}
	if len(keepAud) == 0 {
		keepAud = mi.Audio
	}
	if len(keepAud) == 0 {
		if mp4 {
			a = append(a, "-map", "0:a?", "-c:a", "aac", "-b:a", "256k") // unknown tracks → AAC for MP4 safety
		} else {
			a = append(a, "-map", "0:a?", "-c:a", "copy")
		}
	} else {
		outAud := 0
		for _, au := range keepAud {
			a = append(a, "-map", fmt.Sprintf("0:a:%d", au.AudIndex))
			switch {
			case plan.Audio.Loudnorm:
				// Never loudnorm lossless/object-based audio: it would re-encode Atmos/TrueHD/
				// DTS-HD down to 256k AAC. Those tracks are copied untouched instead.
				if losslessAudio(au.Codec) {
					a = append(a, fmt.Sprintf("-c:a:%d", outAud), "copy")
				} else {
					a = append(a, fmt.Sprintf("-c:a:%d", outAud), "aac", fmt.Sprintf("-b:a:%d", outAud), "256k", fmt.Sprintf("-filter:a:%d", outAud), "loudnorm=I=-16:TP=-1.5:LRA=11")
				}
			case mp4 && !mp4Audio(au.Codec):
				a = append(a, fmt.Sprintf("-c:a:%d", outAud), "aac", fmt.Sprintf("-b:a:%d", outAud), "256k") // MP4 can't hold TrueHD/DTS/… → AAC
			default:
				a = append(a, fmt.Sprintf("-c:a:%d", outAud), "copy")
			}
			outAud++
			if plan.Audio.AddStereo && au.Channels > 2 {
				a = append(a, "-map", fmt.Sprintf("0:a:%d", au.AudIndex),
					fmt.Sprintf("-c:a:%d", outAud), "aac", fmt.Sprintf("-ac:a:%d", outAud), "2", fmt.Sprintf("-b:a:%d", outAud), "192k",
					fmt.Sprintf("-metadata:s:a:%d", outAud), "title=Stereo")
				outAud++
			}
		}
	}

	// Subtitles are carried through untouched — the Subtitles module owns extraction/stripping now.
	// MKV copies every subtitle stream as-is; MP4 can't hold image subs (PGS/VOBSUB), so an MP4
	// target keeps only text subs, re-encoded to mov_text.
	if mp4 {
		mapped := false
		for _, s := range mi.Subs {
			if s.Text {
				a = append(a, "-map", fmt.Sprintf("0:s:%d", s.SubIndex))
				mapped = true
			}
		}
		if mapped {
			a = append(a, "-c:s", "mov_text")
		}
	} else {
		a = append(a, "-map", "0:s?", "-c:s", "copy")
	}

	// Attachments — embedded fonts, cover art. Once ANY -map is given, ffmpeg's default
	// stream selection is off, so without this every attachment is silently dropped: ASS/SSA
	// subtitles (anime especially) then render in a fallback font with the typesetting and
	// karaoke styling destroyed. MP4 can't hold attachments, so this is MKV-only.
	if !mp4 {
		a = append(a, "-map", "0:t?", "-c:t", "copy")
	}

	// Video: copy for remux-only, else re-encode to the target codec (optionally downscaled).
	if plan.VideoCodec == "" {
		a = append(a, "-c:v", "copy")
	} else {
		codec := plan.VideoCodec
		crf := plan.Quality
		if crf <= 0 {
			crf = crfDefault(codec)
		}
		if plan.VFRToCFR && mi.VFR {
			a = append(a, "-fps_mode", "cfr") // normalize VFR → prevents A/V desync
		}
		scale := plan.ScaleHeight > 0 && mi.Height > plan.ScaleHeight // downscale only, never up
		switch enc.Kind {
		case "vaapi": // AMD/Intel hardware — scale + encode on the GPU.
			if hwDecode {
				// Frames arrive as VAAPI surfaces straight from the hardware decoder,
				// so no software decode / hwupload — optionally scale on the GPU, then encode.
				if scale {
					a = append(a, "-vf", fmt.Sprintf("scale_vaapi=w=-2:h=%d", plan.ScaleHeight))
				}
			} else {
				// Software decode: convert to the right pixel format and upload each frame.
				pix := "nv12"
				if mi.TenBit && codec != "h264" {
					pix = "p010"
				}
				chain := "format=" + pix + ",hwupload"
				if scale {
					chain += fmt.Sprintf(",scale_vaapi=w=-2:h=%d", plan.ScaleHeight)
				}
				a = append(a, "-vf", chain)
			}
			// Quality has to be expressed in the ENCODER's own scale, not ours.
			//
			// hevc_vaapi/h264_vaapi take -qp on a 0-52 scale, close enough to CRF to pass
			// straight through. av1_vaapi doesn't expose -qp at all: AV1 quantizes on a
			// 0-255 index, so the value goes via -global_quality after being rescaled.
			// Passing our CRF-scale number as -qp meant av1_vaapi ignored it entirely and
			// used its own default — which produced files LARGER than the source, so every
			// AV1 conversion was discarded by the never-grow guard.
			a = append(a, "-c:v", enc.Name, "-rc_mode", "CQP")
			if codec == "av1" {
				a = append(a, "-global_quality", strconv.Itoa(av1QIndex(crf)))
			} else {
				a = append(a, "-qp", strconv.Itoa(crf))
			}
			if mi.TenBit && codec != "h264" {
				a = append(a, "-profile:v", "main10")
			}
		case "nvenc":
			if scale {
				a = append(a, "-vf", scaleCPU(plan.ScaleHeight))
			}
			a = append(a, "-c:v", enc.Name, "-preset", "p5", "-rc", "vbr", "-cq", strconv.Itoa(crf))
			if mi.TenBit && codec != "h264" {
				a = append(a, "-pix_fmt", "p010le")
			}
		case "qsv":
			if scale {
				a = append(a, "-vf", scaleCPU(plan.ScaleHeight))
			}
			qsvQ := crf
			if codec == "av1" {
				qsvQ = av1QIndex(crf) // AV1 quantizes 0-255, not on the CRF scale
			}
			a = append(a, "-c:v", enc.Name, "-global_quality", strconv.Itoa(qsvQ), "-preset", "medium")
			if mi.TenBit && codec != "h264" {
				a = append(a, "-pix_fmt", "p010le")
			}
		case "videotoolbox":
			if scale {
				a = append(a, "-vf", scaleCPU(plan.ScaleHeight))
			}
			a = append(a, "-c:v", enc.Name, "-q:v", "55")
		default: // CPU
			if scale {
				a = append(a, "-vf", scaleCPU(plan.ScaleHeight))
			}
			// HDR10/HLG static passthrough: ffmpeg keeps the colour tags on a re-encode but
			// drops the mastering-display / max-cll, so they're re-passed to x265. (HDR10+
			// dynamic metadata and the DV RPU are re-injected by their own pipelines.)
			hdrParams, colourTags := "", []string(nil)
			if isHDR(mi.HDR) {
				switch {
				case codec == "hevc" && enc.Name == "libx265":
					hdrParams, colourTags = hdr10Params(mi)
				case codec == "av1" && enc.Name == "libsvtav1":
					hdrParams, colourTags = av1HDRParams(mi)
				}
			}
			a = append(a, cpuVideoArgs(enc.Name, codec, crf, mi.TenBit, cores, hdrParams, noNumaPools)...)
			a = append(a, colourTags...)
		}
	}

	a = append(a, "-map_metadata", "0", "-map_chapters", "0")
	if mp4 {
		a = append(a, "-movflags", "+faststart") // stream-friendly: moov atom up front
	}
	a = append(a, plan.ExtraArgs...) // advanced raw-ffmpeg escape hatch (usually empty)
	return a
}

// scaleCPU builds the software scale filter that downscales to a target height while keeping
// the aspect ratio and forcing an even width (required by most codecs).
func scaleCPU(height int) string { return fmt.Sprintf("scale=-2:%d", height) }

// hdr10Args re-applies HDR10 static metadata to a libx265 encode: the BT.2020/PQ colour tags plus
// the mastering-display + max-cll (hdr10=1 emits the SEI, repeat-headers keeps it on every IDR so
// seeking stays HDR-correct). HDR10+ dynamic metadata and Dolby Vision RPU are re-embedded
// post-encode by their tools (the bundled x265 isn't built with dhdr10-info support). m may be nil.
func hdr10Params(mi *MediaInfo) (params string, colourTags []string) {
	// The transfer curve must follow the SOURCE. This was hardcoded to smpte2084 (PQ), so
	// every HLG file was re-tagged as PQ — it then plays back with wrong brightness, washed
	// out or crushed, with the original already in the recycle bin.
	trc := "smpte2084"
	if mi.HDR == "HLG" {
		trc = "arib-std-b67"
	}
	params = "hdr10=1:repeat-headers=1:colorprim=bt2020:transfer=" + trc + ":colormatrix=bt2020nc"
	// Mastering display / max-cll describe an absolute-luminance (PQ) grade. HLG is relative
	// and self-describing, so it carries neither.
	if mi.HDR != "HLG" && mi.HDR10 != nil {
		if mi.HDR10.MasterDisplay != "" {
			params += ":master-display=" + mi.HDR10.MasterDisplay
		}
		if mi.HDR10.MaxCLL != "" {
			params += ":max-cll=" + mi.HDR10.MaxCLL
		}
	}
	return params, []string{"-color_primaries", "bt2020", "-color_trc", trc, "-colorspace", "bt2020nc"}
}

// av1HDRParams builds the SVT-AV1 static-HDR parameters plus the colour tags. AV1 carries
// the colour description in its sequence header via ffmpeg's -color_* flags, and the
// mastering display / content light as metadata OBUs via -svtav1-params.
//
// Unlike the x265 form there's no hdr10=1 or colorprim= — those are x265-specific knobs.
func av1HDRParams(mi *MediaInfo) (params string, colourTags []string) {
	trc := "smpte2084"
	if mi.HDR == "HLG" {
		trc = "arib-std-b67"
	}
	// HLG is relative and self-describing: no mastering metadata applies.
	if mi.HDR != "HLG" && mi.HDR10 != nil {
		if mi.HDR10.MasterDisplay != "" {
			params = "mastering-display=" + mi.HDR10.MasterDisplay
		}
		if mi.HDR10.MaxCLL != "" {
			if params != "" {
				params += ":"
			}
			params += "content-light=" + mi.HDR10.MaxCLL
		}
	}
	return params, []string{"-color_primaries", "bt2020", "-color_trc", trc, "-colorspace", "bt2020nc"}
}

// hdr10Args is the standalone form used by the elementary-stream HDR pipelines, which build
// their own x265 command rather than going through compileOutputArgs.
func hdr10Args(mi *MediaInfo) []string {
	params, tags := hdr10Params(mi)
	return append(tags, "-x265-params", params)
}

// cpuVideoArgs builds the CPU encoder args for a target codec. H.264 is pinned to 8-bit
// (yuv420p) for compatibility; HEVC/AV1 preserve 10-bit when the source is 10-bit.
// stripTuningParams removes the quality-tuning parameter strings from a compiled command,
// leaving the plain preset/CRF encode. Used for the safe-mode retry: the tuned parameters
// are a much larger surface than "-preset slow -crf 18", and a failure there shouldn't cost
// the user the conversion when the simple form would have worked.
func stripTuningParams(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-x265-params" || args[i] == "-svtav1-params" {
			i++ // skip the value too
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// cpuVideoArgs builds the CPU encoder args. cores bounds the encoder's own thread pool so a
// library conversion can't take the whole machine — the server is also running Plex and
// whatever else, and an encode that saturates every core makes the box unusable for days.
func cpuVideoArgs(name, codec string, crf int, tenBit bool, cores int, hdrParams string, noNumaPools bool) []string {
	switch codec {
	case "h264":
		return []string{"-c:v", name, "-preset", "medium", "-crf", strconv.Itoa(crf), "-pix_fmt", "yuv420p"}

	case "av1":
		// preset 5, not 8. SVT-AV1's presets run 0 (slowest) to 13; 8 is a *fast* preset and
		// was plainly at odds with a module whose purpose is quality retention. tune=0
		// targets subjective quality — the default tunes for PSNR, which visibly
		// over-smooths. lp bounds the thread pool.
		params := fmt.Sprintf("tune=0:lp=%d", cores)
		// SVT-AV1 takes the mastering display and content light in exactly the same string
		// form x265 does, so the probed metadata is reused verbatim. Verified against this
		// ffmpeg build by encoding and probing the output: every value round-trips.
		if hdrParams != "" {
			params += ":" + hdrParams
		}
		out := []string{"-c:v", name, "-preset", "5", "-crf", strconv.Itoa(crf), "-svtav1-params", params}
		if tenBit {
			out = append(out, "-pix_fmt", "yuv420p10le")
		}
		return out

	default: // hevc
		// preset slow (~1.6x medium's time for a real fidelity gain), plus the params that
		// matter for retaining detail rather than for speed:
		//   aq-mode=3   better bit distribution in dark scenes and gradients
		//   psy-rd      preserves texture/grain the default happily smooths away
		//   no-sao      SAO is x265's classic detail-smearer at high quality
		//   rc-lookahead / bframes  more context for rate decisions
		// NOTE: no "pools=" here. It's x265's thread-topology knob and the one most likely
		// to misbehave when the detected core count doesn't match what the container is
		// actually allowed to use. ffmpeg's -threads already bounds the encode, and dropping
		// pools measured no slower.
		params := "aq-mode=3:psy-rd=2.0:psy-rdoq=1.0:no-sao=1:bframes=8:rc-lookahead=40"
		if noNumaPools {
			// Worker pools bind to NUMA nodes via set_mempolicy, which this environment
			// denies. Unpooled keeps frame-level parallelism and, unlike the default,
			// finishes. See numaPoolsBlocked.
			params += ":pools=none"
		}
		// HDR params must be MERGED here, not appended as a second -x265-params: ffmpeg keeps
		// only the last occurrence, so two flags means one set is silently discarded.
		if hdrParams != "" {
			params += ":" + hdrParams
		}
		out := []string{"-c:v", name, "-preset", "slow", "-crf", strconv.Itoa(crf), "-x265-params", params}
		if tenBit {
			out = append(out, "-pix_fmt", "yuv420p10le")
		}
		return out
	}
}
