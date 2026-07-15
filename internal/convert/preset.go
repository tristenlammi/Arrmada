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
	if l == "" {
		return false
	}
	for _, w := range wanted {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == l || twoToThree[w] == l || twoToThree[l] == w {
			return true
		}
	}
	return false
}

// twoToThree maps common ISO 639-1 codes to 639-2/T so "en" matches an "eng" track.
var twoToThree = map[string]string{
	"en": "eng", "es": "spa", "fr": "fre", "de": "ger", "it": "ita", "pt": "por",
	"nl": "dut", "sv": "swe", "pl": "pol", "ru": "rus", "tr": "tur", "ar": "ara",
	"hi": "hin", "ja": "jpn", "ko": "kor", "zh": "chi",
}

// vaapiDevice is the DRM render node VAAPI (AMD/Intel) encodes through.
const vaapiDevice = "/dev/dri/renderD128"

// globalArgs returns ffmpeg options that must appear before the input (device init). Only
// VAAPI needs one today.
func globalArgs(enc Encoder) []string {
	if enc.Kind == "vaapi" {
		return []string{"-vaapi_device", vaapiDevice}
	}
	return nil
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
func compileOutputArgs(enc Encoder, mi *MediaInfo, plan Plan) []string {
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
				a = append(a, fmt.Sprintf("-c:a:%d", outAud), "aac", fmt.Sprintf("-b:a:%d", outAud), "256k", fmt.Sprintf("-filter:a:%d", outAud), "loudnorm=I=-16:TP=-1.5:LRA=11")
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

	// Subtitles, container-aware. Text subs go to SRT sidecars when ExtractText is set; image
	// subs (PGS/VOBSUB) can only live in MKV. MP4 keeps text subs as mov_text.
	subCopy, subConv := false, ""
	if plan.Subs.ExtractText {
		if !mp4 {
			for _, s := range mi.Subs {
				if !s.Text {
					a = append(a, "-map", fmt.Sprintf("0:s:%d", s.SubIndex))
					subCopy = true
				}
			}
		}
	} else if mp4 {
		for _, s := range mi.Subs {
			if s.Text {
				a = append(a, "-map", fmt.Sprintf("0:s:%d", s.SubIndex))
				subConv = "mov_text"
			}
		}
	} else {
		a = append(a, "-map", "0:s?")
		subCopy = true
	}
	if subConv != "" {
		a = append(a, "-c:s", subConv)
	} else if subCopy {
		a = append(a, "-c:s", "copy")
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
		case "vaapi": // AMD/Intel hardware — upload frames to the GPU, scale + encode there.
			pix := "nv12"
			if mi.TenBit && codec != "h264" {
				pix = "p010"
			}
			chain := "format=" + pix + ",hwupload"
			if scale {
				chain += fmt.Sprintf(",scale_vaapi=w=-2:h=%d", plan.ScaleHeight)
			}
			a = append(a, "-vf", chain, "-c:v", enc.Name, "-rc_mode", "CQP", "-qp", strconv.Itoa(crf))
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
			a = append(a, "-c:v", enc.Name, "-global_quality", strconv.Itoa(crf), "-preset", "medium")
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
			a = append(a, cpuVideoArgs(enc.Name, codec, crf, mi.TenBit)...)
			// HDR10 static passthrough: ffmpeg keeps the colour tags on a re-encode but drops the
			// mastering-display / max-cll, so re-pass them to x265. (HDR10+ dynamic metadata is
			// handled by a separate inject pipeline — see encodeHDR10Plus.)
			if codec == "hevc" && enc.Name == "libx265" && (mi.HDR == "HDR10" || mi.HDR == "HDR10+") {
				a = append(a, hdr10Args(mi.HDR10)...)
			}
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
func hdr10Args(m *HDR10Meta) []string {
	params := "hdr10=1:repeat-headers=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc"
	if m != nil {
		if m.MasterDisplay != "" {
			params += ":master-display=" + m.MasterDisplay
		}
		if m.MaxCLL != "" {
			params += ":max-cll=" + m.MaxCLL
		}
	}
	return []string{
		"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc",
		"-x265-params", params,
	}
}

// cpuVideoArgs builds the CPU encoder args for a target codec. H.264 is pinned to 8-bit
// (yuv420p) for compatibility; HEVC/AV1 preserve 10-bit when the source is 10-bit.
func cpuVideoArgs(name, codec string, crf int, tenBit bool) []string {
	switch codec {
	case "h264":
		return []string{"-c:v", name, "-preset", "medium", "-crf", strconv.Itoa(crf), "-pix_fmt", "yuv420p"}
	case "av1":
		out := []string{"-c:v", name, "-preset", "8", "-crf", strconv.Itoa(crf)}
		if tenBit {
			out = append(out, "-pix_fmt", "yuv420p10le")
		}
		return out
	default: // hevc
		out := []string{"-c:v", name, "-preset", "medium", "-crf", strconv.Itoa(crf)}
		if tenBit {
			out = append(out, "-pix_fmt", "yuv420p10le")
		}
		return out
	}
}
