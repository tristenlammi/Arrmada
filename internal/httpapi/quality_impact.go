package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/quality"
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
// one movies query plus one versions query, or one episodes query, however big the library.
func (a *api) filesOnProfile(ctx context.Context, ref, media string) ([]profileFile, error) {
	ideals := newProfileIdeals(ctx, a.deps.Quality, media)
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
					Title: title, Release: automation.UpgradeBaseline(m, v), Bytes: v.SizeBytes, RuntimeMin: m.Runtime,
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
				Title: e.SeriesTitle, Release: e.SourceRelease, Bytes: e.SizeBytes, RuntimeMin: e.RuntimeMin,
			},
				// UpgradeSeries visits monitored shows, and in them monitored episodes with a
				// file, never specials.
				swept: e.SeriesMonitored && e.Monitored && e.Season != 0 && e.Path != ""})
		}
	}
	return out, nil
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
