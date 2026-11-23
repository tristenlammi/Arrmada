package convert

// analyze.go probes a file (ffprobe) into MediaInfo: video, HDR / Dolby Vision base, audio
// and subtitle tracks. The package doc lives in service.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/toolrun"
)

// MediaInfo is the probed spec of a single media file.
type MediaInfo struct {
	Container  string `json:"container"`
	VideoCodec string `json:"video_codec"`
	VideoIndex int    `json:"video_index,omitempty"` // position among VIDEO streams (the N in 0:v:N) — nonzero when cover art precedes the movie
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Resolution string `json:"resolution"`           // "2160p" | "1080p" | "720p" | "SD"
	HDR        string `json:"hdr"`                  // "SDR" | "HDR10" | "HDR10+" | "HLG" | "Dolby Vision"
	DVProfile  int    `json:"dv_profile,omitempty"` // Dolby Vision profile (5, 7, 8…); 0 = unknown / not DV
	// DVCompat is the configuration record's base-layer compatibility id: 1 = an HDR10 base
	// (profile 8.1), 4 = HLG (8.4), 6 = a profile 7 HDR10 base; 0 = no record.
	DVCompat int `json:"dv_compat,omitempty"`
	// DVBase is the HDR format UNDER a Dolby Vision layer — what a conversion keeps once the
	// DV layer is dropped: "HDR10" (profiles 7 and 8.1), "HDR10+" (when the base also carries
	// dynamic metadata), "HLG" (8.4), or "SDR" when the base isn't a standard HDR grade at all
	// (profile 5's IPT-PQ-c2, which can't be converted without wrecking the colour).
	DVBase      string  `json:"dv_base,omitempty"`
	BitrateKbps int     `json:"bitrate_kbps"`
	FrameRate   float64 `json:"frame_rate"`
	// FrameRateRat is the frame rate as ffprobe's exact rational (e.g. "24000/1001"), from
	// avg_frame_rate. Anywhere a rate is stamped onto a stream this is the value to use —
	// a %.6g float of 23.976… re-times every frame slightly and drifts over a feature.
	FrameRateRat string  `json:"frame_rate_rat,omitempty"`
	DurationSec  float64 `json:"duration_sec"`
	// VideoStartSec is where the video starts relative to the file (an audio track that
	// begins a frame earlier puts it at ~0.042). A conversion that rebuilds the video's
	// timestamps — the pieced, resumable encode (chunk.go) — restores this offset, or the
	// sound would sit that far off the picture.
	VideoStartSec float64 `json:"video_start_sec,omitempty"`
	SizeBytes     int64   `json:"size_bytes"`
	AudioTracks   int     `json:"audio_tracks"`
	SubTracks     int     `json:"sub_tracks"`
	TenBit        bool    `json:"ten_bit"`
	Interlaced    bool    `json:"interlaced,omitempty"` // field_order says the content is interlaced → needs deinterlacing
	VFR           bool    `json:"vfr"`                  // variable frame rate → needs CFR conversion
	HasCC         bool    `json:"has_cc"`               // embedded EIA/CEA-608/708 closed captions in the video
	// Frames is the video stream's frame count as the container states it (MP4 nb_frames,
	// Matroska NUMBER_OF_FRAMES); 0 when it doesn't. VideoSec is the video stream's own
	// length (0 = unknown), which can differ from DurationSec when an audio track runs on.
	// Both are for the full-decode check, which compares the frames it decodes with them.
	Frames   int64   `json:"frames,omitempty"`
	VideoSec float64 `json:"video_sec,omitempty"`
	// Colour tags as probed (empty / "unknown" when untagged). Re-asserted on hardware
	// encodes, which don't always forward them.
	ColorPrimaries string        `json:"color_primaries,omitempty"`
	ColorTransfer  string        `json:"color_transfer,omitempty"`
	ColorSpace     string        `json:"color_space,omitempty"`
	Audio          []AudioStream `json:"audio,omitempty"`
	Subs           []SubStream   `json:"subs,omitempty"`
	HDR10          *HDR10Meta    `json:"hdr10,omitempty"` // static HDR10 metadata for passthrough (when present)
}

