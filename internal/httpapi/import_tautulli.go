package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/store"
	"github.com/tristenlammi/arrmada/internal/tautulli"
)

// handleImportTautulli backfills Insights with a Tautulli watch history so its stats/graphs aren't
// empty on day one. Verifies the connection, then streams the full history in the background.
// Re-running is safe: plays already imported, and plays Arrmada recorded live, are skipped.
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
	if strings.TrimSpace(req.URL) == "" || strings.TrimSpace(req.APIKey) == "" {
		a.writeError(w, http.StatusBadRequest, "url and api_key are required")
		return
	}
	client := tautulli.New(req.URL, req.APIKey)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	opts := insights.ImportOptions{Before: max(req.Before, 0)}

	// One import at a time (the job runner's single-flight), so a double-clicked import
	// doesn't interleave duplicate inserts.
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "insights.import-tautulli", Target: "all", Class: jobs.ClassExternalImport, Timeout: 30 * time.Minute,
		Fn: func(bg context.Context, p *jobs.Progress) (any, error) {
			client := tautulli.New(req.URL, req.APIKey)
			var counts insights.ImportCounts
			err := client.History(bg, func(rows []tautulli.Row) error {
				counts.Add(a.deps.Insights.ImportHistory(bg, importedSessions(rows), opts))
				return nil
			})
			if err != nil {
				a.deps.Log.Warn("tautulli import failed", "imported", counts.Imported, "err", err)
				return counts, err
			}
			a.deps.Log.Info("tautulli import finished", "imported", counts.Imported, "duplicates", counts.Duplicate,
				"recorded_live", counts.Overlap, "invalid", counts.Invalid, "after_cutoff", counts.AfterCutoff,
				"failed", counts.Failed, "first_error", counts.FirstError)
			p.SetMessage(fmt.Sprintf("Imported %d plays (%d already here, %d recorded live)", counts.Imported, counts.Duplicate, counts.Overlap))
			return counts, nil
		}})
	if !ok {
		return
	}
	if existing {
		a.alreadyRunning(w, jobID, "an import is already running")
		return
	}
	a.accepted(w, jobID, false, map[string]any{"status": "started"})
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
