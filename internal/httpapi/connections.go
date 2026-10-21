package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/health"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/jellyfinsvc"
	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Settings → Connections answers "is everything connected and working?" in one place:
// every indexer, download client, Prowlarr, FlareSolverr, metadata key, Plex, the alert
// connections and the audiobook server, each with how it has been answering. The list is
// read from what's already known (the integration status tracker, Plex's poll record,
// the alert delivery log) — loading the page never calls anything outside Arrmada. Only
// Test does, for the one connection asked about.

// Kinds the status tracker doesn't define itself; connstatus has indexer, download_client,
// flaresolverr and prowlarr.
const (
	connKindAPIKey        = "apikey"
	connKindPlex          = "plex"
	connKindJellyfin      = "jellyfin"
	connKindNotifications = "notifications"
	connKindAudioServer   = "audioserver"

	// The one-of-a-kind connections all use this ref (and main records FlareSolverr's
	// pings under it).
	prowlarrRef     = indexer.ProwlarrStatusRef
	flareSolverrRef = "default"
	singletonRef    = "default"
)

// States beyond connstatus's phases (ok, failing, backing_off, unknown).
const (
	connStateDisabled = "disabled" // switched off: nothing asks it, so its old state isn't health
	connStateWarn     = "warn"     // working, with something worth a look
)

// connectionItem is one card. Card is its anchor on the page ("indexer-12", "flaresolverr"),
// which health warnings link to. Nothing here is a secret: no key, password, token or URL
// query string, and every error has been through connstatus.Redact.
type connectionItem struct {
	Kind                string            `json:"kind"`
	Ref                 string            `json:"ref"`
	Card                string            `json:"card"`
	Name                string            `json:"name"`
	Type                string            `json:"type,omitempty"`     // torznab, qbittorrent… for the kind label
	Subtitle            string            `json:"subtitle,omitempty"` // its address (scheme, host and path only) or what it's for
	Configured          bool              `json:"configured"`
	Enabled             bool              `json:"enabled"`
	State               string            `json:"state"`
	Detail              string            `json:"detail,omitempty"`
	Source              string            `json:"source,omitempty"` // a key or URL saved in the app ("settings") or from the install ("env")
	LastOKAt            *time.Time        `json:"last_ok_at,omitempty"`
	LastError           string            `json:"last_error,omitempty"`
	LastErrorAt         *time.Time        `json:"last_error_at,omitempty"`
	FailingSince        *time.Time        `json:"failing_since,omitempty"`
	BackoffUntil        *time.Time        `json:"backoff_until,omitempty"`
	CheckedAt           *time.Time        `json:"checked_at,omitempty"`
	ConsecutiveFailures int               `json:"consecutive_failures,omitempty"`
	Counts              *connectionCounts `json:"counts,omitempty"`
	Testable            bool              `json:"testable"`
	ManagedBy           string            `json:"managed_by,omitempty"`
	FixPath             string            `json:"fix_path,omitempty"` // where it's edited, when that isn't its card
	// UsedFor is what Arrmada uses a media server for right now (watch, library, signin,
	// insights): only what's switched on and built, so the card's chips are true.
	UsedFor []string `json:"used_for,omitempty"`
}

// connectionCounts is an indexer's last 24 hours.
type connectionCounts struct {
	Queries24h  int `json:"queries_24h"`
	Failures24h int `json:"failures_24h"`
	Grabs24h    int `json:"grabs_24h"`
}

type connectionSection struct {
	ID    string           `json:"id"`
	Title string           `json:"title"`
	Items []connectionItem `json:"items"`
}

// withState fills in what the tracker knows. Something never asked reads "unknown".
func (it *connectionItem) withState(st connstatus.State, now time.Time) {
	it.State = st.Phase(now)
	it.LastOKAt, it.LastErrorAt, it.FailingSince = timePtr(st.LastOKAt), timePtr(st.LastErrorAt), timePtr(st.FailingSince)
	it.ConsecutiveFailures = st.ConsecutiveFailures
	if st.ConsecutiveFailures > 0 {
		it.LastError = st.LastError
	}
	if now.Before(st.BackoffUntil) {
		it.BackoffUntil = timePtr(st.BackoffUntil)
	}
	it.CheckedAt = laterOf(it.LastOKAt, it.LastErrorAt)
}

