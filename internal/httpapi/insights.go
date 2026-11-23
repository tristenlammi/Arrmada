package httpapi

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/netsafe"
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

// validateInsightsImagePath is validatePlexImagePath, or a Jellyfin image path in its exact
// strict form (anything else with the jellyfin: prefix is refused, never cleaned up).
func validateInsightsImagePath(raw string) (string, bool) {
	if strings.HasPrefix(raw, "jellyfin:") {
		return raw, insights.IsJellyfinImage(raw)
	}
	if strings.HasPrefix(raw, "emby:") { // an Emby poster: "emby:/Items/<digits>/Images/Primary"
		return raw, insights.IsEmbyImage(raw)
	}
	return validatePlexImagePath(raw)
}

// handleInsightsServers answers GET /api/v1/insights/servers: each media server Insights can
// watch, whether it's connected and what its monitoring is doing — the page's server chips
// and badges.
func (a *api) handleInsightsServers(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]any{"servers": a.deps.Insights.Servers(r.Context())})
}

// handleJellyfinMonitoring answers PUT /api/v1/jellyfin/monitoring {monitoring_enabled?,
// poll_seconds?}: Jellyfin's "record what's played" switch and its poll interval. Like
// Plex's monitoring switch, it's a manager's to change (the connection itself is an
// admin's). Answers with the saved connection.
func (a *api) handleJellyfinMonitoring(w http.ResponseWriter, r *http.Request) {
	if !a.jellyfinReady(w) {
		return
	}
	var req struct {
		MonitoringEnabled *bool `json:"monitoring_enabled"`
		PollSeconds       *int  `json:"poll_seconds"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Insights.SetJellyfinMonitoring(r.Context(), req.MonitoringEnabled, req.PollSeconds); err != nil {
		if errors.Is(err, insights.ErrNoJellyfin) {
			a.writeError(w, http.StatusServiceUnavailable, "Jellyfin isn't available")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not save Jellyfin monitoring")
		return
	}
	a.recheckHealth(r, jellyfinHealthKey)
	a.writeJSON(w, http.StatusOK, a.deps.Jellyfin.Config(r.Context()))
}

// handleInsightsConfig returns the Plex connection settings (token presence only, never the value).
func (a *api) handleInsightsConfig(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Insights.Config(r.Context()))
}

// handleUpdateInsightsConfig persists the Plex connection settings. Every field is
// optional: Settings → Media servers' Plex monitoring switch and poll interval each save on their own
// ({"enabled": false}), without sending the server address back; a missing url keeps the
// saved one, like a missing token does.
func (a *api) handleUpdateInsightsConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL         *string `json:"url"`
		Token       *string `json:"token"`
		Enabled     *bool   `json:"enabled"`
		PollSeconds *int    `json:"poll_seconds"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	u := a.deps.Insights.Config(r.Context()).URL
	if req.URL != nil {
		u = *req.URL
	}
	if err := a.deps.Insights.SetConfig(r.Context(), u, req.Token, req.Enabled, req.PollSeconds); err != nil {
		if errors.Is(err, insights.ErrTokenForNewAddress) {
			a.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not save Plex settings")
		return
	}
	a.recheckHealth(r, "plex.connection") // a reconnected Plex clears its warning now
	a.writeJSON(w, http.StatusOK, a.deps.Insights.Config(r.Context()))
}

// handlePlexMonitoring answers PUT /api/v1/insights/plex/monitoring {enabled?,
// poll_seconds?}: watch monitoring, which managers run, without the connection (the
// admin's). Answers with the saved connection.
func (a *api) handlePlexMonitoring(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled     *bool `json:"enabled"`
		PollSeconds *int  `json:"poll_seconds"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.PollSeconds != nil && (*req.PollSeconds < 2 || *req.PollSeconds > 60) {
		a.writeError(w, http.StatusBadRequest, "the interval must be 2 to 60 seconds")
		return
	}
	if err := a.deps.Insights.SetMonitoring(r.Context(), req.Enabled, req.PollSeconds); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save Plex monitoring")
		return
	}
	a.recheckHealth(r, "plex.connection")
	a.writeJSON(w, http.StatusOK, a.deps.Insights.Config(r.Context()))
}

// handleInsightsTest validates a Plex connection (using provided or stored credentials).
// Only an admin tests another address or token; a manager tests the saved connection.
func (a *api) handleInsightsTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if !a.decodeJSON(w, r, &req) { // send {} to test the stored config
		return
	}
	if me, _ := userFrom(r); me == nil || !me.Role.AtLeast(auth.RoleAdmin) {
		if req.Token != "" || (strings.TrimSpace(req.URL) != "" && !netsafe.SameEndpoint(req.URL, a.deps.Insights.Config(r.Context()).URL)) {
			a.writeError(w, http.StatusForbidden, "Only an admin can test another Plex address or token; you can test the saved connection.")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := a.deps.Insights.Test(ctx, req.URL, req.Token)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, res)
}

// handleInsightsPlexAuthStart begins a "Sign in with Plex" flow: returns the PIN id
// and the plex.tv URL the UI opens for the user to authorize.
func (a *api) handleInsightsPlexAuthStart(w http.ResponseWriter, r *http.Request) {
	me, _ := userFrom(r)
	if me == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	// ?mode=redirect comes back to the Plex card on Settings → Connections, which finishes
	// the connection.
	var forward func(int) string
	if plexRedirectMode(r) {
		if _, ok := plexReturnBase(r); !ok {
			a.writeError(w, http.StatusBadRequest, "Couldn't work out this site's address for Plex to send you back to.")
			return
		}
		forward = func(pin int) string {
			u, _ := plexForwardURL(r, "/settings/connections?plexpin="+strconv.Itoa(pin))
			return u
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	auth, err := a.deps.Insights.StartPlexAuth(ctx, forward)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := a.bindPlexPin(w, r, plexFlowConnect, me.ID, auth.ID); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not start Plex sign-in")
		return
	}
	a.writeJSON(w, http.StatusOK, auth)
}

// handleInsightsPlexAuthPoll checks whether the user has authorized the sign-in; on
// success the token (and, if unset, the server URL) are stored.
func (a *api) handleInsightsPlexAuthPoll(w http.ResponseWriter, r *http.Request) {
	// Read as an int, the type every Plex PIN id is kept in, so nothing converts it.
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	me, _ := userFrom(r)
	if me == nil || !a.plexPinOwned(r, plexFlowConnect, me.ID, id) {
		a.writeError(w, http.StatusForbidden, errPlexPinElsewhere)
		return
	}
	// Long enough to try each of the owner's server addresses in turn (3 s each) once approved.
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := a.deps.Insights.PollPlexAuth(ctx, id)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if res.Authorized {
		a.plexPins.forget(id)
		a.recheckHealth(r, "plex.connection") // a fresh connection clears its warning now
	}
	a.writeJSON(w, http.StatusOK, res)
}

// handleInsightsActivity returns the current live Plex streams (the Activity view).
func (a *api) handleInsightsActivity(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	act, err := a.deps.Insights.Activity(ctx, serverParam(r))
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
	f := historyFilter(r)
	f.Limit, f.Offset = size, (page-1)*size
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := a.deps.Insights.History(ctx, f)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load history")
		return
	}
	a.writeJSON(w, http.StatusOK, res)
}

// historyFilter reads History's filters from the query string: user (a Plex user id), type,
// decision, q, from and to (epoch seconds, to exclusive) and group=1.
func historyFilter(r *http.Request) insights.HistoryFilter {
	q := r.URL.Query()
	epoch := func(k string) int64 {
		n, err := strconv.ParseInt(q.Get(k), 10, 64)
		if err != nil || n < 0 {
			return 0
		}
		return n
	}
	return insights.HistoryFilter{
		UserID:   q.Get("user"),
		Type:     q.Get("type"),
		Decision: q.Get("decision"),
		Query:    q.Get("q"),
		From:     epoch("from"),
		To:       epoch("to"),
		Group:    q.Get("group") == "1",
		Server:   serverParam(r),
	}
}

// serverParam reads ?server= on an Insights read: "plex", "jellyfin", or both (absent,
// "all", or anything else).
func serverParam(r *http.Request) string {
	return insights.NormalizeServer(r.URL.Query().Get("server"))
}

// handleInsightsHistoryCSV downloads the plays History's filters match (newest first, up to
// insights.HistoryExportMax) as CSV. Staff only, like the History it mirrors; nothing in it
// is logged.
func (a *api) handleInsightsHistoryCSV(w http.ResponseWriter, r *http.Request) {
	f := historyFilter(r)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="watch-history-`+time.Now().Format("2006-01-02")+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"when", "user", "title", "show", "season_episode", "player", "platform", "ip", "decision", "watched_secs", "progress_pct", "sessions", "server"})
	rows := 0
	err := a.deps.Insights.HistoryExport(ctx, f, func(e insights.HistoryEntry) error {
		rows++
		se := ""
		if e.MediaType == "episode" {
			se = fmt.Sprintf("S%02dE%02d", e.ParentIndex, e.MediaIndex)
		}
		sessions := e.Sessions
		if sessions == 0 {
			sessions = 1
		}
		return cw.Write([]string{
			time.Unix(e.StartedAt, 0).UTC().Format(time.RFC3339), csvCell(e.UserName), csvCell(e.Title), csvCell(e.GrandparentTitle), se,
			csvCell(e.Player), csvCell(e.Platform), csvCell(e.IPAddress), e.Decision,
			strconv.FormatInt(e.WatchedSecs, 10), strconv.Itoa(e.ProgressPct), strconv.Itoa(sessions), e.Server,
		})
	})
	if err != nil && rows == 0 {
		// Nothing has gone out but the header yet: say so as an error instead of a file
		// that looks like an empty history.
		w.Header().Del("Content-Disposition")
		w.Header().Del("Cache-Control")
		a.writeError(w, http.StatusInternalServerError, "could not export history")
		return
	}
	cw.Flush()
}

