package automation

import (
	"context"
	"strings"

	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// MovieCutoff is one movie track whose file doesn't meet its profile's target yet: a row
// of Movies → Wanted → Cutoff unmet. The file is judged the way the upgrade sweep judges
// it (quality.JudgeFile on currentMovieFile: Convert's probed facts when they still
// describe the file, its release name otherwise).
type MovieCutoff struct {
	Movie   movies.Movie
	Track   movies.Version // ID 0 = the default track
	Profile string         // the profile ref the file was judged under (the title's, or the default)
	// ProfileName is that profile's name, for the row.
	ProfileName string
	// Fit is what about the file misses the target. It can have no issues: a file that
	// fits the target but isn't at the best resolution the profile allows, or lacks
	// something the target prefers, still doesn't meet it.
	Fit quality.Fit
	// WillUpgrade: the upgrade sweep will look for a better release for this track. When
	// it won't, WhyNot says why in plain words.
	WillUpgrade bool
	WhyNot      string
}

// MoviesCutoffUnmet lists every track with a file on disk that a profile with a target
// judges as not there yet, in library order. A profile with no target set up has no
// cutoff to miss, so its files aren't listed. Database reads only (and Convert's index).
func (c *Coordinator) MoviesCutoffUnmet(ctx context.Context) ([]MovieCutoff, error) {
	byMovie, all, err := c.movies.LibraryVersionRows(ctx)
	if err != nil {
		return nil, err
	}
	stored := map[string]*quality.StoredProfile{} // effective ref → profile with a target (nil = none)
	targetOf := func(ref string) *quality.StoredProfile {
		if sp, ok := stored[ref]; ok {
			return sp
		}
		var out *quality.StoredProfile
		if sp, err := c.quality.GetStored(ctx, ref); err == nil && sp.Ideal != nil && !sp.Ideal.Empty() {
			out = &sp
		}
		stored[ref] = out
		return out
	}
	var out []MovieCutoff
	for _, m := range all {
		for _, v := range byMovie[m.ID] {
			if !v.HasFile || (v.File != nil && v.File.Missing) {
				continue
			}
			ref := c.effectiveProfile(ctx, v.QualityProfile, quality.MediaMovie)
			sp := targetOf(ref)
			if sp == nil {
				continue
			}
			cur := c.currentMovieFile(ctx, m, v)
			if strings.TrimSpace(cur.Release) == "" && cur.Facts == nil {
				continue // nothing recorded to judge the file by
			}
			verdict := sp.JudgeFile(cur)
			if verdict.TargetMet {
				continue
			}
			row := MovieCutoff{
				Movie: m, Track: v, Profile: ref, ProfileName: sp.Name,
				Fit: quality.CheckFit(*sp.Ideal, sp.AllowedResolutions, verdict.Facts),
			}
			row.WillUpgrade, row.WhyNot = upgradeOutlook(m, v, sp, verdict)
			out = append(out, row)
		}
	}
	return out, nil
}

// upgradeOutlook says whether the upgrade sweep will look for a better release for a
// track, by the sweep's own tests (UpgradeTargets, upgradeMovie, UpgradeCandidate): a
// monitored movie and track, a profile with upgrades on, no hold, and room under the
// ceiling. When it won't, the reason is the first test that fails.
func upgradeOutlook(m movies.Movie, v movies.Version, sp *quality.StoredProfile, verdict quality.FileVerdict) (bool, string) {
	switch {
	case !m.Monitored || !v.Monitored:
		if v.QualityProfile == "n/a" || (v.IsDefault && m.QualityProfile == "n/a") {
			return false, "Scanned in — not monitored"
		}
		return false, "Not monitored"
	case !sp.UpgradesEnabled:
		return false, "Its profile doesn't allow upgrades"
	case v.UpgradeHold:
		return false, "Upgrades paused — the file was kept when its profile changed"
	case verdict.AtCeiling:
		return false, "At the profile's bitrate ceiling — no release it accepts would be better"
	}
	return true, ""
}
