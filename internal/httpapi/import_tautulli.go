package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/insights"
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

	// Serialize imports process-wide so a double-clicked import doesn't interleave duplicate inserts.
	if !a.deps.Insights.TryStartImport() {
		a.writeError(w, http.StatusConflict, "an import is already running")
		return
	}

	go func() {
		defer a.deps.Insights.StopImport()
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
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
					StartedAt: r.Started, StoppedAt: r.Stopped, DurationMS: r.DurationSec * 1000, PausedMS: r.PausedSec * 1000,
				})
			}
			imp, skp := a.deps.Insights.ImportHistory(bg, sessions)
			imported += imp
			skipped += skp
			return nil
		})
		if err != nil {
			a.deps.Log.Warn("tautulli import failed", "imported", imported, "err", err)
			return
		}
		a.deps.Log.Info("tautulli import finished", "imported", imported, "skipped", skipped)
	}()

	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "started"})
}