// disable marks a connection that's switched off: its last state is history, not health.
func (it *connectionItem) disable() {
	it.State, it.BackoffUntil = connStateDisabled, nil
}

func laterOf(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil || a.After(*b):
		return a
	}
	return b
}

// handleConnections is GET /api/v1/connections: every integration, grouped as the page
// shows them (search, downloads, metadata, other).
func (a *api) handleConnections(w http.ResponseWriter, r *http.Request) {
	sections, err := a.connectionSections(r.Context(), isAdminRequest(r))
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the connections")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"sections": sections})
}

func isAdminRequest(r *http.Request) bool {
	u, _ := userFrom(r)
	return u != nil && u.Role.AtLeast(auth.RoleAdmin)
}

func (a *api) connectionSections(ctx context.Context, admin bool) ([]connectionSection, error) {
	now := time.Now()
	search, err := a.searchConnections(ctx, now)
	if err != nil {
		return nil, err
	}
	downloads, err := a.downloadConnections(ctx, now)
	if err != nil {
		return nil, err
	}
	return []connectionSection{
		{ID: "search", Title: "Search", Items: search},
		{ID: "downloads", Title: "Downloads", Items: downloads},
		{ID: "metadata", Title: "Metadata", Items: a.metadataConnections(ctx, now, admin)},
		{ID: "other", Title: "Alerts and apps", Items: a.otherConnections(ctx, now)},
	}, nil
}

// findConnection is one item, as the list would show it (Test answers with it).
func (a *api) findConnection(ctx context.Context, kind, ref string, admin bool) (connectionItem, bool) {
	sections, err := a.connectionSections(ctx, admin)
	if err != nil {
		return connectionItem{}, false
	}
	for _, s := range sections {
		for _, it := range s.Items {
			if it.Kind == kind && it.Ref == ref {
				return it, true
			}
		}
	}
	return connectionItem{}, false
}

// searchConnections: Prowlarr, FlareSolverr and every indexer.
func (a *api) searchConnections(ctx context.Context, now time.Time) ([]connectionItem, error) {
	var list []indexer.Indexer
	if a.deps.Indexers != nil {
		var err error
		if list, err = a.deps.Indexers.List(ctx); err != nil {
			return nil, err
		}
	}
	managed := 0
	var needFlare []string
	for _, i := range list {
		if i.ProwlarrID > 0 {
			managed++
		}
		if a.deps.Indexers.Searched(i) && (i.Kind == indexer.KindTorrentLeech || i.Kind == indexer.KindX1337) {
			needFlare = append(needFlare, i.Name)
		}
	}
	out := []connectionItem{a.prowlarrItem(ctx, now, managed), a.flareSolverrItem(ctx, now, needFlare)}

	grabs := a.grabsByIndexer(ctx)
	for _, i := range list {
		st, counts, _ := a.deps.Indexers.Status(i.ID)
		s := statusFor(i, st, counts, now)
		ref := strconv.FormatInt(i.ID, 10)
		it := connectionItem{
			Kind: connstatus.KindIndexer, Ref: ref, Card: "indexer-" + ref, Name: i.Name, Type: string(i.Kind),
			Subtitle: displayURL(i.URL), Configured: true, Enabled: i.Enabled, Testable: true,
			Counts: &connectionCounts{Queries24h: counts.Queries, Failures24h: counts.Failures, Grabs24h: grabs[i.Name]},
		}
		it.withState(st, now)
		it.State = s.State // statusFor already turns a switched-off indexer "disabled"
		if !i.Enabled {
			it.disable()
		}
		if i.ProwlarrID > 0 {
			it.ManagedBy = "prowlarr"
		}
		switch {
		case i.ManagedNote == indexer.NoteNeedsUsenetClient && a.usenetClient(ctx):
			// The note is from a sync that ran before the usenet client was added.
			it.Detail = "A usenet client is set up now: the next Prowlarr sync switches this back on"
		case i.ManagedNote != "":
			it.Detail = i.ManagedNote
		case i.Enabled && !a.deps.Indexers.Searched(i):
			it.State, it.Detail = connStateWarn, "Not searched until a usenet download client (SABnzbd) is added"
		}
		out = append(out, it)
	}
	return out, nil
}

