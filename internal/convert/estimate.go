package convert

import "strings"

// estimatePlanSize predicts the output size of running a Plan on a file. Audio is always
// copied, so the dropped tracks come straight off the total; the video is predicted from
// the file's own bitrate (see predictedVideoBytes). Only a real encode gives the true
// number — Compare does that — so this errs on the side of promising less.
func estimatePlanSize(mi *MediaInfo, plan Plan) int64 {
	if mi == nil || mi.SizeBytes <= 0 {
		return 0
	}
	dur := mi.DurationSec
	if dur <= 0 {
		dur = 1
	}
	var srcAudio int64
	for _, au := range mi.Audio {
		srcAudio += audioBytes(au.Codec, au.Channels, dur)
	}
	srcVideo := mi.SizeBytes - srcAudio
	if srcVideo < 0 {
		srcVideo, srcAudio = mi.SizeBytes, 0
	}

	targetVideo := srcVideo
	if plan.VideoCodec != "" {
		if v := predictedVideoBytes(mi, plan.VideoCodec, srcVideo, dur); v > 0 && v < srcVideo {
			targetVideo = v
		}
	}
	var targetAudio int64
	for _, au := range keptAudio(mi, plan) {
		targetAudio += audioBytes(au.Codec, au.Channels, dur)
	}
	return targetVideo + targetAudio
}

// predictedVideoBytes is the video size a transparent encode of this file is expected to
// land at, from the file's OWN bitrate: a faithful x265 encode keeps about 55% of an HEVC
// remux's video bitrate, 50% of an H.264's, and 35% of MPEG-2 / VC-1 (much older, much less
// efficient codecs). That scales with how hard the content is — a grainy film carries a
// high bitrate and keeps a high one.
//
// The first version predicted from the resolution alone, a fixed bitrate per pixel: every
// 4K film came out near 7 Mb/s, so a grainy 113 GB remux was "15 GB at the same quality" —
// not a number anyone should believe. The resolution figure survives only as a FLOOR: no
// encode goes below what a clean picture of that size needs, which is what keeps a lean
// file from looking like it would shrink. AV1 is estimated 15% under HEVC.
func predictedVideoBytes(mi *MediaInfo, codec string, srcVideo int64, dur float64) int64 {
	if srcVideo <= 0 {
		return 0
	}
	ratio := 0.35
	switch codecClass(mi.VideoCodec) {
	case "hevc", "vp9":
		ratio = 0.55
	case "h264":
		ratio = 0.50
	}
	out := float64(srcVideo) * ratio
	if floor := floorVideoBytes(mi, dur); floor > out {
		out = floor
	}
	if codec == "av1" {
		out *= 0.85
	}
	return int64(out)
}

// floorVideoBytes is the least a transparent encode of a clean picture needs at this
// resolution and frame rate (bits per pixel per frame; bigger frames compress better).
func floorVideoBytes(mi *MediaInfo, dur float64) float64 {
	if mi.Width <= 0 || mi.Height <= 0 {
		return 0
	}
	fps := mi.FrameRate
	if fps <= 0 {
		fps = 24
	}
	bpp := 0.06
	switch px := mi.Width * mi.Height; {
	case px >= 3200*1600:
		bpp = 0.035
	case px >= 1700*900:
		bpp = 0.06
	case px >= 1100*600:
		bpp = 0.08
	default:
		bpp = 0.10
	}
	return bpp * float64(mi.Width*mi.Height) * fps * dur / 8
}

// audioKbps estimates a track's bitrate from its codec + channel count.
func audioKbps(codec string, channels int) int {
	if channels <= 0 {
		channels = 2
	}
	switch strings.ToLower(codec) {
	case "truehd", "mlp":
		return 4000
	case "dts", "dca":
		return 1500
	case "flac", "alac":
		return 900 * channels
	case "pcm_s16le", "pcm_s24le", "pcm_bluray", "pcm_dvd":
		return 1536 * channels / 2
	case "eac3":
		return ac3Kbps(channels, 640, 256)
	case "ac3":
		return ac3Kbps(channels, 448, 192)
	case "aac", "opus", "vorbis", "mp3":
		return 128 * maxi(1, channels/2)
	}
	return 256
}

// audioBytes estimates a track's byte size over a duration.
func audioBytes(codec string, channels int, durationSec float64) int64 {
	return kbpsBytes(audioKbps(codec, channels), durationSec)
}

func ac3Kbps(channels, surround, stereo int) int {
	if channels > 2 {
		return surround
	}
	return stereo
}

func kbpsBytes(kbps int, durationSec float64) int64 {
	return int64(float64(kbps) * 1000 / 8 * durationSec)
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}
