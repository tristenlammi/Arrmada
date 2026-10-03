package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/convert"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// The ideal-file check over the library: every analysed file judged against its title's
// quality profile (quality.CheckFit). Read from the Convert library index — nothing is
// probed — and report-only.

type fitItem struct {
	MovieID  int64             `json:"movie_id,omitempty"`
	SeriesID int64             `json:"series_id,omitempty"`
	Season   int               `json:"season"`
	Episode  int               `json:"episode,omitempty"`
	Facts    quality.FileFacts `json:"facts"`
	Fit      *quality.Fit      `json:"fit,omitempty"` // nil when the title's profile has no ideal file set up
	Profile  string            `json:"profile,omitempty"`
}

type seriesFit struct {
	SeriesID int64 `json:"series_id"`
	Checked  int   `json:"checked"` // episodes judged (their profile has an ideal file)
	Fits     int   `json:"fits"`
	Over     int   `json:"over"`
	Under    int   `json:"under"`
	Mismatch int   `json:"mismatch"`
}

// profileIdeals resolves profile refs (a title's, or the default for "") to profiles
// with an ideal file set up, once per ref.
type profileIdeals struct {
	q     *quality.Service
	def   string
	cache map[string]*quality.StoredProfile
}

func newProfileIdeals(ctx context.Context, q *quality.Service, media string) *profileIdeals {
	return &profileIdeals{q: q, def: q.DefaultProfile(ctx, media), cache: map[string]*quality.StoredProfile{}}
}

func (p *profileIdeals) get(ctx context.Context, ref string) *quality.StoredProfile {
	if ref == "" {
		ref = p.def
	}
	if sp, ok := p.cache[ref]; ok {
		return sp
	}
	var out *quality.StoredProfile
	if sp, err := p.q.GetStored(ctx, ref); err == nil && sp.Ideal != nil {
		out = &sp
	}
	p.cache[ref] = out
	return out
}

func judge(ctx context.Context, ideals *profileIdeals, ref string, mi *convert.MediaInfo) fitItem {
	it := fitItem{Facts: convert.Facts(mi)}
	if sp := ideals.get(ctx, ref); sp != nil {
		fit := quality.CheckFit(*sp.Ideal, sp.AllowedResolutions, it.Facts)
		it.Fit, it.Profile = &fit, sp.Name
	}
	return it
}

// handleLibraryFit reports how library files fit their profiles' ideal file.
//
//	?media=movies            → every movie file
//	?media=series            → per-show counts
//	?media=series&series=<id> → that show's episode files
func (a *api) handleLibraryFit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if a.deps.Convert == nil || a.deps.Quality == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"items": []fitItem{}})
		return
	}
	q := r.URL.Query()
	switch q.Get("media") {
	case "series":
		a.libraryFitSeries(w, r, q.Get("series"))
	default:
		a.libraryFitMovies(w, ctx)
	}
}

func (a *api) libraryFitMovies(w http.ResponseWriter, ctx context.Context) {
	files, err := a.deps.Convert.IndexedFiles(ctx, "movie", 0)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the library index")
		return
	}
	refs := map[int64]string{}
	if a.deps.Movies != nil {
		if ms, err := a.deps.Movies.List(ctx); err == nil {
			for _, m := range ms {
				refs[m.ID] = m.QualityProfile
			}
		}
	}
	ideals := newProfileIdeals(ctx, a.deps.Quality, quality.MediaMovie)
	items := make([]fitItem, 0, len(files))
	for i := range files {
		f := &files[i]
		it := judge(ctx, ideals, refs[f.MovieID], &f.Info)
		it.MovieID = f.MovieID
		items = append(items, it)
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *api) libraryFitSeries(w http.ResponseWriter, r *http.Request, seriesParam string) {
	ctx := r.Context()
	var seriesID int64
	if seriesParam != "" {
		id, err := strconv.ParseInt(seriesParam, 10, 64)
		if err != nil || id <= 0 {
			a.writeError(w, http.StatusBadRequest, "invalid series id")
			return
		}
		seriesID = id
	}
	files, err := a.deps.Convert.IndexedFiles(ctx, "episode", seriesID)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the library index")
		return
	}
	refs := map[int64]string{}
	if a.deps.Series != nil {
		if ss, err := a.deps.Series.List(ctx); err == nil {
			for _, s := range ss {
				refs[s.ID] = s.QualityProfile
			}
		}
	}
	ideals := newProfileIdeals(ctx, a.deps.Quality, quality.MediaSeries)
	if seriesID > 0 {
		items := make([]fitItem, 0, len(files))
		for i := range files {
			f := &files[i]
			it := judge(ctx, ideals, refs[f.SeriesID], &f.Info)
			it.SeriesID, it.Season, it.Episode = f.SeriesID, f.Season, f.Episode
			items = append(items, it)
		}
		a.writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}
	agg := map[int64]*seriesFit{}
	order := []int64{}
	for i := range files {
		f := &files[i]
		sf := agg[f.SeriesID]
		if sf == nil {
			sf = &seriesFit{SeriesID: f.SeriesID}
			agg[f.SeriesID] = sf
			order = append(order, f.SeriesID)
		}
		it := judge(ctx, ideals, refs[f.SeriesID], &f.Info)
		if it.Fit == nil {
			continue
		}
		sf.Checked++
		switch it.Fit.Status {
		case quality.FitOK:
			sf.Fits++
		case quality.FitOver:
			sf.Over++
		case quality.FitUnder:
			sf.Under++
		default:
			sf.Mismatch++
		}
	}
	out := make([]seriesFit, 0, len(order))
	for _, id := range order {
		out = append(out, *agg[id])
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
