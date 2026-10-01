package convert

import "strings"

// estimatePlanSize predicts the output size of running a Plan on a file. Audio is always
// copied, so the dropped tracks come straight off the total; the video is predicted from
// the bitrate a transparent encode typically needs at this resolution and frame rate.
//
// The old flat "H.264 × 0.55" guess was wrong in both directions at once: it promised a
// 45% saving on a lean 2 Mb/s encode that barely shrinks, and badly under-promised on a
// 70 Mb/s remux that shrinks by two thirds. Only a real encode gives the true number — the
// per-file comparison does that — but this is close enough to rank files by.
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
		if v := predictedVideoBytes(mi, plan.VideoCodec, dur); v > 0 && v < srcVideo {
			targetVideo = v
		}
	}
	var targetAudio int64
	for _, au := range keptAudio(mi, plan) {
		targetAudio += audioBytes(au.Codec, au.Channels, dur)
	}
	return targetVideo + targetAudio
}

// predictedVideoBytes is the video size a transparent encode of this file typically lands
// at: bits per pixel per frame by resolution (bigger frames compress better per pixel),
// times the frame rate and runtime. AV1 is about a fifth smaller than HEVC at equal quality.
func predictedVideoBytes(mi *MediaInfo, codec string, dur float64) int64 {
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
	if codec == "av1" {
		bpp *= 0.8
	}
	return int64(bpp * float64(mi.Width*mi.Height) * fps * dur / 8)
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
