package quality

// DefaultFormats is the built-in custom-format catalog. These are the "Prefer/
// Avoid" attributes the Simple UI toggles and the Advanced UI scores.
func DefaultFormats() []CustomFormat {
	return []CustomFormat{
		{Name: "Dolby Vision", Conditions: []Condition{{Type: CondDynamicRange, Value: "DV"}}},
		{Name: "HDR10", Conditions: []Condition{{Type: CondDynamicRange, Value: "HDR10"}}},
		{Name: "Atmos", Conditions: []Condition{{Type: CondAudio, Value: "Atmos"}}},
		{Name: "TrueHD", Conditions: []Condition{{Type: CondAudio, Value: "TrueHD"}}},
		{Name: "DTS-HD", Conditions: []Condition{{Type: CondAudio, Value: "DTS-HD"}}},
		{Name: "HEVC", Conditions: []Condition{{Type: CondCodec, Value: "x265"}}},
	}
}

// fallbackProfile is a permissive, unlisted profile used only when a movie
// references a quality profile that no longer exists (e.g. it was deleted). It
// keeps acquisition from stalling — it accepts any resolution and mildly
// prefers the common premium formats. It is never shown in the UI.
func fallbackProfile() Profile {
	return Profile{
		Name:         "Any quality",
		SmallBias:    0.15,
		FormatScores: map[string]int{"Dolby Vision": 30, "HDR10": 25, "Atmos": 20},
	}
}
