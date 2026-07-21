// Package mediainfo reads real media properties from a file via ffprobe, so
// quality decisions and version routing don't have to trust the filename. When
// ffprobe is unavailable, callers fall back to filename parsing.
package mediainfo

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Info is the subset of media properties Arrmada cares about.
type Info struct {
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	Resolution  string   `json:"resolution"` // "2160p" | "1080p" | …
	VideoCodec  string   `json:"video_codec"`
	AudioCodec  string   `json:"audio_codec"`
	Channels    int      `json:"channels"`
	DurationSec int      `json:"duration_sec"`
	HDR         []string `json:"hdr,omitempty"`
}

// Available reports whether ffprobe is installed.
func Available() bool {
	_, err := exec.LookPath("ffprobe")
	return err == nil
}

// Probe runs ffprobe against path and returns its media info.
func Probe(path string) (Info, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", path)
	out, err := cmd.Output()
	if err != nil {
		return Info{}, err
	}
	return parse(out)
}

type ffprobeOut struct {
	Streams []struct {
		CodecType     string `json:"codec_type"`
		CodecName     string `json:"codec_name"`
		Width         int    `json:"width"`
		Height        int    `json:"height"`
		Channels      int    `json:"channels"`
		ColorTransfer string `json:"color_transfer"`
		Disposition   struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
		SideDataList []struct {
			SideDataType string `json:"side_data_type"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func parse(b []byte) (Info, error) {
	var o ffprobeOut
	if err := json.Unmarshal(b, &o); err != nil {
		return Info{}, err
	}
	var info Info
	for _, s := range o.Streams {
		switch s.CodecType {
		case "video":
			if s.Disposition.AttachedPic == 1 {
				continue // cover art is a video stream too — don't report the movie as MJPEG
			}
			if info.VideoCodec == "" { // first REAL video stream
				info.Width, info.Height = s.Width, s.Height
				info.VideoCodec = normalizeVideoCodec(s.CodecName)
				info.HDR = detectHDR(s.ColorTransfer, s.SideDataList)
			}
		case "audio":
			if info.AudioCodec == "" {
				info.AudioCodec = strings.ToUpper(s.CodecName)
				info.Channels = s.Channels
			}
		}
	}
	info.Resolution = resolutionFor(info.Width, info.Height)
	if d, err := strconv.ParseFloat(o.Format.Duration, 64); err == nil {
		info.DurationSec = int(d)
	}
	return info, nil
}

func normalizeVideoCodec(c string) string {
	switch strings.ToLower(c) {
	case "hevc", "h265":
		return "x265"
	case "h264", "avc":
		return "x264"
	case "av1":
		return "AV1"
	default:
		return c
	}
}

func detectHDR(transfer string, sideData []struct {
	SideDataType string `json:"side_data_type"`
}) []string {
	var hdr []string
	switch transfer {
	case "smpte2084":
		hdr = append(hdr, "HDR10")
	case "arib-std-b67":
		hdr = append(hdr, "HLG")
	}
	for _, sd := range sideData {
		if strings.Contains(strings.ToLower(sd.SideDataType), "dovi") || strings.Contains(strings.ToLower(sd.SideDataType), "dolby vision") {
			hdr = append(hdr, "DV")
		}
	}
	return hdr
}

// resolutionFor classifies by WIDTH (how release resolutions are defined) with
// height as a fallback — cinematic content is letterboxed, so a 1080p film is
// often 1920×800, not 1920×1080. Classifying by height would wrongly call it 720p.
func resolutionFor(w, h int) string {
	switch {
	case w >= 3000 || h >= 1700:
		return "2160p"
	case w >= 1800 || h >= 950:
		return "1080p"
	case w >= 1100 || h >= 650:
		return "720p"
	case h >= 560:
		return "576p"
	case w > 0 || h > 0:
		return "480p"
	default:
		return ""
	}
}
