package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/store"
	"github.com/tristenlammi/arrmada/internal/tautulli"
)

// importTimeout bounds one Tautulli import. A run that hits it is recorded as timed out,
// and Retry carries on from where it got to (rows already in are skipped).
const importTimeout = 30 * time.Minute

// handleTautulliConfig is the saved Tautulli connection: the URL, and only whether a key
// is saved — the key itself never leaves the server.
func (a *api) handleTautulliConfig(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Insights.TautulliConfig(r.Context()))
}

// handleImportTautulli backfills Insights with a Tautulli watch history so its stats/graphs aren't
// empty on day one. Verifies the connection, saves it (for Retry), then imports in the background
// as a tracked run. Re-running is safe: plays already imported, and plays Arrmada recorded live,
// are skipped.
func (a *api) handleImportTautulli(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
		// Before (epoch seconds, optional) imports only plays that started earlier.
		Before int64 `json:"before"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	url, key := strings.TrimSpace(req.URL), strings.TrimSpace(req.APIKey)
	savedURL, savedKey := a.deps.Insights.TautulliCredentials(r.Context())
	if url == "" {
		url = savedURL
	}
	// The saved key is only ever sent to the URL it was saved with: a new address needs the
	// key typed again, so an edited URL can't carry the stored key somewhere else.
	if key == "" && url == savedURL {
		key = savedKey
	}
	if url == "" || key == "" {
		a.writeError(w, http.StatusBadRequest, "url and api_key are required")
		return
	}
	if !a.pingTautulli(w, r, url, key) {
		return
	}
	if err := a.deps.Insights.SaveTautulli(r.Context(), url, key); err != nil {
		a.writeError(w, http.StatusInternalServerError, "couldn't save the Tautulli connection")
		return
	}
	a.startTautulliImport(w, r, url, key, max(req.Before, 0))
}

// handleRetryImportRun runs an import again from the saved connection, with the same
// cutoff. Plays the earlier attempt brought in are skipped, so nothing doubles.
func (a *api) handleRetryImportRun(w http.ResponseWriter, r *http.Request) {
	run, ok := a.importRunFromPath(w, r)
	if !ok {
		return
	}
	url, key := a.deps.Insights.TautulliCredentials(r.Context())
	if url == "" || key == "" {
		a.writeError(w, http.StatusBadRequest, "there's no saved Tautulli connection — start the import from the form")
		return
	}
	if !a.pingTautulli(w, r, url, key) {
		return
	}
	a.startTautulliImport(w, r, url, key, run.CutoffAt)
}

func (a *api) pingTautulli(w http.ResponseWriter, r *http.Request, url, key string) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := tautulli.New(url, key).Ping(ctx); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return false
	}
	return true
}

func (a *api) startTautulliImport(w http.ResponseWriter, r *http.Request, url, key string, before int64) {
	// One import at a time (the job runner's single-flight), so a double-clicked import
	// doesn't interleave duplicate inserts.
	jobID, existing, ok := a.submitOr503(w, r, a.tautulliImportJob(url, key, before))
	if !ok {
		return
	}
	if existing {
		a.alreadyRunning(w, jobID, "an import is already running")
		return
	}
	a.accepted(w, jobID, false, map[string]any{"status": "started"})
}

// tautulliImportJob is one tracked import: a run record that follows each 500-row page
// (progress and counts), closed as done, failed, timeout or interrupted.
func (a *api) tautulliImportJob(url, key string, before int64) jobs.Spec {
	return jobs.Spec{Kind: "insights.import-tautulli", Target: "all", Class: jobs.ClassExternalImport, Timeout: importTimeout,
		Fn: func(bg context.Context, p *jobs.Progress) (any, error) {
			ins := a.deps.Insights
			runID, err := ins.StartImportRun(bg, p.JobID(), before)
			if err != nil {
				return nil, fmt.Errorf("couldn't start the import's record: %w", err)
			}
			opts := insights.ImportOptions{Before: before, RunID: runID}
			var counts insights.ImportCounts
			total := 0
			err = tautulli.New(url, key).History(bg, func(pg tautulli.Page) error {
				total = max(total, pg.Total)
				c := ins.ImportHistory(bg, importedSessions(pg.Rows), opts)
				c.Invalid += pg.Skipped
				counts.Add(c)
				if uerr := ins.UpdateImportRun(bg, runID, total, counts); uerr != nil && bg.Err() == nil {
					a.deps.Log.Warn("tautulli import: couldn't record progress", "run", runID, "err", uerr)
				}
				pct := 0.0
				if total > 0 {
					pct = float64(counts.Processed()) / float64(total)
				}
				p.Set(pct, fmt.Sprintf("Read %d of %d plays", counts.Processed(), total))
				return bg.Err() // a page cut short by a timeout or shutdown stops here
			})
			status, errText := insights.RunDone, ""
			switch {
			case err == nil:
			case errors.Is(err, context.DeadlineExceeded):
				status, errText = insights.RunTimeout, "the import took longer than 30 minutes and was stopped — Retry carries on from where it got to"
			case errors.Is(err, context.Canceled):
				status, errText = insights.RunInterrupted, "the import was stopped before it finished"
			default:
				status, errText = insights.RunFailed, err.Error()
			}
			// The run's ending is written even when the job's own context is already over.
			wctx, cancel := context.WithTimeout(context.WithoutCancel(bg), 5*time.Second)
			if ferr := ins.FinishImportRun(wctx, runID, status, total, counts, errText); ferr != nil {
				a.deps.Log.Warn("tautulli import: couldn't record how it ended", "run", runID, "err", ferr)
			}
			cancel()
			a.deps.Log.Info("tautulli import "+status, "run", runID, "imported", counts.Imported, "duplicates", counts.Duplicate,
				"recorded_live", counts.Overlap, "invalid", counts.Invalid, "after_cutoff", counts.AfterCutoff,
				"failed", counts.Failed, "first_error", counts.FirstError, "err", errText)
			result := map[string]any{"run_id": runID, "total": total, "counts": counts}
			if err != nil {
				return result, errors.New(errText)
			}
			p.SetMessage(fmt.Sprintf("Imported %d plays (%d already here, %d recorded live)", counts.Imported, counts.Duplicate, counts.Overlap))
			return result, nil
		}}
}

// importedSessions maps a page of Tautulli rows to the importer's neutral shape.
func importedSessions(rows []tautulli.Row) []insights.ImportedSession {
	sessions := make([]insights.ImportedSession, 0, len(rows))
	for _, r := range rows {
		sessions = append(sessions, insights.ImportedSession{
			UserID: r.UserID, UserName: r.User, UserThumb: r.UserThumb, RatingKey: r.RatingKey, MediaType: r.MediaType,
			Title: r.Title, GrandparentTitle: r.GrandparentTitle, ParentTitle: r.ParentTitle,
			MediaIndex: r.MediaIndex, ParentIndex: r.ParentIndex, Year: r.Year, Thumb: r.Thumb,
			Player: r.Player, Platform: r.Platform, Product: r.Product, IPAddress: r.IPAddress, Decision: r.Decision,
			StartedAt: r.Started, StoppedAt: r.Stopped, PausedMS: r.PausedSec * 1000,
			// Tautulli's `duration` is how long they WATCHED, not how long the
			// media runs — it belongs in WatchedMS. Putting it in DurationMS made
			// History divide the view offset by watch time for its progress bar.
			WatchedMS: r.DurationSec * 1000,
		})
	}
	return sessions
}

// handleImportRuns lists the latest import runs with their progress and counts.
func (a *api) handleImportRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := a.deps.Insights.ImportRuns(r.Context(), 5)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "couldn't read the import history")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (a *api) importRunFromPath(w http.ResponseWriter, r *http.Request) (insights.ImportRun, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		a.writeError(w, http.StatusBadRequest, "invalid run id")
		return insights.ImportRun{}, false
	}
	run, err := a.deps.Insights.ImportRun(r.Context(), id)
	if errors.Is(err, insights.ErrRunNotFound) {
		a.writeError(w, http.StatusNotFound, "import run not found")
		return insights.ImportRun{}, false
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "couldn't read that import")
		return insights.ImportRun{}, false
	}
	return run, true
}

// handleRemoveImportRun undoes one import: a job that backs the database up first, then
// deletes exactly that run's plays (and their buffer events) in one transaction.
// ?expected= is the count the owner confirmed; a different count deletes nothing.
func (a *api) handleRemoveImportRun(w http.ResponseWriter, r *http.Request) {
	run, ok := a.importRunFromPath(w, r)
	if !ok {
		return
	}
	expected, err := strconv.Atoi(r.URL.Query().Get("expected"))
	if err != nil || expected < 0 {
		a.writeError(w, http.StatusBadRequest, "expected (the count you confirmed) is required")
		return
	}
	if run.Status == insights.RunRunning {
		a.writeError(w, http.StatusConflict, insights.ErrRunRunning.Error())
		return
	}
	if a.deps.Snapshot == nil {
		a.writeError(w, http.StatusInternalServerError, "couldn't take a safety copy first — nothing was deleted")
		return
	}
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "insights.remove-import", Target: jobTarget("run", run.ID),
		Class: jobs.ClassExternalImport, Timeout: 10 * time.Minute,
		Fn: func(bg context.Context, p *jobs.Progress) (any, error) {
			p.SetMessage("Taking a database backup first")
			if _, err := a.insightsSafetyCopy(bg); err != nil {
				return nil, err
			}
			p.SetMessage("Removing the imported plays")
			removed, err := a.deps.Insights.RemoveImportRun(bg, run.ID, expected)
			if err != nil {
				if errors.Is(err, insights.ErrRunRowsChanged) || errors.Is(err, insights.ErrRunRunning) {
					return nil, err
				}
				return nil, fmt.Errorf("nothing was removed: %w", err)
			}
			a.deps.Log.Info("insights: removed an import's plays", "run", run.ID, "removed", removed)
			p.SetMessage(fmt.Sprintf("Removed %d imported plays", removed))
			return map[string]int64{"removed": removed}, nil
		}})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, nil)
}

// handleImportOverlaps counts imported plays that double up plays Arrmada recorded live
// (imports made before the importer skipped those), for the repair button.
func (a *api) handleImportOverlaps(w http.ResponseWriter, r *http.Request) {
	n, first, err := a.deps.Insights.ImportOverlaps(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "couldn't count double-counted plays")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"count": n, "first_live_at": first})
}

// handleRemoveImportOverlaps removes those double-counted imported plays as a job: a
// database backup first (no backup, no delete), then one transaction that deletes only
// imported rows. expected is the count the owner confirmed; if more or fewer plays match
// by the time it runs, nothing is deleted.
func (a *api) handleRemoveImportOverlaps(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Expected *int `json:"expected"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Expected == nil || *req.Expected < 0 {
		a.writeError(w, http.StatusBadRequest, "expected (the count you confirmed) is required")
		return
	}
	if a.deps.Snapshot == nil {
		a.writeError(w, http.StatusInternalServerError, "couldn't take a safety copy first — nothing was deleted")
		return
	}
	expected := *req.Expected
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "insights.remove-overlaps", Target: "all", Class: jobs.ClassExternalImport, Timeout: 10 * time.Minute,
		Fn: func(bg context.Context, p *jobs.Progress) (any, error) {
			p.SetMessage("Taking a database backup first")
			if _, err := a.insightsSafetyCopy(bg); err != nil {
				return nil, err
			}
			p.SetMessage("Removing double-counted plays")
			removed, err := a.deps.Insights.RemoveImportOverlaps(bg, expected)
			if err != nil {
				if errors.Is(err, insights.ErrOverlapsChanged) {
					return nil, err
				}
				return nil, fmt.Errorf("nothing was removed: %w", err)
			}
			a.deps.Log.Info("insights: removed imported plays that doubled live ones", "removed", removed)
			p.SetMessage(fmt.Sprintf("Removed %d double-counted plays", removed))
			return map[string]int64{"removed": removed}, nil
		}})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, nil)
}

// insightsSafetyCopy backs the database up before Insights deletes watch history in bulk.
func (a *api) insightsSafetyCopy(ctx context.Context) (string, error) {
	path, err := a.deps.Snapshot(ctx, string(store.BackupPreInsightsRepair))
	if err != nil {
		a.deps.Log.Error("insights: safety copy before a history cleanup failed", "err", err)
		return "", fmt.Errorf("couldn't take a safety copy first — nothing was deleted (%w)", err)
	}
	a.deps.Log.Info("insights: database copied before a history cleanup", "backup", path)
	return path, nil
}