func (a *api) prowlarrItem(ctx context.Context, now time.Time, managed int) connectionItem {
	base, hasKey := a.deps.Config.ProwlarrURL, false
	if a.deps.Settings != nil {
		base = a.deps.Settings.Get(ctx, keyProwlarrURL, a.deps.Config.ProwlarrURL)
		hasKey = a.deps.Settings.Get(ctx, keyProwlarrKey, "") != ""
	}
	it := connectionItem{
		Kind: connstatus.KindProwlarr, Ref: prowlarrRef, Card: "prowlarr", Name: "Prowlarr",
		Subtitle: displayURL(base), Configured: base != "" && hasKey, Enabled: base != "" && hasKey,
	}
	it.Testable = it.Configured
	st, _ := a.deps.ConnStatus.Get(connstatus.KindProwlarr, prowlarrRef)
	it.withState(st, now)
	switch {
	case managed > 0:
		it.Detail = fmt.Sprintf("Syncs %d indexer%s, hourly", managed, plural(managed))
	case !it.Configured:
		it.Detail = "Not synced yet. Sync once to pull its indexers in."
	}
	// The last sync, scheduled or clicked, is its latest word (its error is redacted).
	if a.deps.Settings != nil {
		if ls, ok := indexer.ReadProwlarrLastSync(ctx, a.deps.Settings); ok && !ls.At.IsZero() {
			when := ls.At.Local().Format("2 Jan 15:04")
			if ls.Error != "" {
				it.Detail = joinDetail(it.Detail, "Last sync failed "+when+": "+ls.Error)
			} else {
				it.Detail = joinDetail(it.Detail, "Last synced "+when)
			}
		}
	}
	return it
}

func (a *api) flareSolverrItem(ctx context.Context, now time.Time, needFlare []string) connectionItem {
	it := connectionItem{
		Kind: connstatus.KindFlareSolverr, Ref: flareSolverrRef, Card: "flaresolverr", Name: "FlareSolverr",
		Subtitle: displayURL(a.deps.FlareSolverr.URL()), Configured: a.deps.FlareSolverr.Configured(),
	}
	it.Enabled, it.Testable = it.Configured, it.Configured
	if a.deps.APIKeys != nil {
		for _, k := range a.deps.APIKeys.Status(ctx) {
			if k.ID == "flaresolverr" {
				it.Source = k.Source
			}
		}
	}
	st, _ := a.deps.ConnStatus.Get(connstatus.KindFlareSolverr, flareSolverrRef)
	it.withState(st, now)
	switch {
	case len(needFlare) > 0 && !it.Configured:
		it.State, it.Detail = connStateWarn, andJoin(needFlare)+" need it to get past Cloudflare"
	case len(needFlare) > 0:
		it.Detail = "Used by " + andJoin(needFlare)
	default:
		it.Detail = "No enabled indexer needs it right now"
	}
	return it
}

// grabsByIndexer is how many releases each indexer (by name, as grabs record it) supplied
// in the last 24 hours. A failure only costs the cards their grab counts.
func (a *api) grabsByIndexer(ctx context.Context) map[string]int {
	out := map[string]int{}
	if a.deps.Store == nil {
		return out
	}
	rows, err := a.deps.Store.DB().QueryContext(ctx,
		`SELECT indexer, COUNT(*) FROM grabs WHERE grabbed_at > `+store.UTCNowTimestamp(a.deps.Store.DB(), "'-1 day'")+` GROUP BY indexer`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if rows.Scan(&name, &n) == nil {
			out[name] = n
		}
	}
	return out
}

// downloadConnections: every download client.
func (a *api) downloadConnections(ctx context.Context, now time.Time) ([]connectionItem, error) {
	if a.deps.Downloads == nil {
		return []connectionItem{}, nil
	}
	list, err := a.deps.Downloads.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]connectionItem, 0, len(list))
	for _, c := range list {
		ref := strconv.FormatInt(c.ID, 10)
		it := connectionItem{
			Kind: connstatus.KindDownloadClient, Ref: ref, Card: "download_client-" + ref, Name: c.Name, Type: string(c.Kind),
			Subtitle: displayURL(c.URL), Configured: true, Enabled: c.Enabled, Testable: true,
		}
		st, _ := a.deps.Downloads.Status(c.ID)
		it.withState(st, now)
		var notes []string
		if c.Bundled {
			notes = append(notes, "Comes with Arrmada")
		}
		if len(list) > 1 {
			notes = append(notes, fmt.Sprintf("Order %d", c.Priority))
		}
		if !c.Enabled {
			it.disable()
			notes = append(notes, "Switched off: gets no new downloads")
		} else if st.ConsecutiveFailures > 0 && len(list) > 1 {
			notes = append(notes, "Stalled-download fail-over is paused while it's down")
		}
		it.Detail = strings.Join(notes, " · ")
		out = append(out, it)
	}
	return out, nil
}

