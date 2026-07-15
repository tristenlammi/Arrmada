package convert

import "strings"

// estimatePlanSize predicts the output size of running a Plan on a file. It's plan-aware
// (unlike a flat ratio): it splits the source into estimated video vs audio bytes, applies a
// per-codec transcode ratio to the video, and adjusts the audio for language-strip / stereo-
// add / loudnorm. For a content-exact number the UI runs a 30s sample (see SampleEstimate).
func estimatePlanSize(mi *MediaInfo, plan Plan) int64 {
	if mi == nil || mi.SizeBytes <= 0 {
		return 0
	}
	dur := mi.DurationSec
	if dur <= 0 {
		dur = 1
	}

	// Split the source: estimate audio bytes, the rest is (video + small overhead).
	var srcAudio int64
	for _, au := range mi.Audio {
		srcAudio += audioBytes(au.Codec, au.Channels, dur)
	}
	srcVideo := mi.SizeBytes - srcAudio
	if srcVideo < 0 {
		srcVideo = mi.SizeBytes // audio estimate overshot — fall back
		srcAudio = 0
	}

	// Target video: apply the source→target codec ratio + a mild quality adjustment, then the
	// downscale area ratio (bytes scale roughly with pixel count).
	vr := codecRatio(mi.VideoCodec, plan.VideoCodec, plan.Quality)
	if plan.VideoCodec == "" {
		vr = 1 // remux keeps the video stream as-is
	}
	if plan.ScaleHeight > 0 && mi.Height > plan.ScaleHeight {
		ratio := float64(plan.ScaleHeight) / float64(mi.Height)
		vr *= ratio * ratio
	}
	targetVideo := int64(float64(srcVideo) * vr)

	// Target audio: which tracks survive, each copied / normalized, plus stereo adds.
	var targetAudio int64
	keep := mi.Audio
	if len(plan.Audio.KeepLangs) > 0 {
		var filtered []AudioStream
		for _, au := range mi.Audio {
			if langIn(au.Lang, plan.Audio.KeepLangs) {
				filtered = append(filtered, au)
			}
		}
		if len(filtered) > 0 {
			keep = filtered
		}
	}
	for _, au := range keep {
		if plan.Audio.Loudnorm {
			targetAudio += kbpsBytes(256, dur) // re-encoded to AAC 256k
		} else {
			targetAudio += audioBytes(au.Codec, au.Channels, dur)
		}
		if plan.Audio.AddStereo && au.Channels > 2 {
			targetAudio += kbpsBytes(192, dur) // added AAC 2.0 downmix
		}
	}

	return targetVideo + targetAudio
}

// codecRatio is the rough output/source byte ratio for a video transcode, adjusted for
// quality (baseline CRF 24). Lower CRF (higher quality) → larger.
func codecRatio(srcCodec, targetCodec string, quality int) float64 {
	base := 0.55 // default h264 → hevc
	switch strings.ToLower(srcCodec) {
	case "mpeg2video", "mpeg1video":
		base = 0.30
	case "mpeg4", "msmpeg4v3", "vc1", "wmv3":
		base = 0.45
	case "h264", "avc":
		base = 0.55
	case "vp9", "vp8":
		base = 0.70
	}
	if targetCodec == "av1" {
		base *= 0.80 // AV1 is ~20% smaller than HEVC at the same quality
	}
	if quality > 0 {
		base *= 1 + float64(24-quality)*0.045 // CRF 20 → +18%, CRF 28 → −18%
	}
	if base < 0.1 {
		base = 0.1
	}
	return base
}

// audioBytes estimates a track's byte size from its codec + channel count over a duration.
func audioBytes(codec string, channels int, durationSec float64) int64 {
	if channels <= 0 {
		channels = 2
	}
	var kbps int
	switch strings.ToLower(codec) {
	case "truehd", "mlp":
		kbps = 4000
	case "dts", "dca":
		kbps = 1500
	case "flac", "alac":
		kbps = 900 * channels
	case "pcm_s16le", "pcm_s24le":
		kbps = 1536 * channels / 2
	case "eac3":
		kbps = ac3Kbps(channels, 640, 256)
	case "ac3":
		kbps = ac3Kbps(channels, 448, 192)
	case "aac", "opus", "vorbis", "mp3":
		kbps = 128 * maxi(1, channels/2)
	default:
		kbps = 256
	}
	return kbpsBytes(kbps, durationSec)
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
