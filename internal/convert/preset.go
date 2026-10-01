package convert

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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

// twoToThree maps common ISO 639-1 codes to 639-2/T (terminological) so "en" matches an
// "eng" track. Bibliographic variants are folded in separately (biblioToTerm).
var twoToThree = map[string]string{
	"en": "eng", "es": "spa", "fr": "fra", "de": "deu", "it": "ita", "pt": "por",
	"nl": "nld", "sv": "swe", "pl": "pol", "ru": "rus", "tr": "tur", "ar": "ara",
	"hi": "hin", "ja": "jpn", "ko": "kor", "zh": "zho", "cs": "ces", "el": "ell",
	"da": "dan", "no": "nor", "fi": "fin", "he": "heb", "th": "tha", "vi": "vie",
	"uk": "ukr", "hu": "hun", "ro": "ron", "id": "ind", "ta": "tam", "te": "tel",
	"ms": "msa", "fa": "fas", "bn": "ben", "tl": "tgl", "is": "isl", "sk": "slk",
	"bg": "bul", "hr": "hrv", "sr": "srp", "sl": "slv", "et": "est", "lv": "lav",
	"lt": "lit", "ca": "cat", "eu": "eus", "gl": "glg", "cy": "cym",
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
// the Convert → GPU device setting picks which one.
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
// retention" should be. The quality gate still has the final word.
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
// quantizer index (0-255), which is what VAAPI's AV1 encoder takes. They're the same
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

// maxQualityCRF is the quality target for each codec — set for retention, not size. The
// scales differ (AV1's CRF runs higher for the same picture), so each has its own. The
// quality gate catches the rare file that still falls short and re-encodes it tighter.
func maxQualityCRF(codec string) int {
	if codec == "av1" {
		return 24
	}
	return 20
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

// keptAudio applies the plan's audio choices to the probed tracks. Shared by the compiler,
// the verification and the "needs work" check so they can never disagree about which
// tracks survive.
//
// Two rules make it impossible to end up with a silent file: commentary is only dropped
// when something else remains, and a language filter that matches nothing keeps every
// track (the tags are wrong, and no audio at all is worse than the wrong languages).
func keptAudio(mi *MediaInfo, plan Plan) []AudioStream {
	cand := mi.Audio
	if plan.Audio.DropCommentary {
		var nc []AudioStream
		for _, au := range mi.Audio {
			if !au.Commentary {
				nc = append(nc, au)
			}
		}
		if len(nc) > 0 {
			cand = nc
		}
	}
	if len(plan.Audio.KeepLangs) == 0 {
		return cand
	}
	wanted := append([]string{}, plan.Audio.KeepLangs...)
	if plan.Audio.OriginalLang != "" {
		wanted = append(wanted, plan.Audio.OriginalLang)
	}
	var keep []AudioStream
	for _, au := range cand {
		if langIn(au.Lang, wanted) {
			keep = append(keep, au)
		}
	}
	if len(keep) == 0 {
		return cand
	}
	return keep
}

// defaultAudio picks which kept track should be flagged default: the first one in the
// first language you listed, so players start in your language rather than whichever track
// the release happened to put first. -1 = leave the flags as they are.
func defaultAudio(kept []AudioStream, plan Plan) int {
	if len(plan.Audio.KeepLangs) == 0 {
		return -1
	}
	first := []string{plan.Audio.KeepLangs[0]}
	for i, au := range kept {
		l := strings.ToLower(strings.TrimSpace(au.Lang))
		if !au.Commentary && l != "" && l != "und" && langIn(au.Lang, first) {
			return i
		}
	}
	return -1
}

// keptSubs applies the plan's subtitle choices. With no filter and image subs kept, every
// track is kept, so the untouched path stays byte-identical.
func keptSubs(mi *MediaInfo, plan Plan) []SubStream {
	out := mi.Subs
	if len(plan.Subs.KeepLangs) > 0 {
		out = make([]SubStream, 0, len(mi.Subs))
		for _, s := range mi.Subs {
			if langIn(s.Lang, plan.Subs.KeepLangs) {
				out = append(out, s)
			}
		}
		// Never strip every subtitle with a language filter. If it matches nothing, the
		// tags are wrong or unexpected, and silently shipping a file with no subtitles at
		// all is worse than keeping the clutter.
		if len(out) == 0 {
			out = mi.Subs
		}
	}
	switch plan.Subs.ImageSubs {
	case ImageSubsRemove:
		// Asked for outright: every image track goes, even a language's only subtitle.
		// The Subtitles module can fetch or generate a text one in its place.
		kept := make([]SubStream, 0, len(out))
		for _, s := range out {
			if s.Text {
				kept = append(kept, s)
			}
		}
		return kept
	case ImageSubsWhenText:
		return dropCoveredImageSubs(out, plan.Subs.TextSidecarLangs)
	}
	return out
}

// dropCoveredImageSubs removes image tracks only where a text subtitle for that language
// will remain: an embedded text track that survived the language filter, or a sidecar. An
// untagged image track is dropped only if some text subtitle exists at all — we can't know
// its language, but we do know the viewer isn't left with nothing.
func dropCoveredImageSubs(out []SubStream, sidecarLangs []string) []SubStream {
	textLangs := map[string]bool{}
	anyText := false
	for _, s := range out {
		if s.Text {
			textLangs[normLang(s.Lang)] = true
			anyText = true
		}
	}
	for _, l := range sidecarLangs {
		textLangs[normLang(l)] = true
		anyText = true
	}
	kept := make([]SubStream, 0, len(out))
	for _, s := range out {
		if s.Text {
			kept = append(kept, s)
			continue
		}
		l := strings.ToLower(strings.TrimSpace(s.Lang))
		covered := anyText && (l == "" || l == "und" || textLangs[normLang(l)])
		if !covered {
			kept = append(kept, s) // the only subtitle in its language — stays
		}
	}
	return kept
}

// trackArgs maps the kept audio, subtitles and attachments of input `in` into the output.
// Audio and image subtitles are always COPIED — Atmos, TrueHD and DTS-HD pass through bit
// for bit. Used by the standard encode (in = 0) and by the HDR10+ pipeline's final mux,
// where the video comes from input 0 and everything else from the original (in = 1).
func trackArgs(mi *MediaInfo, plan Plan, in int) []string {
	var a []string
	keepAud := keptAudio(mi, plan)
	if len(keepAud) == len(mi.Audio) && defaultAudio(keepAud, plan) < 0 {
		a = append(a, "-map", fmt.Sprintf("%d:a?", in), "-c:a", "copy")
	} else {
		def := defaultAudio(keepAud, plan)
		for out, au := range keepAud {
			a = append(a, "-map", fmt.Sprintf("%d:a:%d", in, au.AudIndex))
			if def >= 0 {
				flag := "-default"
				if out == def {
					flag = "+default"
				}
				a = append(a, fmt.Sprintf("-disposition:a:%d", out), flag)
			}
		}
		a = append(a, "-c:a", "copy")
	}

	subs := keptSubs(mi, plan)
	if len(subs) == len(mi.Subs) {
		a = append(a, "-map", fmt.Sprintf("%d:s?", in), "-c:s", "copy")
		// Matroska can't mux MP4-family text subs (mov_text/tx3g): with -c:s copy every
		// conversion of an MP4-with-subs source fails at header write. Transcode just those
		// streams to SRT. All subs are mapped in order, so output sub N is input sub N.
		for _, s := range mi.Subs {
			if !mkvSub(s.Codec) {
				a = append(a, fmt.Sprintf("-c:s:%d", s.SubIndex), "srt")
			}
		}
	} else {
		// Filtered: map the kept streams individually. Output indexes RENUMBER from 0 as
		// they're mapped, so a per-stream codec override uses the output position.
		for out, s := range subs {
			a = append(a, "-map", fmt.Sprintf("%d:s:%d", in, s.SubIndex))
			if mkvSub(s.Codec) {
				a = append(a, fmt.Sprintf("-c:s:%d", out), "copy")
			} else {
				a = append(a, fmt.Sprintf("-c:s:%d", out), "srt")
			}
		}
	}

	// Attachments — embedded fonts, cover art. Once ANY -map is given, ffmpeg's default
	// stream selection is off, so without this every attachment is silently dropped: ASS/SSA
	// subtitles (anime especially) then render in a fallback font with the typesetting and
	// karaoke styling destroyed.
	a = append(a, "-map", fmt.Sprintf("%d:t?", in), "-c:t", "copy")
	return a
}

// compileOutputArgs turns a Plan into the ffmpeg output options: re-encode (or copy) the
// video to the target codec, then the kept tracks, metadata and chapters. Always Matroska.
func compileOutputArgs(enc Encoder, mi *MediaInfo, plan Plan, hwDecode bool, cores int, noNumaPools bool) []string {
	// Map the REAL video stream: cover art is a video stream too (attached_pic), so 0:v:0
	// isn't always the movie.
	a := []string{"-map", fmt.Sprintf("0:v:%d", mi.VideoIndex)}
	a = append(a, trackArgs(mi, plan, 0)...)
	a = append(a, videoArgs(enc, mi, plan, hwDecode, cores, noNumaPools)...)
	a = append(a, "-map_metadata", "0", "-map_chapters", "0")
	return a
}

// videoArgs is the video half of the command: copy, or encode with the chosen encoder.
func videoArgs(enc Encoder, mi *MediaInfo, plan Plan, hwDecode bool, cores int, noNumaPools bool) []string {
	if plan.VideoCodec == "" {
		return []string{"-c:v", "copy"}
	}
	codec := plan.VideoCodec
	crf := plan.Quality
	if crf <= 0 {
		crf = maxQualityCRF(codec)
	}
	var a []string
	if plan.VFRToCFR && mi.VFR {
		a = append(a, "-fps_mode", "cfr") // normalize VFR → prevents A/V desync
	}
	// Deinterlace first when the source is interlaced — encoding combed fields as
	// progressive frames bakes the combing in permanently.
	swVF := swFilterChain(mi)
	switch enc.Kind {
	case "vaapi": // AMD/Intel hardware
		if hwDecode {
			// Frames arrive as VAAPI surfaces straight from the hardware decoder.
			if mi.Interlaced {
				a = append(a, "-vf", "deinterlace_vaapi")
			}
		} else {
			pix := "nv12"
			if mi.TenBit {
				pix = "p010"
			}
			var chain []string
			if mi.Interlaced {
				chain = append(chain, deintFilter)
			}
			chain = append(chain, "format="+pix, "hwupload")
			a = append(a, "-vf", strings.Join(chain, ","))
		}
		// Quality in the ENCODER's own scale: hevc_vaapi takes -qp (0-52, close to CRF);
		// av1_vaapi has no -qp and quantizes on a 0-255 index via -global_quality.
		a = append(a, "-c:v", enc.Name, "-rc_mode", "CQP")
		if codec == "av1" {
			a = append(a, "-global_quality", strconv.Itoa(av1QIndex(hardwareQuality(crf))))
		} else {
			a = append(a, "-qp", strconv.Itoa(hardwareQuality(crf)))
		}
		if mi.TenBit {
			a = append(a, "-profile:v", "main10")
		}
		a = append(a, colourTagArgs(mi)...)
	case "nvenc":
		if swVF != "" {
			a = append(a, "-vf", swVF)
		}
		// -b:v 0 matters: NVENC's constant-quality mode is otherwise still capped by the
		// default average bitrate (2 Mb/s), which silently overrides the -cq target.
		a = append(a, "-c:v", enc.Name, "-preset", "p6", "-tune", "hq", "-rc", "vbr", "-cq", strconv.Itoa(hardwareQuality(crf)), "-b:v", "0",
			"-spatial-aq", "1", "-temporal-aq", "1", "-rc-lookahead", "32")
		if mi.TenBit {
			a = append(a, "-pix_fmt", "p010le")
		}
		a = append(a, colourTagArgs(mi)...)
	case "qsv":
		if swVF != "" {
			a = append(a, "-vf", swVF)
		}
		// QSV's -global_quality is ICQ on a 1-51 CRF-like scale for EVERY codec, av1_qsv
		// included. Look-ahead and adaptive quantisation keep dark and flat areas from being
		// starved — the blocky-shadows problem fixed-quality hardware encodes are known for.
		a = append(a, "-c:v", enc.Name, "-global_quality", strconv.Itoa(hardwareQuality(crf)), "-preset", "slower",
			"-look_ahead_depth", "40", "-extbrc", "1", "-adaptive_i", "1", "-adaptive_b", "1")
		if mi.TenBit {
			a = append(a, "-pix_fmt", "p010le")
		}
		a = append(a, colourTagArgs(mi)...)
	default: // CPU
		if swVF != "" {
			a = append(a, "-vf", swVF)
		}
		// Static HDR is re-passed: ffmpeg keeps the colour tags on a re-encode but drops the
		// mastering-display / max-cll. (HDR10+ is re-injected by its own pipeline.)
		hdrParams, colourTags := "", []string(nil)
		if isHDR(mi.EncodeHDR()) {
			switch codec {
			case "hevc":
				hdrParams, colourTags = hdr10Params(mi)
			case "av1":
				hdrParams, colourTags = av1HDRParams(mi)
			}
		}
		a = append(a, cpuVideoArgs(enc.Name, codec, crf, cores, hdrParams, noNumaPools)...)
		a = append(a, colourTags...)
	}
	return a
}

// trackSummary describes what a plan does to a file's tracks, for the job note and the log
// ("audio 5 → 2 · subtitles 31 → 1"). Empty when nothing changes.
func trackSummary(mi *MediaInfo, plan Plan) string {
	var parts []string
	if n := len(keptAudio(mi, plan)); n < len(mi.Audio) {
		parts = append(parts, fmt.Sprintf("audio %d → %d", len(mi.Audio), n))
	}
	if n := len(keptSubs(mi, plan)); n < len(mi.Subs) {
		parts = append(parts, fmt.Sprintf("subtitles %d → %d", len(mi.Subs), n))
	}
	return strings.Join(parts, " · ")
}

// planWarnings lists what running this plan on this file will lose that the user didn't
// explicitly ask to lose. They must never be silent.
func planWarnings(mi *MediaInfo, plan Plan) []string {
	var w []string
	if plan.VideoCodec != "" && mi.HasCC {
		w = append(w, "embedded closed captions (CEA-608/708) are lost on re-encode")
	}
	if plan.VideoCodec != "" && mi.HDR == "Dolby Vision" {
		w = append(w, "Dolby Vision layer dropped — kept as "+mi.EncodeHDR())
	}
	return w
}

// deintFilter is the software deinterlacer. send_frame keeps the frame count 1:1 with the
// source (bwdif's default, send_field, doubles the rate) — important both for A/V timing and
// for the HDR10+ pipeline, where per-frame metadata must stay aligned.
const deintFilter = "bwdif=mode=send_frame"

// swFilterChain builds the software video-filter chain for paths that feed the encoder
// system-memory frames: deinterlace when needed. "" = no filter.
func swFilterChain(mi *MediaInfo) string {
	if mi.Interlaced {
		return deintFilter
	}
	return ""
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

// hdr10Params re-applies static HDR to a libx265 encode: the BT.2020 colour tags plus the
// mastering-display + max-cll (hdr10=1 emits the SEI, repeat-headers keeps it on every IDR so
// seeking stays HDR-correct). HDR10+ dynamic metadata is re-embedded post-encode by its tool
// (the bundled x265 isn't built with dhdr10-info support).
func hdr10Params(mi *MediaInfo) (params string, colourTags []string) {
	// The transfer curve must follow the SOURCE. Every HLG file re-tagged as PQ plays back
	// with wrong brightness, washed out or crushed.
	trc := "smpte2084"
	if mi.EncodeHDR() == "HLG" {
		trc = "arib-std-b67"
	}
	params = "hdr10=1:repeat-headers=1:colorprim=bt2020:transfer=" + trc + ":colormatrix=bt2020nc"
	// Mastering display / max-cll describe an absolute-luminance (PQ) grade. HLG is relative
	// and self-describing, so it carries neither.
	if trc != "arib-std-b67" && mi.HDR10 != nil {
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
// mastering display / content light as metadata OBUs via -svtav1-params (verified against
// the bundled SVT-AV1 3.1.2: the values round-trip into the output).
func av1HDRParams(mi *MediaInfo) (params string, colourTags []string) {
	trc := "smpte2084"
	if mi.EncodeHDR() == "HLG" {
		trc = "arib-std-b67"
	}
	if trc != "arib-std-b67" && mi.HDR10 != nil {
		if mi.HDR10.MasterDisplay != "" {
			// SVT-AV1's mastering-display form uses FLOATS, while x265 takes the raw integer
			// units the probe records. The probed x265 string is converted, not reused.
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
// larger surface than "-preset slow -crf 20", and a failure there shouldn't cost the user
// the conversion when the simple form would have worked. HDR metadata, the core budget and
// AV1's dark-scene protection are NOT tuning and survive the strip (see stripTuningKeys).
func stripTuningParams(args []string) []string {
	// The hardware encoders' tuning flags, dropped whole (flag + value). Look-ahead and
	// adaptive quantisation depend on the driver and chip generation in ways a startup
	// test can't cover; the plain constant-quality encode is the dependable fallback.
	hwTuning := map[string]bool{
		"-look_ahead_depth": true, "-extbrc": true, "-adaptive_i": true, "-adaptive_b": true,
		"-spatial-aq": true, "-temporal-aq": true, "-rc-lookahead": true, "-tune": true,
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if hwTuning[args[i]] && i+1 < len(args) {
			i++
			continue
		}
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
// keeping everything that isn't tuning: the HDR metadata, the pools/lp core budget (dropping
// it can crash the encode or unbound it from the budget), and SVT-AV1's visual-quality mode
// and dark-scene protection (without them AV1 crushes shadows — measured, not assumed).
func stripTuningKeys(params string) string {
	keep := map[string]bool{
		"hdr10": true, "repeat-headers": true, "colorprim": true, "transfer": true,
		"colormatrix": true, "master-display": true, "max-cll": true, "chromaloc": true,
		"mastering-display": true, "content-light": true, // the SVT-AV1 spellings
		"pools": true, "lp": true,
		"bframes": true, // the HDR10+ pipeline's bframes=0 is a correctness requirement, not tuning
		"tune":    true, "enable-variance-boost": true, "luminance-qp-bias": true,
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

// av1QualityParams is SVT-AV1's quality configuration.
//
//	tune=0                   visual quality, not PSNR — the default visibly over-smooths
//	enable-variance-boost=1  gives flat and low-contrast areas (skies, walls, shadows) the
//	                         bits they need; off, SVT starves them
//	luminance-qp-bias=20     spends more on dark frames, where AV1 otherwise crushes blacks
//
// Measured on dark footage with the bundled SVT-AV1 3.1.2 at the same CRF: plain tune=0
// scored 29.3 dB XPSNR — visibly crushed shadows; with both protections, 36.0 dB.
const av1QualityParams = "tune=0:enable-variance-boost=1:luminance-qp-bias=20"

// cpuVideoArgs builds the CPU encoder args. cores bounds the encoder's own thread pool so a
// library conversion can't take the whole machine. Output is always 10-bit: the extra
// precision near-eliminates the banding on skies, smoke and dark gradients that is the first
// thing anyone notices in a re-encode, and it improves efficiency rather than costing any.
//
// -dolbyvision 0: this ffmpeg's x265 and SVT-AV1 wrappers default to copying a source's
// Dolby Vision RPU into the output. Dolby Vision is deliberately dropped (only its HDR10/
// HLG base is kept), so that passthrough is switched off explicitly.
func cpuVideoArgs(name, codec string, crf, cores int, hdrParams string, noNumaPools bool) []string {
	if codec == "av1" {
		params := fmt.Sprintf("%s:lp=%d", av1QualityParams, cores)
		if hdrParams != "" {
			params += ":" + hdrParams
		}
		return []string{"-c:v", name, "-preset", "5", "-crf", strconv.Itoa(crf), "-dolbyvision", "0",
			"-svtav1-params", params, "-pix_fmt", "yuv420p10le"}
	}
	// HEVC. preset slow, plus the params that matter for retaining detail rather than speed:
	//   aq-mode=3   better bit distribution in dark scenes and gradients
	//   psy-rd      preserves texture/grain the default happily smooths away
	//   no-sao      SAO is x265's classic detail-smearer at high quality
	//   rc-lookahead / bframes  more context for rate decisions
	params := "aq-mode=3:psy-rd=2.0:psy-rdoq=1.0:no-sao=1:bframes=8:rc-lookahead=40"
	switch {
	case noNumaPools:
		// Worker pools bind to NUMA nodes via set_mempolicy, which this environment denies.
		// Unpooled keeps frame-level parallelism and, unlike the default, finishes.
		params += ":pools=none"
	case cores > 0:
		// pools=<N> is what actually bounds x265's own worker threads to the core budget.
		params += fmt.Sprintf(":pools=%d", cores)
	}
	// HDR params must be MERGED here, not appended as a second -x265-params: ffmpeg keeps
	// only the last occurrence, so two flags means one set is silently discarded.
	if hdrParams != "" {
		params += ":" + hdrParams
	}
	return []string{"-c:v", name, "-preset", "slow", "-crf", strconv.Itoa(crf), "-dolbyvision", "0",
		"-x265-params", params, "-pix_fmt", "yuv420p10le"}
}

// sidecarLangs lists the languages with an external .srt next to path, by the
// "<name>.<lang>.srt" convention the Subtitles module writes. Cache is optional and keyed
// by directory: a season folder holds many episodes, and the index pass would otherwise
// ReadDir the same folder once per episode.
func sidecarLangs(path string, cache map[string][]string) []string {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	var names []string
	if cache != nil {
		if got, ok := cache[dir]; ok {
			names = got
		}
	}
	if names == nil {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		names = make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		}
		if cache != nil {
			cache[dir] = names
		}
	}
	var out []string
	for _, n := range names {
		ln := strings.ToLower(n)
		if !strings.HasSuffix(ln, ".srt") || !strings.HasPrefix(ln, base+".") {
			continue
		}
		// "<base>.<lang>.srt", possibly "<base>.<lang>.forced.srt": take the segment
		// right after the base.
		rest := strings.TrimSuffix(strings.TrimPrefix(ln, base+"."), ".srt")
		if lang, _, _ := strings.Cut(rest, "."); lang != "" && len(lang) <= 3 {
			out = append(out, lang)
		}
	}
	return out
}

// withSidecars returns the plan with TextSidecarLangs filled in for one file.
func withSidecars(plan Plan, path string, cache map[string][]string) Plan {
	plan.Subs.TextSidecarLangs = sidecarLangs(path, cache)
	return plan
}
