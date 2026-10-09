package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/convert"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// profileFile is one library file whose title runs under a profile, as the upgrade
// sweeps see it, with the row that records it.
type profileFile struct {
	table string // "movies" (a movie's default file), "movie_versions" or "episodes"
	id    int64  // that row's id
	file  quality.ImpactFile
	// swept: the upgrade sweep looks at this file at all — the title and the file are
	// monitored (and an episode isn't a special). Files it never visits can't be replaced,
	// whatever the profile says.
	swept bool
}

// filesOnProfile lists every file whose effective profile is ref, from the database alone:
// one movies query plus one versions query, or one episodes query, however big the library,
// and one read of Convert's analysis for the media type so each file is judged on its
// probed facts the way the sweeps judge it.
func (a *api) filesOnProfile(ctx context.Context, ref, media string) ([]profileFile, error) {
	ideals := newProfileIdeals(ctx, a.deps.Quality, media)
	facts := a.factsIndex(ctx, media)
	var out []profileFile
	switch media {
	case quality.MediaMovie:
		if a.deps.Movies == nil {
			return nil, nil
		}
		versions, all, err := a.deps.Movies.LibraryVersionRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, m := range all {
			title := m.Title
			if m.Year > 0 {
				title = fmt.Sprintf("%s (%d)", m.Title, m.Year)
			}
			for _, v := range versions[m.ID] {
				if !v.HasFile || ideals.resolve(ctx, v.QualityProfile) != ref {
					continue
				}
				pf := profileFile{table: "movie_versions", id: v.ID, file: quality.ImpactFile{
					Title: title, Release: automation.UpgradeBaseline(m, v), Bytes: v.SizeBytes, RuntimeMin: m.Runtime, Held: v.UpgradeHold,
					Facts: facts.lookup(v.FilePath, v.SizeBytes), OrigRelease: v.ConvertedFromRelease, OrigBytes: v.ConvertedFromSize,
				}}
				if v.IsDefault {
					pf.table, pf.id = "movies", m.ID
				}
				// UpgradeMovies visits monitored movies with a file, then each monitored track.
				pf.swept = m.Monitored && m.HasFile && v.Monitored
				out = append(out, pf)
			}
		}
	case quality.MediaSeries:
		if a.deps.Series == nil {
			return nil, nil
		}
		eps, err := a.deps.Series.LibraryEpisodeFiles(ctx)
		if err != nil {
			return nil, err
		}
		for _, e := range eps {
			if ideals.resolve(ctx, e.SeriesProfile) != ref {
				continue
			}
			out = append(out, profileFile{table: "episodes", id: e.EpisodeID, file: quality.ImpactFile{
				Title: e.SeriesTitle, Release: e.SourceRelease, Bytes: e.SizeBytes, RuntimeMin: e.RuntimeMin, Held: e.Held,
				Facts: facts.lookup(e.Path, e.SizeBytes), OrigRelease: e.ConvertedFromRelease, OrigBytes: e.ConvertedFromSize,
			},
				// UpgradeSeries visits monitored shows, and in them monitored episodes with a
				// file, never specials.
				swept: e.SeriesMonitored && e.Monitored && e.Season != 0 && e.Path != ""})
		}
	}
	return out, nil
}

// impactFacts is Convert's analysis for one media type, keyed by path; nil (no Convert, or
// it couldn't be read) answers every lookup with no facts, so files are judged by their
// release names as the sweeps judge them without a fact source.
type impactFacts convert.FactsIndex

func (a *api) factsIndex(ctx context.Context, media string) impactFacts {
	if a.deps.Convert == nil {
		return nil
	}
	kind := "movie"
	if media == quality.MediaSeries {
		kind = "episode"
	}
	ix, err := a.deps.Convert.FactsByPath(ctx, kind)
	if err != nil {
		a.deps.Log.Warn("quality impact: could not read Convert's analysis — judging by release names", "err", err)
		return nil
	}
	return impactFacts(ix)
}

// lookup is the file's probed facts when the analysis still describes it (same size,
// current probe), else nil.
func (f impactFacts) lookup(path string, sizeBytes int64) *quality.FileFacts {
	if f == nil || path == "" {
		return nil
	}
	ff, ok := convert.FactsIndex(f).Lookup(path, sizeBytes)
	if !ok {
		return nil
	}
	return &ff
}

