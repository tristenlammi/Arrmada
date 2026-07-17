package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/subtitles"
)

// handleSubtitleJobs returns recent + active subtitle-ensure jobs (polled for the Queue tab).
func (a *api) handleSubtitleJobs(w http.ResponseWriter, r *http.Request) {
	jobs := a.deps.Subtitles.Jobs()
	if jobs == nil {
		jobs = []subtitles.Job{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// handleSubtitleLogs returns the recent Subtitles activity console lines.
func (a *api) handleSubtitleLogs(w http.ResponseWriter, r *http.Request) {
	logs := a.deps.Subtitles.Logs()
	if logs == nil {
		logs = []subtitles.LogLine{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"lines": logs})
}

// handleSubtitleQueueMovie queues a subtitle-ensure job for one movie.
func (a *api) handleSubtitleQueueMovie(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	job, err := a.deps.Subtitles.QueueMovie(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusAccepted, job)
}

// handleSubtitleQueueEpisode queues a subtitle-ensure job for one TV episode.
func (a *api) handleSubtitleQueueEpisode(w http.ResponseWriter, r *http.Request) {
	seriesID, ok := a.pathValueID(w, r, "series")
	if !ok {
		return
	}
	season, err := strconv.Atoi(r.PathValue("season"))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid season")
		return
	}
	episode, err := strconv.Atoi(r.PathValue("episode"))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid episode")
		return
	}
	job, err := a.deps.Subtitles.QueueEpisode(r.Context(), seriesID, season, episode)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusAccepted, job)
}

// handleSubtitleSweep queues an ensure job for every file missing a kept-language subtitle.
func (a *api) handleSubtitleSweep(w http.ResponseWriter, r *http.Request) {
	media := "movies"
	if r.URL.Query().Get("media") == "tv" {
		media = "tv"
	}
	n, err := a.deps.Subtitles.SweepMissing(r.Context(), media)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not start the sweep")
		return
	}
	a.writeJSON(w, http.StatusAccepted, map[string]any{"queued": n})
}

func (a *api) handleGetSubtitleSettings(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Subtitles.GetSettings(r.Context()))
}

func (a *api) handleUpdateSubtitleSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MoviesAuto *bool     `json:"movies_auto"`
		SeriesAuto *bool     `json:"series_auto"`
		Languages  *[]string `json:"languages"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	var langs []string
	if req.Languages != nil {
		langs = *req.Languages
	}
	if err := a.deps.Subtitles.SetSettings(r.Context(), req.MoviesAuto, req.SeriesAuto, langs); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save subtitle settings")
		return
	}
	a.writeJSON(w, http.StatusOK, a.deps.Subtitles.GetSettings(r.Context()))
}

// handleSubtitleLibrary returns per-file subtitle coverage for the Subtitles Library tab
// (media=movies|tv), computed against the kept languages.
func (a *api) handleSubtitleLibrary(w http.ResponseWriter, r *http.Request) {
	media := "movies"
	if r.URL.Query().Get("media") == "tv" {
		media = "tv"
	}
	list, err := a.deps.Subtitles.Library(r.Context(), media)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load subtitle library")
		return
	}
	if list == nil {
		list = []subtitles.FileSubs{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (a *api) handleSubtitleMovies(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Subtitles.MovieStatuses(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load movie subtitles")
		return
	}
	if list == nil {
		list = []subtitles.MovieStatus{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"movies": list})
}

func (a *api) handleSubtitleSeries(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Subtitles.SeriesStatuses(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load series subtitles")
		return
	}
	if list == nil {
		list = []subtitles.SeriesStatus{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"series": list})
}

// handleSubtitleSearchMovie grabs missing subtitles for one movie (background).
func (a *api) handleSubtitleSearchMovie(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if !a.deps.Subtitles.GetSettings(r.Context()).CanDownload {
		a.writeError(w, http.StatusBadRequest, "OpenSubtitles isn't configured — add an API key, username and password to grab subtitles")
		return
	}
	go func(mid int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if n, err := a.deps.Subtitles.GrabMovie(ctx, mid); err != nil {
			a.deps.Log.Warn("subtitle movie grab failed", "movie_id", mid, "err", err)
		} else {
			a.deps.Log.Info("subtitle movie grab done", "movie_id", mid, "grabbed", n)
		}
	}(id)
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "searching"})
}

// handleSubtitleSearchSeries grabs missing subtitles for a whole series (background).
func (a *api) handleSubtitleSearchSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if !a.deps.Subtitles.GetSettings(r.Context()).CanDownload {
		a.writeError(w, http.StatusBadRequest, "OpenSubtitles isn't configured — add an API key, username and password to grab subtitles")
		return
	}
	go func(sid int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		if n, err := a.deps.Subtitles.GrabSeries(ctx, sid); err != nil {
			a.deps.Log.Warn("subtitle series grab failed", "series_id", sid, "err", err)
		} else {
			a.deps.Log.Info("subtitle series grab done", "series_id", sid, "grabbed", n)
		}
	}(id)
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "searching"})
}