// HDR10Meta is the static HDR10 mastering metadata, pre-formatted for x265 so it can be
// re-passed on transcode (ffmpeg loses it on a naive re-encode). Nil unless the file carries it.
type HDR10Meta struct {
	MasterDisplay string `json:"master_display"` // x265 form: G(gx,gy)B(bx,by)R(rx,ry)WP(wx,wy)L(max,min)
	MaxCLL        string `json:"max_cll"`        // "max_content,max_average", e.g. "1000,400"
	dynamic       bool   // the first frame carries HDR10+ (probe only; not stored)
}

// AudioStream is one audio track. AudIndex is its position among audio streams (the N in
// ffmpeg's "0:a:N").
type AudioStream struct {
	AudIndex   int    `json:"aud_index"`
	Codec      string `json:"codec"`
	Lang       string `json:"lang"`
	Channels   int    `json:"channels"`
	Title      string `json:"title,omitempty"`
	Commentary bool   `json:"commentary,omitempty"` // a commentary track (flagged, or titled so)
	// Atmos and Lossless come from the codec and ffprobe's profile ("Dolby TrueHD + Dolby
	// Atmos", "DTS-HD MA"), for the library's ideal-file check.
	Atmos    bool `json:"atmos,omitempty"`
	Lossless bool `json:"lossless,omitempty"`
	// Sec is the track's own length as the container states it (0 = unknown). The check
	// before a swap compares each copied track with the original's, so a copy cut short
	// can't pass on its count alone.
	Sec float64 `json:"sec,omitempty"`
}

// audioTraits reads Atmos and lossless from an audio stream's codec, profile and title.
func audioTraits(codec, profile, title string) (atmos, lossless bool) {
	p := strings.ToLower(profile)
	atmos = strings.Contains(p, "atmos") || strings.Contains(strings.ToLower(title), "atmos")
	switch c := strings.ToLower(codec); {
	case c == "truehd" || c == "mlp" || c == "flac" || c == "alac" || strings.HasPrefix(c, "pcm_"):
		lossless = true
	case c == "dts":
		lossless = strings.Contains(p, "dts-hd ma") // DTS-HD HRA and core DTS are lossy
	}
	return atmos, lossless
}

// SubStream is one subtitle track. SubIndex is its position among subtitle streams (the N
// in ffmpeg's "0:s:N"). Text subs can be extracted to SRT; image subs (PGS/VOBSUB) can't
// without OCR.
type SubStream struct {
	SubIndex int    `json:"sub_index"`
	Codec    string `json:"codec"`
	Lang     string `json:"lang"`
	Text     bool   `json:"text"`
	// Forced is a foreign-dialogue-only track (a few lines, never the full subtitle),
	// from the disposition flag or a title saying so.
	Forced bool   `json:"forced,omitempty"`
	Title  string `json:"title,omitempty"`
	// HearingImpaired is the disposition flag for an SDH track (a title saying so counts
	// too: see isSDH). Older cache rows lack it and fall back on the title.
	HearingImpaired bool `json:"hearing_impaired,omitempty"`
}

// EncodeHDR is the HDR format a conversion carries forward. For everything but Dolby Vision
// that's the file's own format; a Dolby Vision file keeps its base layer — the DV layer itself
// is dropped, unless the "keep" choice re-injects it around the encode (dv.go).
func (mi *MediaInfo) EncodeHDR() string {
	if mi.HDR == "Dolby Vision" {
		if mi.DVBase == "" {
			return "HDR10" // probed before DVBase existed; the common base, re-probed at job time
		}
		return mi.DVBase
	}
	return mi.HDR
}

// DVUnconvertible reports a Dolby Vision file whose base layer isn't a standard picture
// (profile 5). Dropping the DV layer from one of those leaves colours that are simply wrong
// on every display, so it's left alone.
func (mi *MediaInfo) DVUnconvertible() bool {
	return mi.HDR == "Dolby Vision" && (mi.DVProfile == 5 || mi.DVBase == "SDR")
}

