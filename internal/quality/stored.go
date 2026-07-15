package quality

import (
	"fmt"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// MediaType scopes a profile to a module (the Quality tabs).
const (
	MediaMovie  = "movie"
	MediaSeries = "series"
	MediaBook   = "book"
	MediaMusic  = "music"
)

// StoredProfile is a user-defined (or preset-derived) quality profile as it is
// persisted and edited in the builder. It projects to a runtime Profile plus a
// per-profile engine that also knows the profile's custom formats.
type StoredProfile struct {
	ID                 int64          `json:"id"`
	MediaType          string         `json:"media_type"`
	Name               string         `json:"name"`
	Base               string         `json:"base,omitempty"`
	AllowedResolutions []string       `json:"allowed_resolutions"`
	MinSource          string         `json:"min_source"`
	MaxSource          string         `json:"max_source"`
	BitrateCapMbps     float64        `json:"bitrate_cap_mbps"` // reject releases above this average bitrate (0 = no cap)
	SmallBias          float64        `json:"small_bias"`
	MinFormatScore     int            `json:"min_format_score"`
	FormatScores       map[string]int `json:"format_scores"`
	CustomFormats      []CustomFormat `json:"custom_formats,omitempty"`
	Keywords           []Keyword      `json:"keywords,omitempty"`    // scored terms matched in the release name
	Rejected           []string       `json:"rejected,omitempty"`    // hard-reject terms (incl. file types)
	MinSeeders         int            `json:"min_seeders"`           // reject releases below this seeder count
	StallMinutes       int            `json:"stall_minutes"`         // 0 = off; else fail-over after this long
	UpgradesEnabled    bool           `json:"upgrades_enabled"`      // keep seeking a better release after import
	UpgradeGB          float64        `json:"upgrade_gb"`            // also upgrade if a release is ≥ this many GB bigger (0 = quality-only)
}

// Keyword scores releases whose name contains Term (case-insensitive). Positive
// prefers, negative avoids.
type Keyword struct {
	Term  string `json:"term"`
	Score int    `json:"score"`
}

// ToProfile projects the stored form into the runtime scoring Profile.
func (sp StoredProfile) ToProfile() Profile {
	res := make([]parser.Resolution, 0, len(sp.AllowedResolutions))
	for _, r := range sp.AllowedResolutions {
		res = append(res, parser.Resolution(r))
	}
	return Profile{
		Name:               sp.Name,
		AllowedResolutions: res,
		MinSource:          parser.Source(sp.MinSource),
		MaxSource:          parser.Source(sp.MaxSource),
		BitrateCapMbps:     sp.BitrateCapMbps,
		SmallBias:          sp.SmallBias,
		FormatScores:       sp.FormatScores,
		MinFormatScore:     sp.MinFormatScore,
		Keywords:           sp.Keywords,
		Rejected:           sp.Rejected,
		MinSeeders:         sp.MinSeeders,
	}
}

// Engine builds an engine that knows both the built-in formats and this
// profile's custom formats.
func (sp StoredProfile) Engine() *Engine {
	return NewEngine(append(DefaultFormats(), sp.CustomFormats...))
}

// Summary renders a one-line, plain-language description of the profile.
func (sp StoredProfile) Summary() string {
	var parts []string
	switch {
	case len(sp.AllowedResolutions) == 0:
		parts = append(parts, "Any resolution")
	default:
		labels := make([]string, 0, len(sp.AllowedResolutions))
		for _, r := range sp.AllowedResolutions {
			labels = append(labels, resLabel(parser.Resolution(r)))
		}
		parts = append(parts, strings.Join(labels, " / "))
	}
	switch {
	case sp.MinSource != "" && sp.MaxSource != "":
		parts = append(parts, string(sp.MinSource)+"–"+string(sp.MaxSource))
	case sp.MinSource != "":
		parts = append(parts, string(sp.MinSource)+"+")
	case sp.MaxSource != "":
		parts = append(parts, "up to "+string(sp.MaxSource))
	}
	if sp.BitrateCapMbps > 0 {
		parts = append(parts, fmt.Sprintf("≤%.0f Mbps", sp.BitrateCapMbps))
	}
	var prefs []string
	for name, score := range sp.FormatScores {
		if score > 0 {
			prefs = append(prefs, name)
		}
	}
	if len(prefs) > 0 {
		parts = append(parts, "prefers "+strings.Join(prefs, ", "))
	}
	return strings.Join(parts, " · ")
}

// FormatInfo describes a built-in custom format for the builder's toggle list.
// Group buckets it (hdr | audio | codec) so the UI can split video vs audio and
// enforce "only one per group" — you can't prefer both Dolby Vision and HDR10,
// or both Atmos and DTS-HD.
type FormatInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Group       string `json:"group"` // hdr | audio | codec
}

// Catalog returns the built-in formats with friendly descriptions + groups.
func Catalog() []FormatInfo {
	meta := map[string]struct{ desc, group string }{
		"Dolby Vision": {"Dynamic HDR — the premium colour format", "hdr"},
		"HDR10":        {"Standard high dynamic range (also matches HDR10+)", "hdr"},
		"Atmos":        {"Object-based surround audio", "audio"},
		"TrueHD":       {"Lossless surround audio", "audio"},
		"DTS-HD":       {"Lossless DTS audio", "audio"},
		"HEVC":         {"x265 — smaller files, same quality", "codec"},
	}
	var out []FormatInfo
	for _, f := range DefaultFormats() {
		m := meta[f.Name]
		out = append(out, FormatInfo{Name: f.Name, Description: m.desc, Group: m.group})
	}
	return out
}
