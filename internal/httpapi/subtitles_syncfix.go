package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/subtitles"
)

// handleSubtitleSyncFix measures one language's sidecar against its video's speech and
// moves it into sync when the measurement is trusted (subtitles.FixSync). Named by catalog
// identity like the check; runs as a job because reading the soundtrack takes a while.
func (a *api) handleSubtitleSyncFix(w http.ResponseWriter, r *http.Request) {
	var req struct {
		subtitles.MediaRef
		Lang    string `json:"lang"`
		Variant string `json:"variant"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	t, err := a.deps.Subtitles.SyncTarget(r.Context(), req.MediaRef, req.Lang, req.Variant)
	switch {
	case errors.Is(err, subtitles.ErrNoVideo), errors.Is(err, subtitles.ErrNoSidecar):
		a.writeError(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, subtitles.ErrNotSRT), errors.Is(err, subtitles.ErrBadLanguage):
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		a.writeError(w, http.StatusInternalServerError, "could not find that subtitle")
		return
	}
	// The same target as a check: one measurement of a file at a time.
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "subtitles.sync-fix", Target: t.Key, Timeout: 30 * time.Minute,
		Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
			p.SetMessage("Reading the audio of " + t.Title + "…")
			fix, err := a.deps.Subtitles.FixSync(ctx, req.MediaRef, req.Lang, req.Variant)
			if err != nil {
				return nil, err
			}
			p.SetMessage(t.Name + ": " + fix.Words)
			return fix, nil
		}})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "fixing", "file": t.Name})
}

// handleSubtitleSyncFixSeries fixes the sync of every episode's subtitles in a show, or in
// one season (?season=N), one episode after another (subtitles.FixSyncSeries). One job per
// show at a time.
func (a *api) handleSubtitleSyncFixSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	season := -1
	if v := r.URL.Query().Get("season"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			a.writeError(w, http.StatusBadRequest, "invalid season")
			return
		}
		season = n
	}
	sr, err := a.deps.Series.Get(r.Context(), id)
	if errors.Is(err, series.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "series not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load series")
		return
	}
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "subtitles.sync-fix-series", Target: fmt.Sprintf("series:%d", id), Timeout: 24 * time.Hour,
		Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
			p.SetMessage("Checking the subtitles of " + sr.Title + "…")
			res, err := a.deps.Subtitles.FixSyncSeries(ctx, id, season, func(done, total int, last string) {
				msg := fmt.Sprintf("%d of %d episodes", done, total)
				if last != "" {
					msg += " · " + last
				}
				pct := 0.0
				if total > 0 {
					pct = float64(done) / float64(total)
				}
				p.Set(pct, msg)
			})
			if err != nil {
				return res, err
			}
			p.SetMessage(sr.Title + ": " + res.Words())
			return res, nil
		}})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "fixing"})
}