// csvCell keeps a spreadsheet from running a cell as a formula: a value starting with
// = + - @ (a title, a player name) gets a leading apostrophe.
func csvCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

// handleInsightsStats returns the home watch-statistics cards over a window.
func (a *api) handleInsightsStats(w http.ResponseWriter, r *http.Request) {
	window := clampWindow(atoiQuery(r, "window"))
	byDuration := r.URL.Query().Get("metric") == "duration"
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := a.deps.Insights.Stats(ctx, window, byDuration, serverParam(r))
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
	g, err := a.deps.Insights.Graphs(ctx, window, r.URL.Query().Get("metric"), serverParam(r))
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
	res, err := a.deps.Insights.Reliability(ctx, window, serverParam(r))
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
	users, err := a.deps.Insights.Users(ctx, serverParam(r))
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load users")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// handleInsightsPerson returns one Plex user's People page (?window=days, 0 = all time).
// Staff only: it carries their devices and IP addresses.
func (a *api) handleInsightsPerson(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	p, err := a.deps.Insights.Person(ctx, id, clampWindow(atoiQuery(r, "window")))
	if errors.Is(err, insights.ErrPersonNotFound) {
		a.writeError(w, http.StatusNotFound, "no one by that id has watched anything")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load this person")
		return
	}
	a.writeJSON(w, http.StatusOK, p)
}

// handleInsightsLibrary returns the Insights Library tab (?window=days, ?metric=duration),
// each title linked to its Arrmada page when Arrmada has it. It only links: nothing here
// queues a conversion.
func (a *api) handleInsightsLibrary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	v, err := a.deps.Insights.Library(ctx, clampWindow(atoiQuery(r, "window")), r.URL.Query().Get("metric") == "duration", serverParam(r))
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not build the library view")
		return
	}
	var items []*insights.LibraryItem
	for i := range v.MostWatchedMovies {
		items = append(items, &v.MostWatchedMovies[i])
	}
	for i := range v.MostWatchedShows {
		items = append(items, &v.MostWatchedShows[i])
	}
	for i := range v.NeverWatched {
		items = append(items, &v.NeverWatched[i])
	}
	for i := range v.HotSpots {
		items = append(items, &v.HotSpots[i].LibraryItem)
	}
	a.linkLibraryItems(ctx, items)
	a.writeJSON(w, http.StatusOK, v)
}

