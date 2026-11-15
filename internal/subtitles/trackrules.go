package subtitles

import "context"

// TrackRule is one language of a title's profile, as Convert's "Same as Subtitles" reads it
// to decide which of a file's own subtitle tracks stay (#100).
type TrackRule struct {
	Lang string
	// Full: the full subtitle is wanted ("Full" or "Full + forced"), not the forced one alone.
	Full bool
	// OnlyIfAudioDiffers: the full subtitle isn't wanted when the main audio is in Lang.
	OnlyIfAudioDiffers bool
	// NoSDH: the profile says "Never SDH".
	NoSDH bool
}

// TrackRules is the profile a title uses (its pinned one, else the default), one rule per
// language. kind is "movie" or "episode"/"series".
func (s *Service) TrackRules(ctx context.Context, kind string, movieID, seriesID int64) []TrackRule {
	p, _ := s.profileCfg(ctx).forTitle(kind, movieID, seriesID)
	out := make([]TrackRule, 0, len(p.Items))
	for _, it := range p.Items {
		out = append(out, TrackRule{
			Lang:               it.Lang,
			Full:               it.Variant != WantForced,
			OnlyIfAudioDiffers: it.OnlyIfAudioDiffers,
			NoSDH:              it.SDH == SDHAvoid,
		})
	}
	return out
}

// ProfilesKey changes whenever a profile a title uses, or which title uses which, changes:
// Convert's cached plans depend on it.
func (s *Service) ProfilesKey(ctx context.Context) string { return s.wantsKey(ctx) }
