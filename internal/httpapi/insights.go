package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/insights"
)

// Query-parameter clamps: user-supplied window/limit/page_size values flow into
// downstream code that allocates per-day/per-row slices, so an unbounded value
// (e.g. window=10000000) is a memory-exhaustion vector. These bound the input
// while leaving the "<= 0 means use the downstream default" contract intact.
const (
	maxWindowDays = 365 // ~1 year of daily buckets is already generous
	maxPageSize   = 200 // history/recently-added page cap
)

// clampWindow bounds an explicit positive window to [1, maxWindowDays]. Values
// <= 0 are passed through unchanged so the service layer applies its own default
// (30 days); negatives also fall through to that default and never allocate.
func clampWindow(w int) int {
	if w <= 0 {
		return w
	}
	if w > maxWindowDays {
		return maxWindowDays
	}
	return w
}

// clampPageSize bounds an explicit positive limit/page_size to at most
// maxPageSize. Values <= 0 pass through so the service default applies.
func clampPageSize(n int) int {
	if n > maxPageSize {
		return maxPageSize
	}
	return n
}

// atoiQuery reads a query parameter as an int, defaulting to 0 on absence or
// parse error (which the service layer treats as "use the default").
func atoiQuery(r *http.Request, key string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(key))
	return n
}

// validatePlexImagePath enforces that a proxied Plex image path is a bare,
// normalized path under /library/ or /photo/ — nothing else. It returns the
// cleaned path to forward to Plex and false if the input must be rejected.
//
// The check defeats path-traversal SSRF: a raw prefix check alone lets
// "/library/../status/sessions" through (Plex normalizes the dot-segments and
// returns arbitrary read-only API data under the server's admin token). We
// parse the path, reject any query/fragment (real thumb paths carry neither —
// they look like "/library/metadata/123/thumb/1700000000"), path.Clean it, and
// require the CLEANED path to still start with an allowed prefix so "../"
// escapes are caught after normalization.
func validatePlexImagePath(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	// A query or fragment could inject arbitrary Plex API params (e.g.
	// ?X-Plex-Token=...) or smuggle an endpoint; real image paths have none.
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", false
	}
	// Reject scheme-relative ("//evil") or absolute-URL forms — image paths are
	// server-relative and must not carry a scheme or host.
	if u.Scheme != "" || u.Host != "" || u.Opaque != "" {
		return "", false
	}
	p := u.Path
	if !strings.HasPrefix(p, "/") {
		return "", false
	}
	clean := path.Clean(p)
	// Defense in depth: path.Clean already resolves "..", but reject any residue.
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || strings.HasSuffix(clean, "/..") {
		return "", false
	}
	if !strings.HasPrefix(clean, "/library/") && !strings.HasPrefix(clean, "/photo/") {
		return "", false
	}
	return clean, true
}

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

// handleInsightsPlexAuthStart begins a "Sign in with Plex" flow: returns the PIN id
// and the plex.tv URL the UI opens for the user to authorize.
func (a *api) handleInsightsPlexAuthStart(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	auth, err := a.deps.Insights.StartPlexAuth(ctx)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, auth)
}

// handleInsightsPlexAuthPoll checks whether the user has authorized the sign-in; on
// success the token (and, if unset, the server URL) are stored.
func (a *api) handleInsightsPlexAuthPoll(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	authorized, err := a.deps.Insights.PollPlexAuth(ctx, int(id))
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"authorized": authorized})
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
	size = clampPageSize(size)
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
	window := clampWindow(atoiQuery(r, "window"))
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

// handleInsightsGraphs returns the time-series bundle for the Graphs tab.
func (a *api) handleInsightsGraphs(w http.ResponseWriter, r *http.Request) {
	window := clampWindow(atoiQuery(r, "window"))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	g, err := a.deps.Insights.Graphs(ctx, window)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not compute graphs")
		return
	}
	a.writeJSON(w, http.StatusOK, g)
}

// handleInsightsReliability returns the buffering-history bundle.
func (a *api) handleInsightsReliability(w http.ResponseWriter, r *http.Request) {
	window := clampWindow(atoiQuery(r, "window"))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := a.deps.Insights.Reliability(ctx, window)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not compute reliability")
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
	limit := clampPageSize(atoiQuery(r, "limit"))
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
	clean, ok := validatePlexImagePath(r.URL.Query().Get("path"))
	if !ok {
		a.writeError(w, http.StatusBadRequest, "unsupported image path")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	resp, err := a.deps.Insights.Image(ctx, clean)
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
