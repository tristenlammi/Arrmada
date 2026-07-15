package httpapi

import (
	"context"
	"net/http"
	"time"
)

// handleInsightsConfig returns the Plex connection settings (token presence only, never the value).
func (a *api) handleInsightsConfig(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Insights.Config(r.Context()))
}

// handleUpdateInsightsConfig persists the Plex connection settings.
func (a *api) handleUpdateInsightsConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL         string  `json:"url"`
		Token       *string `json:"token"`
		Enabled     *bool   `json:"enabled"`
		PollSeconds *int    `json:"poll_seconds"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Insights.SetConfig(r.Context(), req.URL, req.Token, req.Enabled, req.PollSeconds); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save Plex settings")
		return
	}
	a.writeJSON(w, http.StatusOK, a.deps.Insights.Config(r.Context()))
}

// handleInsightsTest validates a Plex connection (using provided or stored credentials).
func (a *api) handleInsightsTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if !a.decodeJSON(w, r, &req) { // send {} to test the stored config
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	a.writeJSON(w, http.StatusOK, a.deps.Insights.Test(ctx, req.URL, req.Token))
}