// handleQualityImpact is the dry run behind the builder's Save: what saving this edit to a
// profile would make eligible for replacement. Saved profiles only — a new one has no files.
//
//	POST /api/v1/quality/impact {"profile": {...the edited profile, id set...}}
func (a *api) handleQualityImpact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile quality.StoredProfile `json:"profile"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Profile.ID <= 0 {
		a.writeError(w, http.StatusBadRequest, "only a saved profile has files to judge")
		return
	}
	ctx := r.Context()
	ref := "custom:" + strconv.FormatInt(req.Profile.ID, 10)
	old, err := a.deps.Quality.GetStored(ctx, ref)
	if err != nil {
		a.writeError(w, http.StatusNotFound, "profile not found")
		return
	}
	files, err := a.filesOnProfile(ctx, ref, old.MediaType)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the library")
		return
	}
	judged := make([]quality.ImpactFile, 0, len(files))
	for _, f := range files {
		if f.swept {
			judged = append(judged, f.file)
		}
	}
	a.writeJSON(w, http.StatusOK, a.deps.Quality.Impact(ctx, old, req.Profile, judged))
}

// handleHoldExisting is "Save — keep existing files": it holds the files on a profile out of
// profile-driven upgrades, so an edit applies to what's grabbed from now on while today's
// files stay as they are. Missing files are still searched for.
//
//	POST /api/v1/quality/profiles/{id}/hold-existing {}
//	POST /api/v1/quality/profiles/{id}/hold-existing {"only_affected": true, "profile": {...edited...}}
//
// With only_affected it holds just the files the edit would make eligible for replacement —
// the ones the Save dialog counted — judged against the profile as saved, so the builder
// calls it before saving the edit: that way no upgrade sweep can start on them in between.
func (a *api) handleHoldExisting(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		OnlyAffected bool                   `json:"only_affected"`
		Profile      *quality.StoredProfile `json:"profile"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.OnlyAffected && req.Profile == nil {
		a.writeError(w, http.StatusBadRequest, "only_affected needs the edited profile")
		return
	}
	ctx := r.Context()
	ref := "custom:" + strconv.FormatInt(id, 10)
	old, err := a.deps.Quality.GetStored(ctx, ref)
	if err != nil {
		a.writeError(w, http.StatusNotFound, "profile not found")
		return
	}
	files, err := a.filesOnProfile(ctx, ref, old.MediaType)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the library")
		return
	}
	if req.OnlyAffected {
		judged := make([]quality.ImpactFile, len(files))
		for i, f := range files {
			judged[i] = f.file
			if !f.swept {
				judged[i].Held = true // never visited by a sweep: no edit can make it worse
			}
		}
		worse := a.deps.Quality.Worsened(ctx, old, *req.Profile, judged)
		kept := files[:0:0]
		for i, f := range files {
			if worse[i] {
				kept = append(kept, f)
			}
		}
		files = kept
	}
	var movieIDs, versionIDs, episodeIDs []int64
	for _, f := range files {
		if f.file.Held {
			continue
		}
		switch f.table {
		case "movies":
			movieIDs = append(movieIDs, f.id)
		case "movie_versions":
			versionIDs = append(versionIDs, f.id)
		case "episodes":
			episodeIDs = append(episodeIDs, f.id)
		}
	}
	out := map[string]int{"movies": 0, "versions": 0, "episodes": 0}
	if len(movieIDs)+len(versionIDs) > 0 && a.deps.Movies != nil {
		m, v, err := a.deps.Movies.HoldUpgrades(ctx, movieIDs, versionIDs)
		if err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not keep the files")
			return
		}
		out["movies"], out["versions"] = m, v
	}
	if len(episodeIDs) > 0 && a.deps.Series != nil {
		n, err := a.deps.Series.HoldUpgrades(ctx, episodeIDs)
		if err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not keep the files")
			return
		}
		out["episodes"] = n
	}
	out["held"] = out["movies"] + out["versions"] + out["episodes"]
	a.writeJSON(w, http.StatusOK, out)
}

// handleResumeMovieUpgrades ends a movie's upgrade hold, on every track.
//
//	POST /api/v1/movies/{id}/resume-upgrades
func (a *api) handleResumeMovieUpgrades(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	n, err := a.deps.Movies.ResumeUpgrades(r.Context(), id)
	if errors.Is(err, movies.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "movie not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not resume upgrades")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"resumed": n})
}

// handleResumeSeriesUpgrades ends a show's upgrade hold: every episode's, or one season's
// with ?season=N.
//
//	POST /api/v1/series/{id}/resume-upgrades[?season=N]
func (a *api) handleResumeSeriesUpgrades(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	season := -1
	if s := r.URL.Query().Get("season"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			a.writeError(w, http.StatusBadRequest, "season must be a season number")
			return
		}
		season = n
	}
	n, err := a.deps.Series.ResumeUpgrades(r.Context(), id, season)
	if errors.Is(err, series.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "series not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not resume upgrades")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"resumed": n})
}
