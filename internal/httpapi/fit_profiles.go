package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// The quality builder's live panels: how a profile's titles fit its target (per profile
// for the list page, and for unsaved edits in the editor), and what a profile would pick
// from real indexer results.

// fitCounts tallies a set of files against their target.
type fitCounts struct {
	Titles   int `json:"titles"` // titles using the profile
	Files    int `json:"files"`  // their analysed files that were judged
	Fits     int `json:"fits"`
	Over     int `json:"over"`
	Under    int `json:"under"`
	Mismatch int `json:"mismatch"`
}

func (c *fitCounts) add(fit quality.Fit) {
	c.Files++
	switch fit.Status {
	case quality.FitOK:
		c.Fits++
	case quality.FitOver:
		c.Over++
	case quality.FitUnder:
		c.Under++
	default:
		c.Mismatch++
	}
}

// fitMedia normalises the media query ("movie"/"movies", "series"/"tv").
func fitMedia(m string) string {
	switch m {
	case "series", "tv":
		return quality.MediaSeries
	}
	return quality.MediaMovie
}

// titleRefs maps each title (movie id, or series id) of a media type to the profile it
// runs under.
func (a *api) titleRefs(ctx context.Context, media string, ideals *profileIdeals) map[int64]string {
	out := map[int64]string{}
	if media == quality.MediaSeries {
		if a.deps.Series != nil {
			if ss, err := a.deps.Series.List(ctx); err == nil {
				for _, s := range ss {
					out[s.ID] = ideals.resolve(ctx, s.QualityProfile)
				}
			}
		}
		return out
	}
	if a.deps.Movies != nil {
		if ms, err := a.deps.Movies.List(ctx); err == nil {
			for _, m := range ms {
				out[m.ID] = ideals.resolve(ctx, m.QualityProfile)
			}
		}
	}
	return out
}

// indexedForMedia lists the analysed files of a media type with the title each belongs to.
func (a *api) indexedForMedia(ctx context.Context, media string) (files []struct {
	title int64
	facts quality.FileFacts
}) {
	kind := "movie"
	if media == quality.MediaSeries {
		kind = "episode"
	}
	list, err := a.deps.Convert.IndexedFiles(ctx, kind, 0)
	if err != nil {
		return nil
	}
	for i := range list {
		f := &list[i]
		id := f.MovieID
		if kind == "episode" {
			id = f.SeriesID
		}
		files = append(files, struct {
			title int64
			facts quality.FileFacts
		}{id, convertFacts(&f.Info)})
	}
	return files
}

// handleLibraryFitProfiles reports, per profile, how many titles use it and how their files
// fit its target — the list page's per-profile bars. ?media=movie|series.
func (a *api) handleLibraryFitProfiles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := map[string]*fitCounts{}
	if a.deps.Quality == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
		return
	}
	media := fitMedia(r.URL.Query().Get("media"))
	ideals := newProfileIdeals(ctx, a.deps.Quality, media)
	refs := a.titleRefs(ctx, media, ideals)
	at := func(ref string) *fitCounts {
		if out[ref] == nil {
			out[ref] = &fitCounts{}
		}
		return out[ref]
	}
	for _, ref := range refs {
		at(ref).Titles++
	}
	if a.deps.Convert != nil {
		for _, f := range a.indexedForMedia(ctx, media) {
			ref, ok := refs[f.title]
			if !ok {
				continue
			}
			if sp := ideals.get(ctx, ref); sp != nil {
				at(ref).add(quality.CheckFit(*sp.Ideal, sp.AllowedResolutions, f.facts))
			}
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
}

// handleLibraryFitPreview judges library files against a profile's target as edited, before
// it's saved: the titles that use the profile, or — for a profile not saved yet — the
// whole library of its media type, as if everything used it.
func (a *api) handleLibraryFitPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile quality.StoredProfile `json:"profile"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	sp := req.Profile
	media := fitMedia(sp.MediaType)
	counts := fitCounts{}
	scope := "profile"
	if a.deps.Quality == nil || a.deps.Convert == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "counts": counts})
		return
	}
	ideals := newProfileIdeals(ctx, a.deps.Quality, media)
	refs := a.titleRefs(ctx, media, ideals)
	key := ""
	if sp.ID > 0 {
		key = "custom:" + strconv.FormatInt(sp.ID, 10)
	} else {
		scope = "library"
	}
	inScope := func(title int64) bool {
		ref, ok := refs[title]
		return ok && (key == "" || ref == key)
	}
	for id := range refs {
		if inScope(id) {
			counts.Titles++
		}
	}
	target := quality.IdealFile{}
	if sp.Ideal != nil {
		target = *sp.Ideal
	}
	if !target.Empty() || len(sp.AllowedResolutions) > 0 {
		for _, f := range a.indexedForMedia(ctx, media) {
			if inScope(f.title) {
				counts.add(quality.CheckFit(target, sp.AllowedResolutions, f.facts))
			}
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "counts": counts})
}

// handleQualityTest runs a profile — as edited, saved or not — against a real title's
// indexer results: what it would grab, and why the rest were passed over.
//
//	{"profile": {...}, "movie_id": 12}
//	{"profile": {...}, "series_id": 4, "season": 2}
func (a *api) handleQualityTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile  quality.StoredProfile `json:"profile"`
		MovieID  int64                 `json:"movie_id"`
		SeriesID int64                 `json:"series_id"`
		Season   int                   `json:"season"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if a.deps.Automation == nil {
		a.writeError(w, http.StatusServiceUnavailable, "searching isn't available")
		return
	}
	ctx, cancel := context.WithTimeout(indexer.WithInteractive(r.Context()), 90*time.Second)
	defer cancel()
	var (
		list automation.ReleaseList
		err  error
	)
	switch {
	case req.MovieID > 0:
		list, err = a.deps.Automation.RankReleasesWith(ctx, req.MovieID, &req.Profile)
	case req.SeriesID > 0:
		season := req.Season
		if season <= 0 {
			season = 1
		}
		list, err = a.deps.Automation.RankSeriesReleasesWith(ctx, req.SeriesID, season, 0, &req.Profile)
	default:
		a.writeError(w, http.StatusBadRequest, "pick a movie or a show to test with")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, list)
}
