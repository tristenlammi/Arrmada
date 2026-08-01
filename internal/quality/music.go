package quality

// Music quality is a LADDER, not a set of preferences.
//
// For books, EPUB vs MOBI is taste — neither is better, so a book profile is a flat
// "which of these do I want" map. Music is not like that: MP3-128 and MP3-320 are two and a
// half times apart, and FLAC is categorically above both. The tiers are ordered.
//
// They're still expressed as format_scores rather than a separate ladder field, and that
// turns out to be exactly right: score the tiers in rank order and the existing engine picks
// the best available, refuses anything scoring 0 or below, and — because nothing can score
// higher than the tier you already hold — stops upgrading on its own once you reach the top
// of your ladder. That is the "upgrade until FLAC, then stop" behaviour other music managers
// need a dedicated cutoff setting for.
//
// The names match what trackers actually print, so a profile reads like the release list.

// MusicQualityLadder is every score-able audio tier, best first. The UI renders the profile
// builder from this, so the ladder and the scoring can't drift apart.
func MusicQualityLadder() []string {
	return []string{
		"FLAC-24", "FLAC", "ALAC", "WAV",
		"MP3-320", "MP3-V0", "AAC-256", "MP3-256",
		"MP3-V2", "OPUS", "OGG", "MP3-192", "MP3-128",
	}
}

// MusicPreset is a starter profile offered when creating a music profile, so a user doesn't
// have to hand-build a thirteen-tier ladder to get something sensible.
type MusicPreset struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	FormatScores map[string]int `json:"format_scores"`
	// MinFormatScore is the floor: a release scoring below it is refused outright, which is
	// how "lossless only" becomes a hard rule rather than a preference.
	MinFormatScore  int  `json:"min_format_score"`
	UpgradesEnabled bool `json:"upgrades_enabled"`
}

// MusicPresets returns the starter profiles.
//
// Scores descend in ladder order so the engine's "highest total wins" picks the best tier
// available, and anything a preset omits scores zero — which the engine treats as unwanted.
// That is what makes "Lossless only" actually refuse an MP3 rather than quietly accept one.
func MusicPresets() []MusicPreset {
	return []MusicPreset{
		{
			Name:        "Lossless only",
			Description: "FLAC and friends, nothing lossy. Refuses MP3 even when it's the only release.",
			FormatScores: map[string]int{
				"FLAC-24": 130, "FLAC": 120, "ALAC": 110, "WAV": 100,
			},
			MinFormatScore: 1, UpgradesEnabled: true,
		},
		{
			Name:        "Lossless, or the best MP3",
			Description: "Prefers FLAC; takes MP3 320 or V0 when there's no lossless release, and upgrades later if one appears.",
			FormatScores: map[string]int{
				"FLAC-24": 130, "FLAC": 120, "ALAC": 110, "WAV": 100,
				"MP3-320": 60, "MP3-V0": 55, "AAC-256": 45, "MP3-256": 40, "MP3-V2": 30,
			},
			MinFormatScore: 1, UpgradesEnabled: true,
		},
		{
			Name:        "High-quality lossy",
			Description: "MP3 320 / V0 and equivalents. Skips lossless — smaller library, no audible loss on most gear.",
			FormatScores: map[string]int{
				"MP3-320": 60, "MP3-V0": 55, "AAC-256": 45, "MP3-256": 40, "MP3-V2": 30, "OPUS": 25,
			},
			MinFormatScore: 1, UpgradesEnabled: true,
		},
		{
			Name:        "Any quality",
			Description: "Takes whatever exists, best first. Use when availability matters more than fidelity.",
			FormatScores: map[string]int{
				"FLAC-24": 130, "FLAC": 120, "ALAC": 110, "WAV": 100,
				"MP3-320": 60, "MP3-V0": 55, "AAC-256": 45, "MP3-256": 40,
				"MP3-V2": 30, "OPUS": 25, "OGG": 20, "MP3-192": 15, "MP3-128": 5,
			},
			MinFormatScore: 1, UpgradesEnabled: true,
		},
	}
}

// FallbackMusicProfile scores the full ladder in rank order. Used when an artist references
// a profile that no longer exists, so acquisition degrades to "best available" instead of
// stalling — the same role fallbackProfile plays for video.
func FallbackMusicProfile() StoredProfile {
	for _, p := range MusicPresets() {
		if p.Name == "Any quality" {
			return StoredProfile{
				MediaType: MediaMusic, Name: p.Name, FormatScores: p.FormatScores,
				MinFormatScore: p.MinFormatScore, UpgradesEnabled: p.UpgradesEnabled,
			}
		}
	}
	return StoredProfile{MediaType: MediaMusic}
}