// linkLibraryItems fills each title's Arrmada id from its TMDB id, one lookup per kind.
func (a *api) linkLibraryItems(ctx context.Context, items []*insights.LibraryItem) {
	var movieIDs, seriesIDs []int
	for _, it := range items {
		switch {
		case it.TMDB <= 0:
		case it.Media == "movie":
			movieIDs = append(movieIDs, it.TMDB)
		case it.Media == "series":
			seriesIDs = append(seriesIDs, it.TMDB)
		}
	}
	movieByTMDB, seriesByTMDB := map[int]int64{}, map[int]int64{}
	if a.deps.Movies != nil && len(movieIDs) > 0 {
		if ms, err := a.deps.Movies.ByTMDBIDs(ctx, movieIDs); err == nil {
			for _, m := range ms {
				movieByTMDB[m.TMDBID] = m.ID
			}
		}
	}
	if a.deps.Series != nil && len(seriesIDs) > 0 {
		if ss, err := a.deps.Series.ByTMDBIDs(ctx, seriesIDs); err == nil {
			for _, s := range ss {
				seriesByTMDB[s.TMDBID] = s.ID
			}
		}
	}
	for _, it := range items {
		if it.Media == "movie" {
			it.ArrmadaID = movieByTMDB[it.TMDB]
		} else if it.Media == "series" {
			it.ArrmadaID = seriesByTMDB[it.TMDB]
		}
	}
}

// handleInsightsLibraries returns Plex library sections with counts (live).
func (a *api) handleInsightsLibraries(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	libs, err := a.deps.Insights.Libraries(ctx, serverParam(r))
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
	items, err := a.deps.Insights.RecentlyAdded(ctx, limit, serverParam(r))
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleInsightsImage proxies a Plex or Jellyfin poster/art image so the browser never sees
// the token or key. Only Plex image paths under /library/ or /photo/ are allowed (no
// arbitrary Plex-endpoint proxying), and only the two strict Jellyfin forms
// "jellyfin:/Items/<32 hex>/Images/Primary" and "jellyfin:/Users/<32 hex>/Images/Primary".
func (a *api) handleInsightsImage(w http.ResponseWriter, r *http.Request) {
	clean, ok := validateInsightsImagePath(r.URL.Query().Get("path"))
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