// metadataKeys are the keys that get a card of their own. The OpenSubtitles login rides on
// the API key's card (its Test signs in), and FlareSolverr's URL is a search connection.
var metadataKeys = map[string]bool{"tmdb": true, "tvdb": true, "omdb": true, "hardcover": true, "opensubtitles_api": true, "acoustid": true}

// metadataConnections: TMDB, TheTVDB, OMDb, Hardcover and OpenSubtitles. Only an admin may
// test or change a key, so a manager's cards are read-only.
func (a *api) metadataConnections(ctx context.Context, now time.Time, admin bool) []connectionItem {
	out := []connectionItem{}
	if a.deps.APIKeys == nil {
		return out
	}
	keys := a.keyStatuses(ctx)
	osLogin := 0
	for _, k := range keys {
		if (k.ID == "opensubtitles_username" || k.ID == "opensubtitles_password") && k.Configured {
			osLogin++
		}
	}
	for _, k := range keys {
		if !metadataKeys[k.ID] {
			continue
		}
		name := k.Label
		if k.ID == "opensubtitles_api" {
			name = "OpenSubtitles"
		}
		it := connectionItem{
			Kind: connKindAPIKey, Ref: k.ID, Card: "apikey-" + k.ID, Name: name, Subtitle: k.Purpose,
			Configured: k.Configured, Enabled: k.Configured, Source: k.Source,
			Testable: admin && k.Testable && k.Configured,
		}
		if k.Configured {
			st, _ := a.deps.ConnStatus.Get(connKindAPIKey, k.ID)
			it.withState(st, now)
			// A provider that keeps its own last complaint (OMDb's spent quota) wins when
			// it's newer than anything a Test recorded.
			if k.LastErrorAt != nil && k.LastError != "" && (it.CheckedAt == nil || k.LastErrorAt.After(*it.CheckedAt)) {
				it.State, it.LastError, it.LastErrorAt, it.CheckedAt = connstatus.PhaseFailing, connstatus.Redact(k.LastError), k.LastErrorAt, k.LastErrorAt
			}
			if k.ID == "opensubtitles_api" && osLogin < 2 {
				it.Detail = "Searching works; downloading also needs the username and password"
			}
		} else {
			it.State = connstatus.PhaseUnknown
		}
		out = append(out, it)
	}
	return out
}

// otherConnections: the media servers (Plex, Jellyfin), the alert connections and the
// audiobook server. The page lifts the media servers into a section of their own; the
// rest are edited on their own pages, which FixPath names.
func (a *api) otherConnections(ctx context.Context, now time.Time) []connectionItem {
	out := []connectionItem{}
	if a.deps.Insights != nil {
		out = append(out, a.plexItem(ctx))
	}
	if a.deps.Jellyfin != nil {
		out = append(out, a.jellyfinItem(ctx))
	}
	if a.deps.Notify != nil {
		out = append(out, a.notificationsItem(ctx))
	}
	if a.deps.AudioManager != nil && a.deps.Settings != nil {
		out = append(out, a.audioServerItem(ctx))
	}
	return out
}

// plexItem reads the Plex poll record (insights.PollHealth), the one source the health
// panel and the Insights badge share; it never calls Plex.
func (a *api) plexItem(ctx context.Context) connectionItem {
	h := a.deps.Insights.PollHealth(ctx)
	cfg := a.deps.Insights.Config(ctx)
	it := connectionItem{
		Kind: connKindPlex, Ref: singletonRef, Card: "plex", Name: "Plex", Subtitle: displayURL(cfg.URL),
		Configured: h.Configured, Enabled: h.Configured, Testable: h.Configured, FixPath: health.FixPlexConnection.Path,
		LastOKAt: timePtr(h.LastOKAt), State: connstatus.PhaseUnknown,
	}
	switch {
	case !h.Configured:
	case h.Consecutive > 0:
		it.State, it.ConsecutiveFailures = connstatus.PhaseFailing, h.Consecutive
		it.LastError, it.LastErrorAt, it.FailingSince = connstatus.Redact(h.LastErr), timePtr(h.LastErrAt), timePtr(h.FailingSince)
		if h.Unauthorized {
			it.Detail = "Plex rejected Arrmada's token; reconnect Plex"
		}
	case !h.LastOKAt.IsZero():
		it.State = connstatus.PhaseOK
	}
	it.CheckedAt = laterOf(it.LastOKAt, it.LastErrorAt)
	if h.Configured {
		it.UsedFor = a.plexUses(ctx, h.Monitoring)
	}
	if it.Detail == "" && h.Configured {
		name := cfg.ServerName
		if name == "" {
			name = "Connected"
		}
		if h.Monitoring {
			it.Detail = name + " · watch monitoring on"
		} else {
			it.Detail = name + " · watch monitoring off"
		}
	}
	return it
}

