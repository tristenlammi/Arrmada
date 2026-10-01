package convert

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/movies"
)

// The decision layer: what the settings say, whether a file is worth converting, and the
// plan that brings it to the target. Everything that lists, counts, ranks or converts
// files goes through here, so they can never disagree.

const (
	keyAuto            = "convert_auto"               // the switch: convert the library automatically
	keySweepStart      = "convert_sweep_start"        // encode hours start "HH:MM" (empty = any time)
	keySweepEnd        = "convert_sweep_end"          // encode hours end   "HH:MM"
	keyAllowAV1        = "convert_allow_av1"          // your devices play AV1, so it may be chosen per file
	keyUseGPU          = "convert_use_gpu"            // encode on the GPU: much faster, somewhat bigger
	keyPauseWatching   = "convert_pause_watching"     // pause while someone is watching Plex
	keyKeepAudioLangs  = "convert_keep_audio_langs"   // CSV; empty = keep all audio
	keyKeepOrigLang    = "convert_keep_original_lang" // also keep the title's original-language audio
	keyDropCommentary  = "convert_drop_commentary"    // remove commentary audio tracks
	keyKeepSubLangs    = "convert_keep_sub_langs"     // CSV; empty = keep all subtitles
	keyImageSubs       = "convert_image_subs"         // keep | when_text | remove
	keyScratchDir      = "convert_scratch_dir"        // transcode working dir override
	keyVaapiDevice     = "convert_vaapi_device"       // which /dev/dri/renderD* hardware encodes on
	keyCPUCores        = "convert_cpu_cores"          // max cores a CPU encode may use (0 = half the box)
	keyWorkers         = "convert_workers"            // concurrent conversions (default 1)
	keyScanAt          = "convert_scan_at"            // "HH:MM" — when the daily library index sweep runs
	keyReclaimed       = "convert_reclaimed_bytes"    // running total of space saved
	keyLegacyTarget    = "convert_target_codec"       // pre-rebuild: "av1" meant AV1 for everything
	keyLegacyDropImage = "convert_drop_image_subs"    // pre-rebuild bool for image subtitles
)

// The quality bar and the rules around it are fixed, not settings: they ARE the promise.
const (
	minSSIM        = 0.97 // an encode must score at least this against its source
	minSavingPct   = 20   // a re-encode must save at least this much, or the original stays
	maxFailures    = 3    // after this many failed attempts a file is left alone
	qualityRetries = 2    // re-encodes at a higher quality before giving up on a file
)

// prefs is every setting the decision layer reads, read once so a loop over thousands of
// files doesn't hit the settings store per row.
type prefs struct {
	auto, allowAV1, useGPU, pauseWatching bool
	start, end                            string
	keepAudio                             []string
	keepOriginal, dropCommentary          bool
	keepSubs                              []string
	imageSubs                             string
}

func (s *Service) prefs(ctx context.Context) prefs {
	g := s.settings
	p := prefs{
		auto:           g.GetBool(ctx, keyAuto, false),
		allowAV1:       g.GetBool(ctx, keyAllowAV1, g.Get(ctx, keyLegacyTarget, "") == "av1"),
		useGPU:         g.GetBool(ctx, keyUseGPU, false),
		pauseWatching:  g.GetBool(ctx, keyPauseWatching, true),
		start:          g.Get(ctx, keySweepStart, ""),
		end:            g.Get(ctx, keySweepEnd, ""),
		keepAudio:      splitCSV(g.Get(ctx, keyKeepAudioLangs, "")),
		keepOriginal:   g.GetBool(ctx, keyKeepOrigLang, true),
		dropCommentary: g.GetBool(ctx, keyDropCommentary, false),
		keepSubs:       splitCSV(g.Get(ctx, keyKeepSubLangs, "")),
		imageSubs:      g.Get(ctx, keyImageSubs, ""),
	}
	switch p.imageSubs {
	case ImageSubsKeep, ImageSubsWhenText, ImageSubsRemove:
	default:
		// Before the three-way choice this was a bool, on by default.
		p.imageSubs = ImageSubsKeep
		if g.GetBool(ctx, keyLegacyDropImage, true) {
			p.imageSubs = ImageSubsWhenText
		}
	}
	return p
}

