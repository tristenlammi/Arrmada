// Package convert is the Convert module (Tdarr replacement): GPU-accelerated transcoding,
// remuxing and cleanup for the Movies & TV libraries. This first slice implements the
// analysis + safe conversion engine and the "Save space" preset (→ HEVC); the full rules
// engine, HDR/DV handling, quality gate and UI land in later phases.
package convert

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// MediaInfo is the probed spec of a single media file.
type MediaInfo struct {
	Container   string  `json:"container"`
	VideoCodec  string  `json:"video_codec"`
	VideoIndex  int     `json:"video_index,omitempty"` // position among VIDEO streams (the N in 0:v:N) — nonzero when cover art precedes the movie
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Resolution  string  `json:"resolution"`           // "2160p" | "1080p" | "720p" | "SD"
	HDR         string  `json:"hdr"`                  // "SDR" | "HDR10" | "HDR10+" | "Dolby Vision"
	DVProfile   int     `json:"dv_profile,omitempty"` // Dolby Vision profile (5, 7, 8…); 0 = unknown / not DV
	BitrateKbps int     `json:"bitrate_kbps"`
	FrameRate   float64 `json:"frame_rate"`
	// FrameRateRat is the frame rate as ffprobe's exact rational (e.g. "24000/1001"), from
	// avg_frame_rate. Anywhere a rate is stamped onto a stream this is the value to use —
	// a %.6g float of 23.976… re-times every frame slightly and drifts over a feature.
	FrameRateRat string  `json:"frame_rate_rat,omitempty"`
	DurationSec  float64 `json:"duration_sec"`
	SizeBytes    int64   `json:"size_bytes"`
	AudioTracks  int     `json:"audio_tracks"`
	SubTracks    int     `json:"sub_tracks"`
	TenBit       bool    `json:"ten_bit"`
	Interlaced   bool    `json:"interlaced,omitempty"` // field_order says the content is interlaced → needs deinterlacing
	VFR          bool    `json:"vfr"`                  // variable frame rate → needs CFR conversion
	HasCC        bool    `json:"has_cc"`               // embedded EIA/CEA-608/708 closed captions in the video
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
}

// AudioStream is one audio track. AudIndex is its position among audio streams (the N in
// ffmpeg's "0:a:N").
type AudioStream struct {
	AudIndex int    `json:"aud_index"`
	Codec    string `json:"codec"`
	Lang     string `json:"lang"`
	Channels int    `json:"channels"`
}

// SubStream is one subtitle track. SubIndex is its position among subtitle streams (the N
// in ffmpeg's "0:s:N"). Text subs can be extracted to SRT; image subs (PGS/VOBSUB) can't
// without OCR.
type SubStream struct {
	SubIndex int    `json:"sub_index"`
	Codec    string `json:"codec"`
	Lang     string `json:"lang"`
	Text     bool   `json:"text"`
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
	cmd := exec.CommandContext(ctx, ffprobe,
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
			AvgFrameRate   string `json:"avg_frame_rate"`
			ClosedCaptions int    `json:"closed_captions"`
			Channels       int    `json:"channels"`
			Disposition    struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
			Tags struct {
				Language string `json:"language"`
			} `json:"tags"`
			SideDataList []struct {
				SideDataType string `json:"side_data_type"`
				DVProfile    int    `json:"dv_profile"` // set on the DOVI configuration record
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
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
			mi.HasCC = s.ClosedCaptions == 1
			mi.ColorPrimaries, mi.ColorTransfer, mi.ColorSpace = s.ColorPrimaries, s.ColorTransfer, s.ColorSpace
			// >8-bit, not just 10-bit: a 12-bit source matched neither substring, so it was
			// treated as 8-bit and truncated on encode.
			mi.TenBit = strings.Contains(s.PixFmt, "10") || strings.Contains(s.PixFmt, "p10") ||
				strings.Contains(s.PixFmt, "12") || strings.Contains(s.PixFmt, "14") ||
				strings.Contains(s.PixFmt, "16")
			if s.ColorTransfer == "smpte2084" || s.ColorTransfer == "arib-std-b67" {
				// HLG is its own format, not HDR10. Folding it in meant the encode re-tagged
				// it with PQ's transfer curve, which visibly breaks the picture.
				if s.ColorTransfer == "arib-std-b67" {
					mi.HDR = "HLG"
				} else {
					mi.HDR = "HDR10"
				}
			}
			for _, sd := range s.SideDataList {
				t := strings.ToLower(sd.SideDataType)
				switch {
				// ffprobe's stream-level name is "DOVI configuration record", so matching only
				// "dolby vision" (the FRAME-level name) made this branch dead code.
				case strings.Contains(t, "dolby vision") || strings.Contains(t, "dovi"):
					mi.HDR = "Dolby Vision"
					if sd.DVProfile > 0 {
						mi.DVProfile = sd.DVProfile
					}
				case strings.Contains(t, "hdr dynamic") || strings.Contains(t, "hdr10+"):
					if mi.HDR != "Dolby Vision" { // DV+HDR10+ discs exist; DV routing wins
						mi.HDR = "HDR10+"
					}
				}
			}
		case "audio":
			mi.Audio = append(mi.Audio, AudioStream{
				AudIndex: mi.AudioTracks, Codec: s.CodecName, Lang: s.Tags.Language, Channels: s.Channels,
			})
			mi.AudioTracks++
		case "subtitle":
			mi.Subs = append(mi.Subs, SubStream{
				SubIndex: mi.SubTracks, Codec: s.CodecName, Lang: s.Tags.Language, Text: textSubCodecs[s.CodecName],
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
		if hasDV {
			mi.HDR = "Dolby Vision"
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
	out, err := exec.CommandContext(ctx, ffprobe, "-v", "error",
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
		switch sd.SideDataType {
		case "Mastering display metadata":
			// x265 wants the raw numerators (colour coords in 0.00002 units, luminance in
			// 0.0001 cd/m² units) — exactly the numerators ffprobe reports as num/den.
			m.MasterDisplay = fmt.Sprintf("G(%s,%s)B(%s,%s)R(%s,%s)WP(%s,%s)L(%s,%s)",
				ratNum(sd.GreenX), ratNum(sd.GreenY), ratNum(sd.BlueX), ratNum(sd.BlueY),
				ratNum(sd.RedX), ratNum(sd.RedY), ratNum(sd.WhitePointX), ratNum(sd.WhitePointY),
				ratNum(sd.MaxLuminance), ratNum(sd.MinLuminance))
		case "Content light level metadata":
			m.MaxCLL = fmt.Sprintf("%d,%d", sd.MaxContent, sd.MaxAverage)
		}
	}
	if m.MasterDisplay == "" && m.MaxCLL == "" {
		return nil, hasDV
	}
	return m, hasDV
}

// ratNum returns the numerator of a "num/den" rational string (ffprobe reports HDR coordinates
// this way); a bare integer is returned as-is.
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
