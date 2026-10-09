package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/series"
)

// A numbering change (anime moving onto TVDB's per-season listing) is never applied by a
// refresh. It's stored as a proposal: the series page shows every file that would move,
// and the owner applies or dismisses it here.

// numberingRemapView is one row of the review table.
type numberingRemapView struct {
	Absolute int    `json:"absolute"`
	Old      string `json:"old"`
	New      string `json:"new"` // "" when the new numbering has no place for the file
	File     string `json:"file"`
}

type numberingPendingView struct {
	From      string               `json:"from"`
	To        string               `json:"to"`
	CreatedAt string               `json:"created_at"`
	PlanHash  string               `json:"plan_hash"`
	Files     int                  `json:"files"` // distinct files that would move
	Remaps    []numberingRemapView `json:"remaps"`
}

func numberingView(p *series.NumberingPending) *numberingPendingView {
	if p == nil {
		return nil
	}
	v := &numberingPendingView{From: p.From, To: p.To, CreatedAt: p.CreatedAt, PlanHash: p.PlanHash,
		Remaps: make([]numberingRemapView, 0, len(p.Remaps))}
	files := map[string]bool{}
	for _, r := range p.Remaps {
		row := numberingRemapView{Absolute: r.Absolute, Old: fmt.Sprintf("S%02dE%02d", r.OldSeason, r.OldEpisode),
			File: filepath.Base(r.FilePath)}
		if !r.Unplaced {
			row.New = fmt.Sprintf("S%02dE%02d", r.NewSeason, r.NewEpisode)
			files[r.FilePath] = true
		}
		v.Remaps = append(v.Remaps, row)
	}
	v.Files = len(files)
	return v
}

// handleSeriesNumbering returns the show's numbering source and the proposal waiting for
// review, or pending: null.
func (a *api) handleSeriesNumbering(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	n, err := a.deps.Series.NumberingFor(r.Context(), id)
	if errors.Is(err, series.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "series not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the numbering")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"source": n.Source, "pending": numberingView(n.Pending)})
}

// numberingApplied is what an Apply did on disk.
type numberingApplied struct {
	Moved    int                     `json:"moved"`
	Skipped  []automation.RenameSkip `json:"skipped"`
	Unplaced int                     `json:"unplaced"` // files with no episode in the new numbering, left as they are
}

// handleApplySeriesNumbering applies the proposal the owner reviewed. A plan that isn't the
// one pending is refused straight away; the job then re-fetches the metadata and re-plans
// under the show's refresh lock, and moves nothing unless the plan is still the same.
func (a *api) handleApplySeriesNumbering(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		PlanHash string `json:"plan_hash"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	n, err := a.deps.Series.NumberingFor(r.Context(), id)
	if errors.Is(err, series.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "series not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the numbering")
		return
	}
	if n.Pending == nil || req.PlanHash == "" || n.Pending.PlanHash != req.PlanHash {
		a.writeError(w, http.StatusConflict, "the numbering changed since this was shown — nothing was moved; review it again")
		return
	}
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "series.numbering-apply", Target: jobTarget("series", id),
		Timeout: 30 * time.Minute,
		Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
			return a.applySeriesNumbering(ctx, id, req.PlanHash, p)
		}})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "applying"})
}

// applySeriesNumbering is the Apply job: rebuild the listing, rename exactly the files it
// carried, then rescan so the rows match the disk.
func (a *api) applySeriesNumbering(ctx context.Context, id int64, planHash string, p *jobs.Progress) (any, error) {
	p.SetMessage("Checking the numbering again")
	res, err := a.deps.Series.ApplyNumbering(ctx, id, planHash)
	if err != nil {
		return nil, err
	}
	out := numberingApplied{Skipped: []automation.RenameSkip{}, Unplaced: res.Unplaced}
	clean := res.Unplaced == 0
	if res.Renumbered {
		p.SetMessage("Renaming files")
		rr, rerr := a.deps.Automation.RenameRemapped(ctx, id, res.Remaps)
		if rerr != nil {
			// The rows already follow the new numbering and point at the files where they
			// are, so nothing is lost; the Rename button can finish the job.
			a.deps.Log.Warn("series: rename after renumber failed", "series_id", id, "err", rerr)
			clean = false
		} else {
			out.Moved, out.Skipped = rr.Moved, rr.Skipped
			a.deps.Automation.LogRenameSkips(id, rr)
			clean = len(rr.Skipped) == 0
		}
	}
	// The rebuild leaves every row pointing at its file, so the rescan only confirms it.
	// It's skipped while a file still has an old name — one a rename skipped, or one the
	// new numbering has no episode for: the rescan reads episodes from file names, and an
	// old "S01E04" can mean a different episode in the new numbering.
	if clean {
		a.deps.Automation.RescanSeries(ctx, id)
	}
	msg := fmt.Sprintf("Numbering applied — %d renamed", out.Moved)
	if len(out.Skipped) > 0 {
		msg += fmt.Sprintf(", %d not renamed (see History)", len(out.Skipped))
	}
	if res.Unplaced > 0 {
		msg += fmt.Sprintf(", %d with no episode left where they are", res.Unplaced)
	}
	if !clean {
		msg += " — sort those out before a rescan"
	}
	p.SetMessage(msg)
	return out, nil
}

// handleDismissSeriesNumbering turns the proposal down: nothing changes, and the same plan
// isn't proposed again (a different one is).
func (a *api) handleDismissSeriesNumbering(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	err := a.deps.Series.DismissNumbering(r.Context(), id)
	if errors.Is(err, series.ErrNoPendingNumbering) {
		a.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not dismiss the numbering change")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
