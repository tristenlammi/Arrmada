package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/tautulli"
)

// handleImportTautulli backfills Insights with a Tautulli watch history so its stats/graphs aren't
// empty on day one. Verifies the connection, then streams the full history in the background
// (idempotent — re-running skips sessions already imported).
func (a *api) handleImportTautulli(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
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

	// One import at a time (the job runner's single-flight), so a double-clicked import
	// doesn't interleave duplicate inserts.
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "insights.import-tautulli", Target: "all", Class: jobs.ClassExternalImport, Timeout: 30 * time.Minute,
		Fn: func(bg context.Context, p *jobs.Progress) (any, error) {
			client := tautulli.New(req.URL, req.APIKey)
			var imported, skipped int
			err := client.History(bg, func(rows []tautulli.Row) error {
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
				imp, skp := a.deps.Insights.ImportHistory(bg, sessions)
				imported += imp
				skipped += skp
				return nil
			})
			if err != nil {
				a.deps.Log.Warn("tautulli import failed", "imported", imported, "err", err)
				return map[string]int{"imported": imported, "skipped": skipped}, err
			}
			a.deps.Log.Info("tautulli import finished", "imported", imported, "skipped", skipped)
			p.SetMessage(fmt.Sprintf("Imported %d plays (%d already here)", imported, skipped))
			return map[string]int{"imported": imported, "skipped": skipped}, nil
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