// The "Used for" chips a media server card shows.
const (
	useWatch    = "watch"    // Watch links on titles, notices and pushes
	useLibrary  = "library"  // told to scan after imports, renames and deletes
	useSignIn   = "signin"   // people sign in with their account on it
	useInsights = "insights" // what's played there is recorded
)

// plexUses is what Plex does for Arrmada right now.
func (a *api) plexUses(ctx context.Context, monitoring bool) []string {
	uses := []string{useWatch}
	if a.deps.PlexScan != nil && a.deps.PlexScan.Enabled(ctx) {
		uses = append(uses, useLibrary)
	}
	if a.deps.Settings != nil && a.deps.Settings.GetBool(ctx, "plex_login_enabled", false) {
		uses = append(uses, useSignIn)
	}
	if monitoring {
		uses = append(uses, useInsights)
	}
	return uses
}

// jellyfinItem reads Jellyfin's health record (jellyfinsvc.Health), like Plex's card; it
// never calls Jellyfin. Which server it is (name, version) the card reads from
// GET /jellyfin.
func (a *api) jellyfinItem(ctx context.Context) connectionItem {
	h := a.deps.Jellyfin.Health(ctx)
	it := connectionItem{
		Kind: connKindJellyfin, Ref: singletonRef, Card: "jellyfin", Name: "Jellyfin", Subtitle: displayURL(a.deps.Jellyfin.URL(ctx)),
		Configured: h.Configured, Enabled: h.Configured, Testable: h.Configured, FixPath: health.FixJellyfinConnection.Path,
		LastOKAt: timePtr(h.LastOKAt), State: connstatus.PhaseUnknown,
	}
	switch {
	case !h.Configured:
	case h.Consecutive > 0:
		it.State, it.ConsecutiveFailures = connstatus.PhaseFailing, h.Consecutive
		it.LastError, it.LastErrorAt, it.FailingSince = connstatus.Redact(h.LastErr), timePtr(h.LastErrAt), timePtr(h.FailingSince)
		if h.Unauthorized {
			it.Detail = "Jellyfin rejected Arrmada's API key; paste a new one"
		}
	case !h.LastOKAt.IsZero():
		it.State = connstatus.PhaseOK
	}
	it.CheckedAt = laterOf(it.LastOKAt, it.LastErrorAt)
	if h.Configured {
		// Watch links and library updates are what J1 gives Jellyfin; Insights and
		// sign-in join the list when they're built and switched on.
		it.UsedFor = []string{useWatch}
		if a.deps.JellyfinScan != nil && a.deps.JellyfinScan.Enabled(ctx) {
			it.UsedFor = append(it.UsedFor, useLibrary)
		}
		if h.Monitoring {
			it.UsedFor = append(it.UsedFor, useInsights)
		}
	}
	return it
}

