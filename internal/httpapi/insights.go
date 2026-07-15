package httpapi

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/insights"
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

// handleInsightsActivity returns the current live Plex streams (the Activity view).
func (a *api) handleInsightsActivity(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	act, err := a.deps.Insights.Activity(ctx)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, act)
}

// handleInsightsHistory returns a paginated page of recorded plays with filters.
func (a *api) handleInsightsHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(q.Get("page_size"))
	if size <= 0 {
		size = 50
	}
	f := insights.HistoryFilter{
		UserID:   q.Get("user"),
		Type:     q.Get("type"),
		Decision: q.Get("decision"),
		Query:    q.Get("q"),
		Limit:    size,
		Offset:   (page - 1) * size,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := a.deps.Insights.History(ctx, f)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load history")
		return
	}
	a.writeJSON(w, http.StatusOK, res)
}

// handleInsightsStats returns the home watch-statistics cards over a window.
func (a *api) handleInsightsStats(w http.ResponseWriter, r *http.Request) {
	window, _ := strconv.Atoi(r.URL.Query().Get("window"))
	byDuration := r.URL.Query().Get("metric") == "duration"
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := a.deps.Insights.Stats(ctx, window, byDuration)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not compute stats")
		return
	}
	a.writeJSON(w, http.StatusOK, res)
}

// handleInsightsUsers returns per-user activity aggregates.
func (a *api) handleInsightsUsers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	users, err := a.deps.Insights.Users(ctx)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load users")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// handleInsightsLibraries returns Plex library sections with counts (live).
func (a *api) handleInsightsLibraries(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	libs, err := a.deps.Insights.Libraries(ctx)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"libraries": libs})
}

// handleInsightsRecentlyAdded returns recently-added items (live from Plex).
func (a *api) handleInsightsRecentlyAdded(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	items, err := a.deps.Insights.RecentlyAdded(ctx, limit)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleInsightsImage proxies a Plex poster/art image so the browser never sees the token. Only
// image paths under /library/ or /photo/ are allowed (no arbitrary Plex-endpoint proxying).
func (a *api) handleInsightsImage(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if !strings.HasPrefix(path, "/library/") && !strings.HasPrefix(path, "/photo/") {
		a.writeError(w, http.StatusBadRequest, "unsupported image path")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	resp, err := a.deps.Insights.Image(ctx, path)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not load image")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = io.Copy(w, resp.Body)
}