// textSubCodecs are subtitle codecs we can extract to SRT.
var textSubCodecs = map[string]bool{
	"subrip": true, "srt": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true,
}

// mediaSpec formats a file's key attributes into one readable line for the activity log — the
// before/after picture the user asked for: codec, geometry, HDR, bit depth, bitrate, frame rate,
// audio tracks (codec + channel layout) and subtitle tracks (text vs image).
func mediaSpec(mi *MediaInfo) string {
	if mi == nil {
		return "unknown"
	}
	parts := []string{strings.ToUpper(mi.VideoCodec)}
	if mi.Width > 0 && mi.Height > 0 {
		geo := fmt.Sprintf("%d×%d", mi.Width, mi.Height)
		if mi.Resolution != "" {
			geo += " (" + mi.Resolution + ")"
		}
		parts = append(parts, geo)
	}
	hdr := mi.HDR
	if hdr == "" {
		hdr = "SDR"
	}
	depth := "8-bit"
	if mi.TenBit {
		depth = "10-bit"
	}
	parts = append(parts, hdr, depth)
	if mi.BitrateKbps > 0 {
		parts = append(parts, fmt.Sprintf("%.1f Mb/s", float64(mi.BitrateKbps)/1000))
	}
	if mi.FrameRate > 0 {
		fps := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", mi.FrameRate), "0"), ".") + " fps"
		if mi.VFR {
			fps += " VFR"
		}
		if mi.Interlaced {
			fps += " interlaced"
		}
		parts = append(parts, fps)
	}
	parts = append(parts, "audio: "+audioSummary(mi.Audio, mi.AudioTracks))
	parts = append(parts, "subs: "+subSummary(mi.Subs, mi.SubTracks))
	return strings.Join(parts, " · ")
}

// audioSummary lists each audio track as "CODEC layout [lang]", falling back to a plain count when
// the detailed stream list isn't available.
func audioSummary(tracks []AudioStream, count int) string {
	if len(tracks) == 0 {
		if count == 0 {
			return "none"
		}
		return fmt.Sprintf("%d track(s)", count)
	}
	out := make([]string, 0, len(tracks))
	for _, a := range tracks {
		label := strings.ToUpper(a.Codec)
		if lay := channelLayout(a.Channels); lay != "" {
			label += " " + lay
		}
		if a.Lang != "" && a.Lang != "und" {
			label += " [" + a.Lang + "]"
		}
		out = append(out, label)
	}
	return strings.Join(out, ", ")
}

// channelLayout maps a channel count to its common name (5.1, 7.1, …); "" for unknown.
func channelLayout(ch int) string {
	switch ch {
	case 1:
		return "mono"
	case 2:
		return "2.0"
	case 3:
		return "2.1"
	case 6:
		return "5.1"
	case 7:
		return "6.1"
	case 8:
		return "7.1"
	case 0:
		return ""
	default:
		return fmt.Sprintf("%dch", ch)
	}
}

// subSummary reports the subtitle track count split by text vs image (PGS/VOBSUB), which is what
// matters for Plex compatibility.
func subSummary(subs []SubStream, count int) string {
	if len(subs) == 0 {
		if count == 0 {
			return "none"
		}
		return fmt.Sprintf("%d track(s)", count)
	}
	text, img := 0, 0
	for _, s := range subs {
		if s.Text {
			text++
		} else {
			img++
		}
	}
	kinds := make([]string, 0, 2)
	if text > 0 {
		kinds = append(kinds, fmt.Sprintf("%d text", text))
	}
	if img > 0 {
		kinds = append(kinds, fmt.Sprintf("%d image", img))
	}
	if len(kinds) == 0 {
		return fmt.Sprintf("%d", len(subs))
	}
	return fmt.Sprintf("%d (%s)", len(subs), strings.Join(kinds, ", "))
}