// notificationsItem sums up the alert connections (Apprise and n8n webhooks): how many, and
// how many failed their last delivery. Their URLs hold tokens, so not even a name goes out here.
func (a *api) notificationsItem(ctx context.Context) connectionItem {
	it := connectionItem{
		Kind: connKindNotifications, Ref: singletonRef, Card: "notifications", Name: "Alerts",
		Subtitle: "Discord, Telegram, ntfy, email, n8n and more", FixPath: "/settings/alerts", State: connstatus.PhaseUnknown,
	}
	list, err := a.deps.Notify.List(ctx)
	if err != nil {
		return it
	}
	states, _ := a.deps.Notify.DeliveryStates(ctx)
	total, on, failing := 0, 0, 0
	var lastSent int64
	for _, c := range list {
		if c.Kind == notify.KindWebPush {
			continue // a person's own phone, set up from their Me page
		}
		total++
		if !c.Enabled {
			continue
		}
		on++
		st := states[c.ID]
		if st.Status == "failed" || st.Status == "retrying" {
			failing++
		}
		lastSent = max(lastSent, st.LastSentAt)
	}
	it.Configured, it.Enabled = total > 0, on > 0
	switch {
	case total == 0:
		it.Detail = "No alert connections yet"
	case on == 0:
		it.disable()
		it.Detail = fmt.Sprintf("%d connection%s, all switched off", total, plural(total))
	default:
		it.Detail = fmt.Sprintf("%d connection%s", on, plural(on))
		if failing > 0 {
			it.State = connStateWarn
			it.Detail += fmt.Sprintf(" · %d failed its last delivery", failing)
		} else if lastSent > 0 {
			it.State = connstatus.PhaseOK
		}
	}
	if lastSent > 0 {
		it.LastOKAt = timePtr(time.Unix(lastSent, 0))
		it.CheckedAt = it.LastOKAt
	}
	return it
}

// audioServerItem is the listening apps' server: on or off, and running or why not.
func (a *api) audioServerItem(ctx context.Context) connectionItem {
	on := a.deps.Settings.GetBool(ctx, audioserver.KeyEnabled, false)
	running, lastErr := a.deps.AudioManager.Running()
	it := connectionItem{
		Kind: connKindAudioServer, Ref: singletonRef, Card: "audioserver", Name: "Audiobook server",
		Subtitle: "For listening apps", Configured: on, Enabled: on, Testable: on, FixPath: health.FixAudiobookServer.Path,
	}
	if port := portOf(a.deps.AudioManager.Addr()); port != "" {
		it.Subtitle = "For listening apps · port " + port
	}
	switch {
	case !on:
		it.State, it.Detail = connStateDisabled, "Switched off"
	case running:
		it.State = connstatus.PhaseOK
	default:
		it.State, it.LastError = connstatus.PhaseFailing, connstatus.Redact(lastErr)
		it.Detail = "Switched on but not running"
	}
	return it
}

// handleTestConnection is POST /api/v1/connections/{kind}/{ref}/test: a real check of one
// connection, recorded like any other use, answered with the refreshed card. An unknown
// kind or ref is a 404. Metadata keys have their own admin-only route onto this handler.
func (a *api) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	kind, ref := r.PathValue("kind"), r.PathValue("ref")
	if kind == "" {
		kind = connKindAPIKey // the admin-only /connections/apikey/{ref}/test route
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	detail, err, found := a.testConnection(ctx, kind, ref)
	if !found {
		a.writeError(w, http.StatusNotFound, "no such connection")
		return
	}
	if kind == connKindAPIKey && ref == "tmdb" {
		a.recheckTMDB(r)
	}
	item, ok := a.findConnection(r.Context(), kind, ref, isAdminRequest(r))
	if !ok {
		a.writeError(w, http.StatusNotFound, "no such connection")
		return
	}
	out := map[string]any{"ok": err == nil, "item": item}
	if err != nil {
		out["detail"] = connstatus.Redact(err.Error())
	} else {
		out["detail"] = detail
	}
	a.writeJSON(w, http.StatusOK, out)
}

// errNotFound is a Test for a connection that doesn't exist.
var errNotFound = errors.New("not found")