// cacheKey identifies the settings that change which files need work, for cached views.
func (p prefs) cacheKey() string {
	return fmt.Sprintf("%v|%v|%s|%v|%s|%s", p.allowAV1, p.keepOriginal, strings.Join(p.keepAudio, ","),
		p.dropCommentary, strings.Join(p.keepSubs, ","), p.imageSubs)
}

// Needs is the gap between a file and the target.
type Needs struct {
	Video bool `json:"video"` // the picture is worth re-encoding
	Subs  bool `json:"subs"`  // carries subtitle tracks that go
	Audio bool `json:"audio"` // carries audio tracks that go
	// Why explains a file whose video is left alone ("already efficient · 9.8 Mb/s").
	Why string `json:"why,omitempty"`
}

// Any reports whether the file falls short of the target at all.
func (n Needs) Any() bool { return n.Video || n.Subs || n.Audio }

// RemuxOnly reports whether the gap can be closed by copying the video — only the tracks
// are wrong. That's minutes and no quality loss, versus hours and a re-encode.
func (n Needs) RemuxOnly() bool { return !n.Video && (n.Subs || n.Audio) }

// The thresholds that decide whether a picture is worth re-encoding, in bits per pixel per
// frame — the bitrate a file spends relative to its resolution and frame rate. That, not
// the codec name, is what says whether a file is wasteful.
//
//   - Older codecs (H.264, MPEG-2, VC-1…) below leanLegacyBPP are already lean: a 1080p
//     H.264 at 2 Mb/s would shrink a little and lose a generation of quality for it.
//   - Modern codecs (HEVC, VP9) are only worth re-encoding when they're bloated — a 60 Mb/s
//     4K remux is close to the disc, so encoding it is a first generation of compression,
//     not a second. A 15 Mb/s HEVC WEB-DL is already efficient and stays as it is.
//   - AV1 is the most efficient there is; it's never re-encoded.
const (
	leanLegacyBPP    = 0.05 // ≈ 2.5 Mb/s at 1080p24
	bloatedModernBPP = 0.15 // ≈ 7.5 Mb/s at 1080p24, 30 Mb/s at 4K24
)

// videoBitsPerPixel is the video's bits per pixel per frame, with the audio estimate taken
// off the container bitrate. 0 when it can't be worked out.
func videoBitsPerPixel(mi *MediaInfo) (bpp float64, videoKbps int) {
	if mi.Width <= 0 || mi.Height <= 0 || mi.BitrateKbps <= 0 {
		return 0, 0
	}
	videoKbps = mi.BitrateKbps
	for _, au := range mi.Audio {
		videoKbps -= audioKbps(au.Codec, au.Channels)
	}
	if videoKbps <= 0 {
		videoKbps = mi.BitrateKbps * 9 / 10
	}
	fps := mi.FrameRate
	if fps <= 0 {
		fps = 24
	}
	return float64(videoKbps) * 1000 / (float64(mi.Width*mi.Height) * fps), videoKbps
}

// videoWorth decides whether a file's picture is worth re-encoding, and if not, why not.
func videoWorth(mi *MediaInfo) (bool, string) {
	if mi == nil || mi.VideoCodec == "" {
		return false, ""
	}
	if mi.DVUnconvertible() {
		return false, "Dolby Vision profile 5 — its picture can't be kept without the Dolby Vision layer"
	}
	class := codecClass(mi.VideoCodec)
	if class == "av1" {
		return false, "already AV1"
	}
	modern := class == "hevc" || class == "vp9"
	bpp, kbps := videoBitsPerPixel(mi)
	if bpp <= 0 {
		// Can't judge the bitrate: convert the old codecs (that's what they usually need),
		// leave the modern ones.
		if modern {
			return false, "already " + strings.ToUpper(mi.VideoCodec)
		}
		return true, ""
	}
	rate := fmt.Sprintf("%.1f Mb/s", float64(kbps)/1000)
	if modern {
		if bpp < bloatedModernBPP {
			return false, "already efficient · " + strings.ToUpper(mi.VideoCodec) + " " + rate
		}
		return true, ""
	}
	if bpp < leanLegacyBPP {
		return false, "already lean · " + rate
	}
	return true, ""
}

