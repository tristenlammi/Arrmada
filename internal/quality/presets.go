package quality

// DefaultFormats is the built-in custom-format catalog. These are the "Prefer/
// Avoid" attributes the Simple UI toggles and the Advanced UI scores.
func DefaultFormats() []CustomFormat {
	return []CustomFormat{
		{Name: "Dolby Vision", Conditions: []Condition{{Type: CondDynamicRange, Value: "DV"}}},
		{Name: "HDR10+", Conditions: []Condition{{Type: CondDynamicRange, Value: "HDR10+"}}},
		{Name: "HDR10", Conditions: []Condition{{Type: CondDynamicRange, Value: "HDR10"}}},
		{Name: "HLG", Conditions: []Condition{{Type: CondDynamicRange, Value: "HLG"}}},
		{Name: "SDR", Conditions: []Condition{{Type: CondDynamicRange, Value: "SDR"}}},
		{Name: "Atmos", Conditions: []Condition{{Type: CondAudio, Value: "Atmos"}}},
		{Name: "Lossless", Conditions: []Condition{{Type: CondLossless}}},
		{Name: "TrueHD", Conditions: []Condition{{Type: CondAudio, Value: "TrueHD"}}},
		{Name: "DTS-HD", Conditions: []Condition{{Type: CondAudio, Value: "DTS-HD"}}},
		{Name: "HEVC", Conditions: []Condition{{Type: CondCodec, Value: "x265"}}},
		{Name: "AV1", Conditions: []Condition{{Type: CondCodec, Value: "AV1"}}},
		{Name: "H.264", Conditions: []Condition{{Type: CondCodec, Value: "x264"}}},
	}
}

// fallbackProfile is a permissive, unlisted profile used only when no profile of the
// title's media type exists at all (a deleted profile resolves to the default through
// Effective instead). It keeps acquisition from stalling — it accepts any resolution
// and mildly prefers the common premium formats — but it still refuses cams and other
// pre-release copies, which nobody wants in their library by accident. It is never
// shown in the UI.
func fallbackProfile() Profile {
	return Profile{
		Name:             "Any quality",
		SmallBias:        0.15,
		FormatScores:     map[string]int{"Dolby Vision": 30, "HDR10": 25, "Atmos": 20},
		RejectPreRelease: true,
	}
}