// testConnection runs one connection's Test. found is false for an unknown kind or ref.
func (a *api) testConnection(ctx context.Context, kind, ref string) (detail string, testErr error, found bool) {
	switch kind {
	case connstatus.KindIndexer:
		id, err := strconv.ParseInt(ref, 10, 64)
		if err != nil || a.deps.Indexers == nil {
			return "", nil, false
		}
		// Recorded by the indexer service, like any search.
		err = a.deps.Indexers.Test(ctx, id)
		if errors.Is(err, indexer.ErrNotFound) {
			return "", nil, false
		}
		return "Connected", err, true

	case connstatus.KindDownloadClient:
		id, err := strconv.ParseInt(ref, 10, 64)
		if err != nil || a.deps.Downloads == nil {
			return "", nil, false
		}
		err = a.deps.Downloads.Test(ctx, id)
		if errors.Is(err, download.ErrNotFound) {
			return "", nil, false
		}
		if err != nil {
			return "", err, true
		}
		// Connected: now whether Arrmada can see where it saves. A client that can't
		// report its paths (or whose preferences can't be read) is just "Connected".
		rep, perr := a.clientPaths(ctx, id)
		if perr != nil {
			return "Connected", nil, true
		}
		if !rep.OK {
			return "", errors.New("Connected, but " + rep.Summary), true
		}
		return "Connected. " + rep.Summary, nil, true

	case connstatus.KindFlareSolverr:
		if ref != flareSolverrRef {
			return "", nil, false
		}
		// Recorded through FlareSolverr's own result hook (see main).
		detail, err := testFlareSolverr(ctx, a.deps.FlareSolverr)
		return detail, err, true

	case connstatus.KindProwlarr:
		if ref != prowlarrRef || a.deps.Settings == nil {
			return "", nil, false
		}
		base := a.deps.Settings.Get(ctx, keyProwlarrURL, a.deps.Config.ProwlarrURL)
		version, err := indexer.ProwlarrStatus(ctx, base, a.deps.Settings.Get(ctx, keyProwlarrKey, ""))
		if !errors.Is(err, indexer.ErrProwlarrNoURL) && !errors.Is(err, indexer.ErrProwlarrNoKey) && ctx.Err() == nil {
			a.deps.ConnStatus.Record(connstatus.KindProwlarr, prowlarrRef, connstatus.Outcome{Err: err, Name: "Prowlarr"})
		}
		if err != nil {
			return "", err, true
		}
		return "Prowlarr " + version + " is answering.", nil, true

	case connKindAPIKey:
		if !metadataKeys[ref] || a.deps.APIKeys == nil {
			return "", nil, false
		}
		if !a.keyConfigured(ctx, ref) {
			return "", errors.New("no key is set to test"), true
		}
		res, err := a.keyTest(ctx, ref, "")
		if err != nil {
			return "", err, true
		}
		a.recordKeyTest(ctx, ref, res.Err)
		return res.Detail, res.Err, true

	case connKindPlex:
		if ref != singletonRef || a.deps.Insights == nil {
			return "", nil, false
		}
		if !a.deps.Insights.PollHealth(ctx).Configured {
			return "", errors.New("Plex isn't connected yet"), true
		}
		// Recorded in Plex's poll record, which the card reads.
		if err := a.deps.Insights.ProbeIdentity(ctx); err != nil {
			return "", err, true
		}
		if name := a.deps.Insights.Config(ctx).ServerName; name != "" {
			return "Plex (" + name + ") is answering.", nil, true
		}
		return "Plex is answering.", nil, true

	case connKindJellyfin:
		if ref != singletonRef || a.deps.Jellyfin == nil {
			return "", nil, false
		}
		if !a.deps.Jellyfin.Configured(ctx) {
			return "", errors.New("Jellyfin isn't connected yet"), true
		}
		// Recorded in Jellyfin's health record, which the card reads.
		si, err := a.deps.Jellyfin.RefreshIdentity(ctx)
		if err != nil {
			return "", errors.New(jellyfinsvc.Describe(err)), true
		}
		if si.ServerName != "" {
			return "Jellyfin (" + si.ServerName + ") is answering.", nil, true
		}
		return "Jellyfin is answering.", nil, true

	case connKindAudioServer:
		if ref != singletonRef || a.deps.AudioManager == nil || a.deps.Settings == nil {
			return "", nil, false
		}
		if !a.deps.Settings.GetBool(ctx, audioserver.KeyEnabled, false) {
			return "", errors.New("the audiobook server is switched off"), true
		}
		if running, lastErr := a.deps.AudioManager.Running(); !running {
			if lastErr == "" {
				lastErr = "it isn't running"
			}
			return "", errors.New(lastErr), true
		}
		return "Listening apps can connect.", nil, true
	}
	return "", errNotFound, false
}

// displayURL is an address as it may be shown: scheme, host and path only. A user:password
// part or a query string (where Torznab feeds and others carry their key) never goes out.
func displayURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.EscapedPath(), "/")
}

// joinDetail adds a sentence to a card's detail line.
func joinDetail(a, b string) string {
	if a == "" {
		return b
	}
	return a + " · " + b
}

// portOf is the port in a listen address (":13378" → "13378").
func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return ""
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// andJoin lists names the way a sentence does: "A", "A and B", "A, B and C".
func andJoin(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
