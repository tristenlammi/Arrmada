package music

import (
	"path/filepath"
	"strings"
)

// Audio formats, uppercase. Used to classify releases and files on disk, and — via a
// profile's format_scores — to decide which releases are wanted.
//
// Lossless and lossy are tracked separately because the distinction is what a music quality
// profile is actually about: a FLAC is not "a bigger MP3", it's a different tier, and an
// upgrade from lossy to lossless is always worth taking regardless of bitrate.
var losslessFormats = map[string]bool{
	"FLAC": true, "ALAC": true, "WAV": true, "AIFF": true, "APE": true, "WV": true,
}

var lossyFormats = map[string]bool{
	"MP3": true, "AAC": true, "M4A": true, "OGG": true, "OPUS": true, "WMA": true,
}

// audioExts maps a file extension to its format tag.
var audioExts = map[string]string{
	".flac": "FLAC", ".alac": "ALAC", ".wav": "WAV", ".aiff": "AIFF", ".aif": "AIFF",
	".ape": "APE", ".wv": "WV",
	".mp3": "MP3", ".aac": "AAC", ".m4a": "M4A", ".ogg": "OGG", ".opus": "OPUS", ".wma": "WMA",
}

// IsLossless reports whether a format tag is a lossless codec.
func IsLossless(f string) bool { return losslessFormats[strings.ToUpper(f)] }

// IsLossy reports whether a format tag is a lossy codec.
func IsLossy(f string) bool { return lossyFormats[strings.ToUpper(f)] }

// IsAudioFormat reports whether a tag names an audio format we understand.
func IsAudioFormat(f string) bool {
	u := strings.ToUpper(f)
	return losslessFormats[u] || lossyFormats[u]
}

// IsAudioFile reports whether a path looks like an audio file by extension.
func IsAudioFile(path string) bool {
	_, ok := audioExts[strings.ToLower(filepath.Ext(path))]
	return ok
}

// FormatOf returns the uppercase format tag for a file path ("" when not audio).
func FormatOf(path string) string {
	return audioExts[strings.ToLower(filepath.Ext(path))]
}

// FormatRank orders formats for upgrade decisions: any lossless beats any lossy, and
// unknown ranks lowest. Within a tier the codecs are treated as equivalent — arguing that
// ALAC beats FLAC (or AAC beats MP3) at the same bitrate is noise, and the profile's
// format_scores is where a user expresses a real preference.
func FormatRank(f string) int {
	switch {
	case IsLossless(f):
		return 2
	case IsLossy(f):
		return 1
	default:
		return 0
	}
}

// BetterFormat reports whether candidate is a strict quality improvement over current:
// lossless beats lossy, and within the same tier a materially higher bitrate wins.
//
// The 10% margin keeps a 320 kbps re-rip from replacing a 319 kbps file forever — the same
// churn-avoidance the video upgrade path applies.
func BetterFormat(candFormat string, candBitrate int, curFormat string, curBitrate int) bool {
	cr, rr := FormatRank(candFormat), FormatRank(curFormat)
	if cr != rr {
		return cr > rr
	}
	if cr == 2 {
		return false // both lossless — nothing to gain, don't churn
	}
	return curBitrate > 0 && candBitrate > 0 && float64(candBitrate) >= float64(curBitrate)*1.10
}