// needsOf is THE definition of "this file doesn't match the target". Every list, roll-up,
// stat and the runner's picker call it. plan must already be the file's own plan (its
// sidecars and original language filled in).
func needsOf(mi *MediaInfo, plan Plan) Needs {
	if mi == nil {
		return Needs{}
	}
	worth, why := videoWorth(mi)
	return Needs{
		Video: worth,
		Why:   why,
		Subs:  len(keptSubs(mi, plan)) < len(mi.Subs),
		Audio: len(keptAudio(mi, plan)) < len(mi.Audio),
	}
}

// basePlan is the part of every file's plan that comes from the settings alone.
func (p prefs) basePlan() Plan {
	return Plan{
		Audio: AudioPlan{KeepLangs: p.keepAudio, DropCommentary: p.dropCommentary},
		Subs:  SubPlan{KeepLangs: p.keepSubs, ImageSubs: p.imageSubs},
	}
}

// planFor builds one file's plan and its gap. The video codec here is the LIKELY target —
// the runner settles it per file at conversion time (HDR10+ forces HEVC; with AV1 allowed,
// a side-by-side test picks). A file whose picture is fine gets a copy plan that only
// rewrites the tracks: minutes, and bit-for-bit identical video.
func (p prefs) planFor(mi *MediaInfo, path, origLang string, dirCache map[string][]string) (Plan, Needs) {
	plan := withSidecars(p.basePlan(), path, dirCache)
	if p.keepOriginal {
		plan.Audio.OriginalLang = origLang
	}
	n := needsOf(mi, plan)
	if n.Video {
		plan.VideoCodec = p.likelyCodec(mi)
		plan.Quality = maxQualityCRF(plan.VideoCodec)
		plan.VFRToCFR = true
	}
	return plan, n
}

// likelyCodec is the codec a file will probably end up in, for estimates: AV1 when it's
// allowed and the file carries nothing AV1 can't hold, else HEVC.
func (p prefs) likelyCodec(mi *MediaInfo) string {
	if p.allowAV1 && mi.EncodeHDR() != "HDR10+" {
		return "av1"
	}
	return "hevc"
}

// movieOrigLang is a movie's original language (TMDB), or "" when unknown.
func movieOrigLang(m movies.Movie) string {
	if m.Extra != nil {
		return m.Extra.OriginalLanguage
	}
	return ""
}

// splitCSV parses a comma-separated setting into trimmed, non-empty values.
func splitCSV(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// codecClass buckets a codec name.
func codecClass(c string) string {
	switch strings.ToLower(c) {
	case "h264", "avc", "avc1":
		return "h264"
	case "hevc", "h265", "hev1", "hvc1":
		return "hevc"
	case "av1", "av01":
		return "av1"
	case "vp9", "vp09":
		return "vp9"
	default:
		return "other"
	}
}

// isHDR reports whether a probed HDR label means the file carries HDR metadata.
func isHDR(hdr string) bool { return hdr != "" && hdr != "SDR" }

// cpuCores is how many cores a CPU encode may use. Default is half the machine: encoding a
// library takes a long time on a server that is also doing other things, so taking the
// whole box is never the right default. Combined with the lowest scheduling priority, a CPU
// encode fills idle capacity rather than competing with Plex.
func (s *Service) cpuCores(ctx context.Context) int {
	n, _ := strconv.Atoi(s.settings.Get(ctx, keyCPUCores, "0"))
	if n <= 0 {
		n = runtime.NumCPU() / 2
	}
	if n < 1 {
		n = 1
	}
	if max := runtime.NumCPU(); n > max {
		n = max
	}
	return n
}

// maxWorkers caps concurrent conversions. More than a few only makes each one slower.
const maxWorkers = 4

// workerCount reads the configured concurrency (clamped to a sane range).
func (s *Service) workerCount(ctx context.Context) int {
	n, _ := strconv.Atoi(s.settings.Get(ctx, keyWorkers, "1"))
	if n < 1 {
		n = 1
	}
	if n > maxWorkers {
		n = maxWorkers
	}
	return n
}
