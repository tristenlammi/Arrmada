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
	nl := normLang(l)
	for _, w := range wanted {
		if normLang(w) == nl {
			return true
		}
	}
	return false
}

// normLang canonicalises a language code for comparison: 639-1 two-letter codes and the
// bibliographic 639-2/B variants (fre, ger, dut, chi, …) all collapse to the terminological
// 639-2/T code, so "fr", "fre" and "fra" tags all match a user's "french" filter however the
// muxer spelled it.
func normLang(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if t, ok := twoToThree[c]; ok {
		return t
	}
	if t, ok := biblioToTerm[c]; ok {
		return t
	}
	return c
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

// twoToThree maps common ISO 639-1 codes to 639-2/T (terminological) so "en" matches an
// "eng" track. Bibliographic variants are folded in separately (biblioToTerm).
var twoToThree = map[string]string{
	"en": "eng", "es": "spa", "fr": "fra", "de": "deu", "it": "ita", "pt": "por",
	"nl": "nld", "sv": "swe", "pl": "pol", "ru": "rus", "tr": "tur", "ar": "ara",
	"hi": "hin", "ja": "jpn", "ko": "kor", "zh": "zho", "cs": "ces", "el": "ell",
	"da": "dan", "no": "nor", "fi": "fin", "he": "heb", "th": "tha", "vi": "vie",
	"uk": "ukr", "hu": "hun", "ro": "ron", "id": "ind",
}

// biblioToTerm maps the ISO 639-2/B (bibliographic) codes to their /T (terminological)
// twins. Muxers disagree on which to write — "fre" and "fra" are both French — so both
// spellings must match a language filter, or tracks are dropped depending on which tool
// tagged them.
var biblioToTerm = map[string]string{
	"fre": "fra", "ger": "deu", "dut": "nld", "chi": "zho", "cze": "ces", "gre": "ell",
	"ice": "isl", "per": "fas", "rum": "ron", "slo": "slk", "alb": "sqi", "arm": "hye",
	"baq": "eus", "bur": "mya", "geo": "kat", "mac": "mkd", "may": "msa", "mao": "mri",
	"tib": "bod", "wel": "cym",
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

// hardwareQualityOffset tightens the quality target for hardware encoders.
//
// Fixed-function silicon needs a tighter quantizer than a software encoder to reach the
// same picture, because it has far simpler rate-distortion optimisation. Feeding hardware
// the same CRF number we'd give x265 or SVT-AV1 therefore lands softer than intended: the
// first real conversion here dropped a 1080p episode from 12.1 to 2.1 Mb/s, an 83% cut,
// with SSIM falling to 0.9754 — comfortably more aggressive than "maximum quality
// retention" should be.
//
// Four points is a deliberate, conservative correction rather than a measured constant.
// The CPU-vs-GPU comparison (plan phase 6) is what would replace this heuristic with a
// number derived from real content.
const hardwareQualityOffset = 4

// hardwareQuality converts a software-scale CRF target into the tighter one a hardware
// encoder needs for comparable output.
func hardwareQuality(crf int) int {
	q := crf - hardwareQualityOffset
	if q < 1 {
		q = 1
	}
	return q
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

// mkvSub reports whether a subtitle codec can be copied into Matroska as-is. The MP4 family
// (mov_text/tx3g/raw text) can't be — Matroska has no mapping for them and the mux fails at
// header write — so those are transcoded to SRT instead.
func mkvSub(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "mov_text", "tx3g", "text":
		return false
	}
	return true
}

// keptAudio applies the plan's language filter to the probed audio tracks: keep the matching
// tracks, or all of them when there's no filter / nothing matched. Shared by the compiler
// and the warnings list so they can never disagree about which tracks survive.
func keptAudio(mi *MediaInfo, plan Plan) []AudioStream {
	var keep []AudioStream
	if len(plan.Audio.KeepLangs) > 0 {
		for _, au := range mi.Audio {
			if langIn(au.Lang, plan.Audio.KeepLangs) {
				keep = append(keep, au)
			}
		}
	}
	if len(keep) == 0 {
		keep = mi.Audio
	}
	return keep
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
	// Map the REAL video stream: cover art is a video stream too (attached_pic), so 0:v:0
	// isn't always the movie.
	a := []string{"-map", fmt.Sprintf("0:v:%d", mi.VideoIndex)}

	// Audio: keep the wanted-language tracks (all if no filter / nothing matched), each copied
	// (or loudnorm-/container-re-encoded), plus an optional AAC 2.0 stereo downmix for surround.
	keepAud := keptAudio(mi, plan)
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
			// Never loudnorm lossless/object-based audio: it would re-encode Atmos/TrueHD/
			// DTS-HD down to 256k AAC. Those tracks are copied untouched (MKV) — but MP4
			// can't hold them at all, so the container branch below still applies.
			case plan.Audio.Loudnorm && !losslessAudio(au.Codec):
				a = append(a, fmt.Sprintf("-c:a:%d", outAud), "aac", fmt.Sprintf("-b:a:%d", outAud), "256k", fmt.Sprintf("-filter:a:%d", outAud), "loudnorm=I=-16:TP=-1.5:LRA=11")
			case mp4 && !mp4Audio(au.Codec):
				// MP4 can't hold TrueHD/DTS/… → AAC. Checked even when loudnorm exempted a
				// lossless track above: copying TrueHD into MP4 fails at mux time.
				a = append(a, fmt.Sprintf("-c:a:%d", outAud), "aac", fmt.Sprintf("-b:a:%d", outAud), "256k")
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
		// Matroska can't mux MP4-family text subs (mov_text/tx3g): with -c:s copy every MKV
		// conversion of an MP4-with-subs source fails at header write. Transcode just those
		// streams to SRT; image subs (PGS/VOBSUB) and native text subs still copy. All subs
		// are mapped in order, so output sub N is input sub N and per-stream overrides land.
		for _, s := range mi.Subs {
			if !mkvSub(s.Codec) {
				a = append(a, fmt.Sprintf("-c:s:%d", s.SubIndex), "srt")
			}
		}
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
		// The software-frame filter chain (every path except full-GPU VAAPI): deinterlace
		// first when the source is interlaced — encoding combed fields as progressive frames
		// bakes the combing in permanently — then the optional downscale.
		swVF := swFilterChain(mi, scale, plan.ScaleHeight)
		switch enc.Kind {
		case "vaapi": // AMD/Intel hardware — scale + encode on the GPU.
			if hwDecode {
				// Frames arrive as VAAPI surfaces straight from the hardware decoder, so no
				// software decode / hwupload — deinterlace/scale on the GPU, then encode.
				var chain []string
				if mi.Interlaced {
					chain = append(chain, "deinterlace_vaapi")
				}
				if scale {
					chain = append(chain, fmt.Sprintf("scale_vaapi=w=-2:h=%d", plan.ScaleHeight))
				}
				if len(chain) > 0 {
					a = append(a, "-vf", strings.Join(chain, ","))
				}
			} else {
				// Software decode: (deinterlace,) convert to the right pixel format and
				// upload each frame; scaling happens on the GPU after upload.
				pix := "nv12"
				if mi.TenBit && codec != "h264" {
					pix = "p010"
				}
				var chain []string
				if mi.Interlaced {
					chain = append(chain, deintFilter)
				}
				chain = append(chain, "format="+pix, "hwupload")
				if scale {
					chain = append(chain, fmt.Sprintf("scale_vaapi=w=-2:h=%d", plan.ScaleHeight))
				}
				a = append(a, "-vf", strings.Join(chain, ","))
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
				a = append(a, "-global_quality", strconv.Itoa(av1QIndex(hardwareQuality(crf))))
			} else {
				a = append(a, "-qp", strconv.Itoa(hardwareQuality(crf)))
			}
			if mi.TenBit && codec != "h264" {
				a = append(a, "-profile:v", "main10")
			}
			a = append(a, colourTagArgs(mi)...)
		case "nvenc":
			if swVF != "" {
				a = append(a, "-vf", swVF)
			}
			// -b:v 0 matters: NVENC's constant-quality mode is otherwise still capped by the
			// default average bitrate (2 Mb/s), which silently overrides the -cq target.
			a = append(a, "-c:v", enc.Name, "-preset", "p5", "-rc", "vbr", "-cq", strconv.Itoa(hardwareQuality(crf)), "-b:v", "0")
			if mi.TenBit && codec != "h264" {
				a = append(a, "-pix_fmt", "p010le")
			}
			a = append(a, colourTagArgs(mi)...)
		case "qsv":
			if swVF != "" {
				a = append(a, "-vf", swVF)
			}
			// QSV's -global_quality is ICQ on a 1-51 CRF-like scale for EVERY codec —
			// including av1_qsv. Rescaling to the AV1 0-255 qindex here fed it ~80+, which
			// clamps to worst quality. (VAAPI's AV1 path genuinely is 0-255; QSV's is not.)
			a = append(a, "-c:v", enc.Name, "-global_quality", strconv.Itoa(hardwareQuality(crf)), "-preset", "medium")
			if mi.TenBit && codec != "h264" {
				a = append(a, "-pix_fmt", "p010le")
			}
			a = append(a, colourTagArgs(mi)...)
		case "videotoolbox":
			if swVF != "" {
				a = append(a, "-vf", swVF)
			}
			// -q:v is VideoToolbox's 1-100 quality scale (higher = better); the plan's CRF
			// target is mapped onto it rather than ignored (it was hardcoded to 55).
			a = append(a, "-c:v", enc.Name, "-q:v", strconv.Itoa(vtQuality(crf)))
			if mi.TenBit && codec != "h264" {
				a = append(a, "-profile:v", "main10", "-pix_fmt", "p010le")
			}
			a = append(a, colourTagArgs(mi)...)
		default: // CPU
			if swVF != "" {
				a = append(a, "-vf", swVF)
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

// planWarnings lists, in human-readable form, what running this plan on this file will lose.
// The degradations themselves are deliberate (MP4 physically can't hold lossless audio or
// image subs), but they must never be silent: the job note should say exactly what was
// traded away. Derived from the same inputs as compileOutputArgs so the two can't disagree.
func planWarnings(mi *MediaInfo, plan Plan) []string {
	var w []string
	if plan.Container == "mp4" {
		for _, au := range keptAudio(mi, plan) {
			if mp4Audio(au.Codec) {
				continue
			}
			if plan.Audio.Loudnorm && !losslessAudio(au.Codec) {
				continue // the user asked for this track to be re-encoded anyway
			}
			label := strings.ToUpper(au.Codec)
			if lay := channelLayout(au.Channels); lay != "" {
				label += " " + lay
			}
			if au.Lang != "" && au.Lang != "und" {
				label += " [" + au.Lang + "]"
			}
			if losslessAudio(au.Codec) {
				w = append(w, fmt.Sprintf("lossless %s audio re-encoded to AAC 256k — MP4 can't hold it", label))
			} else {
				w = append(w, fmt.Sprintf("%s audio re-encoded to AAC 256k — MP4 can't hold it", label))
			}
		}
		img, styled := 0, 0
		for _, sub := range mi.Subs {
			switch {
			case !sub.Text:
				img++
			case strings.EqualFold(sub.Codec, "ass") || strings.EqualFold(sub.Codec, "ssa"):
				styled++
			}
		}
		if img > 0 {
			w = append(w, fmt.Sprintf("%d image subtitle track(s) (PGS/VOBSUB) dropped — MP4 can't hold them", img))
		}
		if styled > 0 {
			w = append(w, fmt.Sprintf("%d styled subtitle track(s) (ASS/SSA) flattened to plain mov_text", styled))
		}
	}
	if plan.VideoCodec != "" && mi.HasCC {
		w = append(w, "embedded closed captions (CEA-608/708) are lost on re-encode")
	}
	return w
}

// scaleCPU builds the software scale filter that downscales to a target height while keeping
// the aspect ratio and forcing an even width (required by most codecs).
func scaleCPU(height int) string { return fmt.Sprintf("scale=-2:%d", height) }

// deintFilter is the software deinterlacer. send_frame keeps the frame count 1:1 with the
// source (bwdif's default, send_field, doubles the rate) — important both for A/V timing and
// for the DV/HDR10+ pipelines, where per-frame metadata must stay aligned.
const deintFilter = "bwdif=mode=send_frame"

// swFilterChain builds the software video-filter chain shared by every path that feeds the
// encoder system-memory frames (CPU, NVENC, QSV, VideoToolbox, and the VAAPI software-decode
// leg before upload): deinterlace when needed, then the optional downscale. "" = no filter.
func swFilterChain(mi *MediaInfo, scale bool, height int) string {
	var chain []string
	if mi.Interlaced {
		chain = append(chain, deintFilter)
	}
	if scale {
		chain = append(chain, scaleCPU(height))
	}
	return strings.Join(chain, ",")
}

// colourTagArgs re-asserts the source's colour tags on a hardware encode. Hardware encoders
// don't reliably forward primaries/transfer/matrix into the output headers, and an untagged
// file gets guessed at by players — usually right for bt709, visibly wrong for anything else.
func colourTagArgs(mi *MediaInfo) []string {
	ok := func(v string) bool { return v != "" && v != "unknown" && v != "unspecified" }
	var a []string
	if ok(mi.ColorPrimaries) {
		a = append(a, "-color_primaries", mi.ColorPrimaries)
	}
	if ok(mi.ColorTransfer) {
		a = append(a, "-color_trc", mi.ColorTransfer)
	}
	if ok(mi.ColorSpace) {
		a = append(a, "-colorspace", mi.ColorSpace)
	}
	return a
}

// vtQuality maps a CRF-scale target onto VideoToolbox's -q:v, a 1-100 scale where higher is
// better. The mapping is a documented heuristic (VT exposes no CRF equivalent): the default
// CRF 24 lands near the old fixed value of 55, and lower CRFs push quality up from there.
func vtQuality(crf int) int {
	q := 100 - 2*crf
	if q < 1 {
		q = 1
	}
	if q > 100 {
		q = 100
	}
	return q
}

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
			// SVT-AV1's documented mastering-display form uses FLOATS — chromaticities as
			// G(0.2650,0.6900)… and luminance as L(1000.0000,0.0100) — while x265 takes the
			// raw integer units. The probed x265 string is converted, not reused.
			params = "mastering-display=" + svtMasterDisplay(mi.HDR10.MasterDisplay)
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

// svtMasterDisplay converts the x265 integer-unit mastering-display string (chromaticity in
// 0.00002 units, luminance in 0.0001 cd/m² units — e.g. "G(13250,34500)…L(10000000,1)") into
// the float form SVT-AV1 documents ("G(0.2650,0.6900)…L(1000.0000,0.0001)"). An unparseable
// string is returned unchanged rather than mangled.
func svtMasterDisplay(x265 string) string {
	var out strings.Builder
	rest := x265
	for len(rest) > 0 {
		open := strings.IndexByte(rest, '(')
		if open < 0 {
			return x265
		}
		label := rest[:open]
		closing := strings.IndexByte(rest[open:], ')')
		if closing < 0 {
			return x265
		}
		inner := rest[open+1 : open+closing]
		parts := strings.SplitN(inner, ",", 2)
		if len(parts) != 2 {
			return x265
		}
		x, errX := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		y, errY := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if errX != nil || errY != nil {
			return x265
		}
		unit := 0.00002 // chromaticity coordinates
		if label == "L" {
			unit = 0.0001 // luminance, cd/m²
		}
		fmt.Fprintf(&out, "%s(%.4f,%.4f)", label, x*unit, y*unit)
		rest = rest[open+closing+1:]
	}
	return out.String()
}

// stripTuningParams removes the quality-tuning parameters from a compiled command, leaving
// the plain preset/CRF encode. Used for the safe-mode retry: the tuned parameters are a much
// larger surface than "-preset slow -crf 18", and a failure there shouldn't cost the user
// the conversion when the simple form would have worked. HDR metadata and environment
// workarounds inside -x265-params/-svtav1-params are NOT tuning and survive the strip (see
// stripTuningKeys) — the old whole-argument removal silently produced SDR-tagged output on
// every safe-mode retry of an HDR file. When the stripped value is empty the flag pair is
// dropped entirely (preserving the old "nothing left" shape).
func stripTuningParams(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if (args[i] == "-x265-params" || args[i] == "-svtav1-params") && i+1 < len(args) {
			if kept := stripTuningKeys(args[i+1]); kept != "" {
				out = append(out, args[i], kept)
			}
			i++ // the value is handled either way
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// stripTuningKeys removes the quality-TUNING keys from an x265/SVT-AV1 params string while
// keeping everything that isn't tuning: the HDR metadata (hdr10, repeat-headers, colorprim,
// transfer, colormatrix, master-display/mastering-display, max-cll/content-light, chromaloc)
// and the pools key (the NUMA workaround / core budget — dropping it can crash the encode or
// unbound it from the core budget). Package-level so the safe-mode retry in service.go can
// reuse it on any params string.
func stripTuningKeys(params string) string {
	keep := map[string]bool{
		"hdr10": true, "repeat-headers": true, "colorprim": true, "transfer": true,
		"colormatrix": true, "master-display": true, "max-cll": true, "chromaloc": true,
		"mastering-display": true, "content-light": true, // the SVT-AV1 spellings
		"pools": true,
	}
	var out []string
	for _, kv := range strings.Split(params, ":") {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if keep[strings.ToLower(strings.TrimSpace(key))] {
			out = append(out, kv)
		}
	}
	return strings.Join(out, ":")
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
		// hdrParams arrives already in SVT-AV1's own format (float mastering-display via
		// av1HDRParams/svtMasterDisplay — NOT x265's integer units, which SVT misreads).
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
		params := "aq-mode=3:psy-rd=2.0:psy-rdoq=1.0:no-sao=1:bframes=8:rc-lookahead=40"
		switch {
		case noNumaPools:
			// Worker pools bind to NUMA nodes via set_mempolicy, which this environment
			// denies. Unpooled keeps frame-level parallelism and, unlike the default,
			// finishes. See numaPoolsBlocked.
			params += ":pools=none"
		case cores > 0:
			// pools=<N> is what actually bounds x265's own worker threads to the core
			// budget. ffmpeg's -threads (placed before -i) only bounds the DECODER — with
			// pools unset, x265 spins up a pool per NUMA node sized to the whole machine
			// and the "half the cores" setting did nothing for the encode itself.
			params += fmt.Sprintf(":pools=%d", cores)
		}
		// HDR params must be MERGED here, not appended as a second -x265-params: ffmpeg keeps
		// only the last occurrence, so two flags means one set is silently discarded.
		if hdrParams != "" {
			params += ":" + hdrParams
		}
		// Always 10-bit, even for 8-bit sources: x265's Main10 avoids banding that its 8-bit
		// path introduces in gradients, at no compatibility cost for HEVC playback.
		return []string{"-c:v", name, "-preset", "slow", "-crf", strconv.Itoa(crf),
			"-x265-params", params, "-pix_fmt", "yuv420p10le"}
	}
}