// probe runs ffprobe and parses a file's spec.
func probe(ctx context.Context, ffprobe, path string) (*MediaInfo, error) {
	cmd := toolrun.Command(ctx, toolrun.Normal, ffprobe,
		"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var raw struct {
		Streams []struct {
			CodecType      string `json:"codec_type"`
			CodecName      string `json:"codec_name"`
			Width          int    `json:"width"`
			Height         int    `json:"height"`
			PixFmt         string `json:"pix_fmt"`
			ColorTransfer  string `json:"color_transfer"`
			ColorPrimaries string `json:"color_primaries"`
			ColorSpace     string `json:"color_space"`
			FieldOrder     string `json:"field_order"`
			RFrameRate     string `json:"r_frame_rate"`
			StartTime      string `json:"start_time"`
			AvgFrameRate   string `json:"avg_frame_rate"`
			ClosedCaptions int    `json:"closed_captions"`
			Channels       int    `json:"channels"`
			Profile        string `json:"profile"`
			Disposition    struct {
				AttachedPic int `json:"attached_pic"`
				Comment     int `json:"comment"`
				Forced      int `json:"forced"`
				Hearing     int `json:"hearing_impaired"`
			} `json:"disposition"`
			Tags struct {
				Language string `json:"language"`
				Title    string `json:"title"`
				// Matroska statistics tags (mkvmerge writes the -eng form, ffmpeg the plain one).
				NumberOfFrames    string `json:"NUMBER_OF_FRAMES"`
				NumberOfFramesEng string `json:"NUMBER_OF_FRAMES-eng"`
				Duration          string `json:"DURATION"`
				DurationEng       string `json:"DURATION-eng"`
			} `json:"tags"`
			NbFrames     string `json:"nb_frames"` // MP4 and friends
			Duration     string `json:"duration"`
			SideDataList []struct {
				SideDataType string `json:"side_data_type"`
				DVProfile    int    `json:"dv_profile"` // set on the DOVI configuration record
				DVCompat     int    `json:"dv_bl_signal_compatibility_id"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
			StartTime  string `json:"start_time"`
			Size       string `json:"size"`
			BitRate    string `json:"bit_rate"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	mi := &MediaInfo{Container: firstContainer(raw.Format.FormatName), HDR: "SDR"}
	mi.DurationSec, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	mi.SizeBytes, _ = strconv.ParseInt(raw.Format.Size, 10, 64)
	if br, _ := strconv.Atoi(raw.Format.BitRate); br > 0 {
		mi.BitrateKbps = br / 1000
	}
	videoSeen := 0
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			vidx := videoSeen
			videoSeen++
			// Cover art is a video stream too (disposition attached_pic). Picking it as THE
			// video meant "converting" a JPEG and mapping the wrong stream.
			if s.Disposition.AttachedPic == 1 {
				continue
			}
			if mi.VideoCodec != "" {
				continue // first real video stream wins
			}
			mi.VideoIndex = vidx
			mi.VideoCodec = s.CodecName
			if vs, err := strconv.ParseFloat(s.StartTime, 64); err == nil {
				fs, _ := strconv.ParseFloat(raw.Format.StartTime, 64)
				mi.VideoStartSec = max(vs-fs, 0)
			}
			mi.Width, mi.Height = s.Width, s.Height
			mi.Resolution = resolutionLabel(s.Width, s.Height)
			// Interlaced content needs a deinterlace filter or the encode bakes combing in.
			switch s.FieldOrder {
			case "tt", "bb", "tb", "bt":
				mi.Interlaced = true
			}
			r := parseRate(s.RFrameRate)
			avg := parseRate(s.AvgFrameRate)
			// Prefer the average rate for display/stamping: for interlaced streams r_frame_rate
			// is often the FIELD rate (2× the frame rate), and for VFR it's the max rate.
			mi.FrameRate = avg
			mi.FrameRateRat = s.AvgFrameRate
			if avg <= 0 {
				mi.FrameRate = r
				mi.FrameRateRat = s.RFrameRate
			}
			mi.VFR = detectVFR(r, avg, mi.Interlaced)
			mi.Frames = firstInt(s.NbFrames, s.Tags.NumberOfFrames, s.Tags.NumberOfFramesEng)
			mi.VideoSec = firstDuration(s.Duration, s.Tags.Duration, s.Tags.DurationEng)
			mi.HasCC = s.ClosedCaptions == 1
			mi.ColorPrimaries, mi.ColorTransfer, mi.ColorSpace = s.ColorPrimaries, s.ColorTransfer, s.ColorSpace
			// >8-bit, not just 10-bit: a 12-bit source matched neither substring, so it was
			// treated as 8-bit and truncated on encode.
			mi.TenBit = strings.Contains(s.PixFmt, "10") || strings.Contains(s.PixFmt, "p10") ||
				strings.Contains(s.PixFmt, "12") || strings.Contains(s.PixFmt, "14") ||
				strings.Contains(s.PixFmt, "16")
			base := "SDR"
			if s.ColorTransfer == "smpte2084" || s.ColorTransfer == "arib-std-b67" {
				// HLG is its own format, not HDR10. Folding it in meant the encode re-tagged
				// it with PQ's transfer curve, which visibly breaks the picture.
				if s.ColorTransfer == "arib-std-b67" {
					base = "HLG"
				} else {
					base = "HDR10"
				}
			}
			dv := false
			for _, sd := range s.SideDataList {
				t := strings.ToLower(sd.SideDataType)
				switch {
				// ffprobe's stream-level name is "DOVI configuration record", so matching only
				// "dolby vision" (the FRAME-level name) made this branch dead code.
				case strings.Contains(t, "dolby vision") || strings.Contains(t, "dovi"):
					dv = true
					if sd.DVProfile > 0 {
						mi.DVProfile, mi.DVCompat = sd.DVProfile, sd.DVCompat
					}
				case strings.Contains(t, "hdr dynamic") || strings.Contains(t, "hdr10+"):
					if base == "HDR10" {
						base = "HDR10+" // DV+HDR10+ discs exist: the base keeps its HDR10+
					}
				}
			}
			mi.HDR = base
			if dv {
				mi.HDR, mi.DVBase = "Dolby Vision", base
			}
		case "audio":
			atmos, lossless := audioTraits(s.CodecName, s.Profile, s.Tags.Title)
			mi.Audio = append(mi.Audio, AudioStream{
				Atmos: atmos, Lossless: lossless,
				AudIndex: mi.AudioTracks, Codec: s.CodecName, Lang: s.Tags.Language, Channels: s.Channels,
				Title:      s.Tags.Title,
				Commentary: s.Disposition.Comment == 1 || strings.Contains(strings.ToLower(s.Tags.Title), "commentary"),
				// The plain DURATION tag first: ffmpeg writes it for each track of the
				// Matroska it makes, while a copied track carries the source's DURATION-eng
				// along unchanged — which would vouch for audio that was cut short.
				Sec: firstDuration(s.Duration, s.Tags.Duration, s.Tags.DurationEng),
			})
			mi.AudioTracks++
		case "subtitle":
			mi.Subs = append(mi.Subs, SubStream{
				SubIndex: mi.SubTracks, Codec: s.CodecName, Lang: s.Tags.Language, Text: textSubCodecs[s.CodecName],
				// Many releases mark forced tracks only in the title. Older cache rows lack
				// the title and keep the flag alone; a new probe picks it up.
				Forced:          s.Disposition.Forced == 1 || strings.Contains(strings.ToLower(s.Tags.Title), "forced"),
				Title:           s.Tags.Title,
				HearingImpaired: s.Disposition.Hearing == 1,
			})
			mi.SubTracks++
		}
	}
	// Pull the mastering-display + content-light values (so a transcode can re-pass them;
	// ffmpeg drops them on a naive re-encode) and detect a Dolby Vision RPU, which ffmpeg
	// exposes as frame — not stream — side data. The probe runs for ANY 10-bit HEVC stream,
	// not just transfer-flagged ones: Dolby Vision profile 5 typically has
	// color_transfer=unspecified, so gating on PQ let it slip through as SDR into hardware
	// encodes that strip the RPU (green/purple output, original recycled).
	if mi.HDR == "HDR10" || mi.HDR == "HDR10+" || mi.HDR == "Dolby Vision" ||
		(mi.TenBit && mi.VideoCodec == "hevc") {
		meta, hasDV := probeHDR10(ctx, ffprobe, path, mi.VideoIndex)
		mi.HDR10 = meta
		if meta != nil && meta.dynamic {
			if mi.HDR == "HDR10" {
				mi.HDR = "HDR10+"
			} else if mi.HDR == "Dolby Vision" && mi.DVBase == "HDR10" {
				mi.DVBase = "HDR10+"
			}
		}
		if hasDV && mi.HDR != "Dolby Vision" {
			mi.HDR, mi.DVBase = "Dolby Vision", mi.HDR
		}
	}
	return mi, nil
}

// detectVFR decides whether a stream is really variable-frame-rate from its nominal (r) and
// average frame rates. Requires a coherent avg (VFR sources sometimes report avg 0/0 — that's
// "unknown", not evidence), and ignores the classic 2× artifact on interlaced streams, where
// r_frame_rate is the FIELD rate: 59.94 fields vs 29.97 frames is CFR, not VFR.
func detectVFR(r, avg float64, interlaced bool) bool {
	if r <= 0 || avg <= 0 {
		return false
	}
	if (r-avg)/avg <= 0.02 {
		return false
	}
	if ratio := r / avg; interlaced && ratio > 1.9 && ratio < 2.1 {
		return false
	}
	return true
}

// probeHDR10 decodes one frame of video stream videoIndex and reads its mastering-display +
// content-light side data (formatted the way x265's master-display / max-cll params expect),
// and reports whether the frame carries a Dolby Vision RPU. Meta is nil when no static
// metadata is present.
func probeHDR10(ctx context.Context, ffprobe, path string, videoIndex int) (meta *HDR10Meta, hasDV bool) {
	out, err := toolrun.Command(ctx, toolrun.Normal, ffprobe, "-v", "error",
		"-select_streams", fmt.Sprintf("v:%d", videoIndex),
		"-read_intervals", "%+#1", "-show_frames", "-print_format", "json", path).Output()
	if err != nil {
		return nil, false
	}
	var raw struct {
		Frames []struct {
			SideDataList []struct {
				SideDataType string `json:"side_data_type"`
				RedX         string `json:"red_x"`
				RedY         string `json:"red_y"`
				GreenX       string `json:"green_x"`
				GreenY       string `json:"green_y"`
				BlueX        string `json:"blue_x"`
				BlueY        string `json:"blue_y"`
				WhitePointX  string `json:"white_point_x"`
				WhitePointY  string `json:"white_point_y"`
				MinLuminance string `json:"min_luminance"`
				MaxLuminance string `json:"max_luminance"`
				MaxContent   int    `json:"max_content"`
				MaxAverage   int    `json:"max_average"`
			} `json:"side_data_list"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(out, &raw); err != nil || len(raw.Frames) == 0 {
		return nil, false
	}
	m := &HDR10Meta{}
	for _, sd := range raw.Frames[0].SideDataList {
		t := strings.ToLower(sd.SideDataType)
		if strings.Contains(t, "dolby vision") || strings.Contains(t, "dovi") {
			hasDV = true
		}
		// This ffprobe reports HDR10+ on the frame ("HDR Dynamic Metadata SMPTE2094-40
		// (HDR10+)"). The conversion still confirms it by extracting the metadata, but the
		// lists can now say HDR10+ instead of HDR10.
		if strings.Contains(t, "2094-40") || strings.Contains(t, "hdr10+") {
			m.dynamic = true
		}
		switch sd.SideDataType {
		case "Mastering display metadata":
			// x265 wants colour coords in 0.00002 units and luminance in 0.0001 cd/m² units.
			// HEVC and Matroska store exactly those (ffprobe reports n/50000 and n/10000); AV1
			// stores other fractions (n/65536, n/256), so each value is scaled, not just read
			// off its numerator.
			c, l := func(s string) string { return ratUnits(s, 50000) }, func(s string) string { return ratUnits(s, 10000) }
			m.MasterDisplay = fmt.Sprintf("G(%s,%s)B(%s,%s)R(%s,%s)WP(%s,%s)L(%s,%s)",
				c(sd.GreenX), c(sd.GreenY), c(sd.BlueX), c(sd.BlueY),
				c(sd.RedX), c(sd.RedY), c(sd.WhitePointX), c(sd.WhitePointY),
				l(sd.MaxLuminance), l(sd.MinLuminance))
		case "Content light level metadata":
			m.MaxCLL = fmt.Sprintf("%d,%d", sd.MaxContent, sd.MaxAverage)
		}
	}
	if m.MasterDisplay == "" && m.MaxCLL == "" && !m.dynamic {
		return nil, hasDV
	}
	return m, hasDV
}

// ratUnits converts a "num/den" rational (ffprobe reports HDR coordinates this way) into
// whole units of 1/scale, rounded: "13250/50000" at 50000 is "13250", "17367/65536" is
// "13250". A bare integer, or a rational that can't be read, gives ratNum's answer.
func ratUnits(s string, scale int64) string {
	n, d, ok := strings.Cut(s, "/")
	if !ok {
		return ratNum(s)
	}
	num, err1 := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
	den, err2 := strconv.ParseInt(strings.TrimSpace(d), 10, 64)
	if err1 != nil || err2 != nil || den <= 0 {
		return ratNum(s)
	}
	if den == scale {
		return strconv.FormatInt(num, 10)
	}
	return strconv.FormatInt(int64(math.Round(float64(num)*float64(scale)/float64(den))), 10)
}

// ratNum returns the numerator of a "num/den" rational string; a bare integer is returned
// as-is.
func ratNum(s string) string {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "0"
	}
	return s
}

func firstContainer(formatName string) string {
	f := strings.SplitN(formatName, ",", 2)[0]
	switch f {
	case "matroska", "webm":
		return "MKV"
	case "mov", "mp4", "m4a", "3gp", "3g2", "mj2":
		return "MP4"
	}
	return strings.ToUpper(f)
}

// resolutionLabel classifies by width primarily — a 1080p movie is 1920 wide but often only
// ~800px tall (2.40:1), so a height-only rule would mislabel it 720p. Height is a fallback.
func resolutionLabel(w, h int) string {
	switch {
	case w >= 3200 || h >= 1700:
		return "2160p"
	case w >= 1800 || h >= 950:
		return "1080p"
	case w >= 1200 || h >= 650:
		return "720p"
	case w > 0 || h > 0:
		return "SD"
	}
	return ""
}

// firstInt is the first of a probe's numeric strings that holds a positive whole number.
func firstInt(vals ...string) int64 {
	for _, v := range vals {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// firstDuration is the first of a probe's durations that parses, in seconds: plain seconds
// ("7199.958000", ffprobe's stream duration) or a clock ("01:59:59.958000000", the
// Matroska DURATION tag).
func firstDuration(vals ...string) float64 {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if sec, err := strconv.ParseFloat(v, 64); err == nil && sec > 0 {
			return sec
		}
		parts := strings.Split(v, ":")
		if len(parts) != 3 {
			continue
		}
		h, e1 := strconv.Atoi(parts[0])
		m, e2 := strconv.Atoi(parts[1])
		sec, e3 := strconv.ParseFloat(parts[2], 64)
		if e1 == nil && e2 == nil && e3 == nil && h >= 0 && m >= 0 && sec >= 0 {
			if t := float64(h*3600+m*60) + sec; t > 0 {
				return t
			}
		}
	}
	return 0
}

func parseRate(r string) float64 {
	parts := strings.SplitN(r, "/", 2)
	if len(parts) != 2 {
		return 0
	}
	num, _ := strconv.ParseFloat(parts[0], 64)
	den, _ := strconv.ParseFloat(parts[1], 64)
	if den == 0 {
		return 0
	}
	return num / den
}
