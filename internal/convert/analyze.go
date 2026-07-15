// Package convert is the Convert module (Tdarr replacement): GPU-accelerated transcoding,
// remuxing and cleanup for the Movies & TV libraries. This first slice implements the
// analysis + safe conversion engine and the "Save space" preset (→ HEVC); the full rules
// engine, HDR/DV handling, quality gate and UI land in later phases (see CONVERT-BUILD-PLAN.md).
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
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Resolution  string  `json:"resolution"` // "2160p" | "1080p" | "720p" | "SD"
	HDR         string  `json:"hdr"`        // "SDR" | "HDR10" | "HDR10+" | "Dolby Vision"
	BitrateKbps int     `json:"bitrate_kbps"`
	FrameRate   float64 `json:"frame_rate"`
	DurationSec float64 `json:"duration_sec"`
	SizeBytes   int64   `json:"size_bytes"`
	AudioTracks int     `json:"audio_tracks"`
	SubTracks   int     `json:"sub_tracks"`
	TenBit      bool    `json:"ten_bit"`
	VFR         bool          `json:"vfr"` // variable frame rate → needs CFR conversion
	HasCC       bool          `json:"has_cc"` // embedded EIA/CEA-608/708 closed captions in the video
	Audio       []AudioStream `json:"audio,omitempty"`
	Subs        []SubStream   `json:"subs,omitempty"`
	HDR10       *HDR10Meta    `json:"hdr10,omitempty"` // static HDR10 metadata for passthrough (when present)
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
			CodecType     string `json:"codec_type"`
			CodecName     string `json:"codec_name"`
			Width         int    `json:"width"`
			Height        int    `json:"height"`
			PixFmt        string `json:"pix_fmt"`
			ColorTransfer string `json:"color_transfer"`
			RFrameRate     string `json:"r_frame_rate"`
			AvgFrameRate   string `json:"avg_frame_rate"`
			ClosedCaptions int    `json:"closed_captions"`
			Channels       int    `json:"channels"`
			Tags          struct {
				Language string `json:"language"`
			} `json:"tags"`
			SideDataList []struct {
				SideDataType string `json:"side_data_type"`
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
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			if mi.VideoCodec != "" {
				continue // first video stream wins
			}
			mi.VideoCodec = s.CodecName
			mi.Width, mi.Height = s.Width, s.Height
			mi.Resolution = resolutionLabel(s.Width, s.Height)
			mi.FrameRate = parseRate(s.RFrameRate)
			avg := parseRate(s.AvgFrameRate)
			// VFR when the nominal (r) and average frame rates differ meaningfully.
			mi.VFR = avg > 0 && mi.FrameRate > 0 && (mi.FrameRate-avg)/avg > 0.02
			mi.HasCC = s.ClosedCaptions == 1
			mi.TenBit = strings.Contains(s.PixFmt, "10") || strings.Contains(s.PixFmt, "p10")
			if s.ColorTransfer == "smpte2084" || s.ColorTransfer == "arib-std-b67" {
				mi.HDR = "HDR10"
			}
			for _, sd := range s.SideDataList {
				t := strings.ToLower(sd.SideDataType)
				if strings.Contains(t, "dolby vision") {
					mi.HDR = "Dolby Vision"
				} else if strings.Contains(t, "hdr dynamic") || strings.Contains(t, "hdr10+") {
					mi.HDR = "HDR10+"
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
	// For HDR files, pull the mastering-display + content-light values (so a transcode can
	// re-pass them; ffmpeg drops them on a naive re-encode) and detect a Dolby Vision RPU, which
	// ffmpeg exposes as frame — not stream — side data. One extra targeted probe, only for HDR.
	if mi.HDR == "HDR10" || mi.HDR == "HDR10+" || mi.HDR == "Dolby Vision" {
		meta, hasDV := probeHDR10(ctx, ffprobe, path)
		mi.HDR10 = meta
		if hasDV {
			mi.HDR = "Dolby Vision"
		}
	}
	return mi, nil
}

// probeHDR10 decodes one frame and reads its mastering-display + content-light side data
// (formatted the way x265's master-display / max-cll params expect), and reports whether the
// frame carries a Dolby Vision RPU. Meta is nil when no static metadata is present.
func probeHDR10(ctx context.Context, ffprobe, path string) (meta *HDR10Meta, hasDV bool) {
	out, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v",
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
		if strings.Contains(strings.ToLower(sd.SideDataType), "dolby vision") {
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
