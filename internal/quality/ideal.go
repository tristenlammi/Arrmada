package quality

import (
	"fmt"
	"strings"
)

// The ideal file. A profile decides which releases are grabbed; this describes the FILE
// the library should end up holding — "HEVC or AV1, HDR10+, Atmos, 20–30 Mb/s at 4K" — so
// the library tables can show, file by file, what doesn't fit and why. It is report-only:
// nothing here changes what is downloaded or converted.

// IdealFile is a profile's target file. Every part is optional; an empty part accepts
// anything.
type IdealFile struct {
	// Codecs the video may be in: "hevc", "av1", "h264".
	Codecs []string `json:"codecs,omitempty"`
	// HDR formats accepted: "SDR", "HDR10", "HDR10+", "HLG", "DV". A Dolby Vision file is
	// judged by the format under its Dolby Vision layer, unless "DV" itself is accepted —
	// so "HDR10+" accepts a Dolby Vision file whose base carries HDR10+.
	HDR []string `json:"hdr,omitempty"`
	// Atmos needs a Dolby Atmos track; Lossless a lossless one (TrueHD, DTS-HD MA, FLAC,
	// PCM).
	Atmos    bool `json:"atmos,omitempty"`
	Lossless bool `json:"lossless,omitempty"`
	// Bitrate is the whole file's average bitrate window per resolution ("2160p", "1080p",
	// "720p", "SD"), in Mb/s — the same figure the library tables show.
	Bitrate map[string]BitrateWindow `json:"bitrate,omitempty"`
}

// BitrateWindow is a Mb/s range; a zero bound is open.
type BitrateWindow struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// Empty reports whether nothing has been set up.
func (f IdealFile) Empty() bool {
	if len(f.Codecs) > 0 || len(f.HDR) > 0 || f.Atmos || f.Lossless {
		return false
	}
	for _, w := range f.Bitrate {
		if w.Min > 0 || w.Max > 0 {
			return false
		}
	}
	return true
}

// FileFacts is what a library file actually is, as far as the check cares.
type FileFacts struct {
	Resolution  string  `json:"resolution"` // "2160p" | "1080p" | "720p" | "SD"
	Codec       string  `json:"codec"`      // "hevc" | "av1" | "h264" | "other"
	HDR         string  `json:"hdr"`        // the picture's own format: SDR | HDR10 | HDR10+ | HLG
	DolbyVision bool    `json:"dolby_vision,omitempty"`
	Atmos       bool    `json:"atmos,omitempty"`
	Lossless    bool    `json:"lossless,omitempty"`
	BitrateMbps float64 `json:"bitrate_mbps"`
}

// Fit statuses, worst first.
const (
	FitOver     = "over"     // above the bitrate ceiling
	FitUnder    = "under"    // below the bitrate floor
	FitMismatch = "mismatch" // the bitrate's fine, something else isn't
	FitOK       = "fits"
)

// FitIssue is one way a file misses its ideal.
type FitIssue struct {
	Kind string `json:"kind"` // bitrate | resolution | codec | hdr | atmos | lossless
	Msg  string `json:"msg"`
}

// Fit is a file's verdict against its profile's ideal.
type Fit struct {
	Status string         `json:"status"`
	Window *BitrateWindow `json:"window,omitempty"` // the window that applied, if any
	Issues []FitIssue     `json:"issues,omitempty"`
}

// CheckFit judges one file against an ideal. allowed are the profile's resolutions
// (empty = any).
func CheckFit(ideal IdealFile, allowed []string, f FileFacts) Fit {
	fit := Fit{Status: FitOK}
	add := func(kind, msg string) { fit.Issues = append(fit.Issues, FitIssue{kind, msg}) }

	if len(allowed) > 0 && f.Resolution != "" && !resolutionAllowed(f.Resolution, allowed) {
		add("resolution", f.Resolution+" isn't one of this profile's resolutions")
	}
	if len(ideal.Codecs) > 0 && !containsFold(ideal.Codecs, f.Codec) {
		add("codec", fmt.Sprintf("%s, not %s", codecName(f.Codec), joinNames(ideal.Codecs, codecName)))
	}
	if len(ideal.HDR) > 0 && !hdrAccepted(ideal.HDR, f) {
		have := f.HDR
		if f.DolbyVision {
			have = "Dolby Vision (" + f.HDR + " base)"
		}
		add("hdr", fmt.Sprintf("%s, not %s", have, joinNames(ideal.HDR, hdrName)))
	}
	if ideal.Atmos && !f.Atmos {
		add("atmos", "no Dolby Atmos track")
	}
	if ideal.Lossless && !f.Lossless {
		add("lossless", "no lossless audio track")
	}
	mismatch := len(fit.Issues) > 0

	if w, ok := ideal.Bitrate[f.Resolution]; ok && (w.Min > 0 || w.Max > 0) {
		fit.Window = &w
		switch br := f.BitrateMbps; {
		case br <= 0:
			// Unknown bitrate: nothing to judge it by.
		case w.Max > 0 && br > w.Max:
			fit.Status = FitOver
			add("bitrate", fmt.Sprintf("%.1f Mb/s is over the %.0f Mb/s ceiling", br, w.Max))
		case w.Min > 0 && br < w.Min:
			fit.Status = FitUnder
			add("bitrate", fmt.Sprintf("%.1f Mb/s is under the %.0f Mb/s floor", br, w.Min))
		}
	}
	if fit.Status == FitOK && mismatch {
		fit.Status = FitMismatch
	}
	return fit
}

// resolutionAllowed matches a file's resolution against the profile's list, where "SD"
// covers the profile's 576p and 480p.
func resolutionAllowed(res string, allowed []string) bool {
	for _, a := range allowed {
		if a == res || (res == "SD" && (a == "576p" || a == "480p")) {
			return true
		}
	}
	return false
}

func hdrAccepted(accepted []string, f FileFacts) bool {
	if f.DolbyVision && containsFold(accepted, "DV") {
		return true
	}
	return containsFold(accepted, f.HDR)
}

func containsFold(xs []string, v string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func codecName(c string) string {
	switch strings.ToLower(c) {
	case "hevc":
		return "HEVC"
	case "av1":
		return "AV1"
	case "h264":
		return "H.264"
	case "", "other":
		return "another codec"
	}
	return strings.ToUpper(c)
}

func hdrName(h string) string {
	if strings.EqualFold(h, "DV") {
		return "Dolby Vision"
	}
	return h
}

func joinNames(xs []string, name func(string) string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = name(x)
	}
	if len(out) <= 1 {
		return strings.Join(out, "")
	}
	return strings.Join(out[:len(out)-1], ", ") + " or " + out[len(out)-1]
}
