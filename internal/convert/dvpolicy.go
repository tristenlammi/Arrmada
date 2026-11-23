package convert

import (
	"context"
	"fmt"
)

// Dolby Vision as an explicit choice. Dropping the layer keeps a Dolby Vision file's HDR10
// (or HLG) base and loses the Dolby Vision layer — the one choice that removes something
// people pick their files for, so a NEW install leaves Dolby Vision files alone until its
// owner says otherwise (dvChoice); an install that was already converting keeps dropping, as
// it always has. Leaving them alone still tidies their tracks. The third choice, a preview,
// carries the layer through the conversion as profile 8.1 (dv.go) — not the default until it
// has been watched on a Dolby Vision TV.
const (
	DVDrop = "drop" // convert, dropping the Dolby Vision layer (keeps HDR10/HLG)
	DVSkip = "skip" // leave Dolby Vision files' pictures alone
	DVKeep = "keep" // convert and keep the Dolby Vision layer, as profile 8.1 (preview)
)

// dvLeftAloneWhy is what a Dolby Vision file's row says under DVSkip.
const dvLeftAloneWhy = "Dolby Vision — left alone (your choice in Convert's settings)"

// dvPolicy reads a saved choice; anything else (a value from a later version) is dropping,
// what every install did before the choice existed.
func dvPolicy(v string) string {
	switch v {
	case DVSkip, DVKeep:
		return v
	}
	return DVDrop
}

// dvChoice reads the Dolby Vision setting. The first read on an install without it decides
// the default and saves it, so it never changes under the owner afterwards: an install that
// has already used Convert (the same signs tenBitChoice reads) keeps dropping the layer, as
// it did before; a new one leaves Dolby Vision files alone.
func (s *Service) dvChoice(ctx context.Context) string {
	if v, saved := s.settings.Lookup(keyDolbyVision); saved {
		return dvPolicy(v)
	}
	choice := DVSkip
	for _, k := range convertUsedKeys {
		if _, used := s.settings.Lookup(k); used {
			choice = DVDrop
			break
		}
	}
	if err := s.settings.Set(ctx, keyDolbyVision, choice); err != nil && s.log != nil {
		s.log.Warn("convert: couldn't save the Dolby Vision default", "err", err)
	}
	return choice
}

// dvKeepBlock says why a Dolby Vision file's layer can't be kept through a conversion under
// DVKeep, or "" when it can. Such a file is left alone rather than converted without it:
// whoever chose "keep" chose not to lose the layer. Profile 5 never gets here (its picture
// isn't convertible at all, videoWorth).
func dvKeepBlock(mi *MediaInfo, hasTool bool) string {
	switch {
	case !hasTool:
		return "Dolby Vision — keeping it needs dovi_tool, which isn't installed, so it's left alone"
	case mi.EncodeHDR() == "HLG":
		// Profile 8.4. The RPU would carry over, but nothing here has been checked on one.
		return "Dolby Vision profile 8.4 (HLG) — keeping its Dolby Vision layer isn't supported, so it's left alone"
	case mi.DVProfile != 0 && mi.DVProfile != 7 && mi.DVProfile != 8:
		return fmt.Sprintf("Dolby Vision profile %d — only profiles 7 and 8 can keep their Dolby Vision layer, so it's left alone", mi.DVProfile)
	}
	return ""
}

// profilePrefersDV asks whether the quality profile a title runs under wants Dolby Vision
// (kind "movie" or "episode", id the movie or series). See SetProfilePrefersDV.
type profilePrefersDV func(ctx context.Context, kind string, id int64) bool

// SetProfilePrefersDV tells Convert how to ask whether a title's quality profile prefers
// Dolby Vision, so the plan can warn that dropping the layer leaves the file outside it.
func (s *Service) SetProfilePrefersDV(fn func(ctx context.Context, kind string, id int64) bool) {
	if fn == nil {
		s.prefersDV.Store(nil)
		return
	}
	f := profilePrefersDV(fn)
	s.prefersDV.Store(&f)
}

// dvProfileWarning is the plan's warning when a conversion would drop the Dolby Vision
// layer of a title whose profile prefers it, or "".
func (s *Service) dvProfileWarning(ctx context.Context, r indexRow, plan Plan) string {
	if plan.VideoCodec == "" || plan.KeepDV || r.Info == nil || r.Info.HDR != "Dolby Vision" {
		return ""
	}
	fn := s.prefersDV.Load()
	if fn == nil {
		return ""
	}
	kind, id := "movie", r.MovieID
	if r.MediaType == "episode" {
		kind, id = "episode", r.SeriesID
	}
	if id <= 0 || !(*fn)(ctx, kind, id) {
		return ""
	}
	return "your quality profile prefers Dolby Vision — after conversion this file won't fit it"
}
