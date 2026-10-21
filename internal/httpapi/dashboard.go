package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/health"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/libroots"
	"github.com/tristenlammi/arrmada/internal/mediaserver"
	"github.com/tristenlammi/arrmada/internal/plex"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The dashboard is one request that fans out over every subsystem, and any of them
// can be down without that being news — Plex is off, the download client is
// restarting. Each section therefore degrades to empty-with-a-note rather than
// failing the whole page, and the whole thing is bounded so a hung client can't
// hold the page open.
const dashboardTimeout = 8 * time.Second

type dashboardPayload struct {
	Storage     []storageVolume    `json:"storage"`
	Streams     *insights.Activity `json:"streams,omitempty"`
	StreamsNote string             `json:"streams_note,omitempty"`
	// PlexConfigured: a server URL and token are set. Without one there's nothing to be
	// unreachable, so StreamsNote stays empty and the page offers to connect instead.
	PlexConfigured bool `json:"plex_configured"`
	// JellyfinConfigured: a Jellyfin address and key are set. Now playing lists both
	// servers' streams, each with its server.
	JellyfinConfigured bool          `json:"jellyfin_configured"`
	Queue              queueSummary  `json:"queue"`
	QueueNote          string        `json:"queue_note,omitempty"`
	Library            libraryCounts `json:"library"`
	// Music is Music's counts, new releases and imports; absent while the module is off.
	Music    *dashboardMusic `json:"music,omitempty"`
	Activity []activityEvent `json:"activity"`
	// Listening is who's listening to audiobooks right now. AudioOff: the audiobook
	// server isn't running, so there's nothing to show.
	Listening []nowListening `json:"listening"`
	AudioOff  bool           `json:"audio_off,omitempty"`
}

// nowListening is one live audiobook session. Arrmada's rule for listening is that a
// manager sees how much and when people listen, never what: the book (title, cover,
// place) is filled in only on the viewer's own sessions.
type nowListening struct {
	User      string  `json:"user"`
	Device    string  `json:"device"`
	Client    string  `json:"client"`
	StartedAt int64   `json:"started_at"`
	LastAt    int64   `json:"last_at"`
	Seconds   float64 `json:"seconds"`
	Playing   bool    `json:"playing"`
	Mine      bool    `json:"mine"`
	BookID    int64   `json:"book_id,omitempty"`
	Title     string  `json:"title,omitempty"`
	Author    string  `json:"author,omitempty"`
	CoverURL  string  `json:"cover_url,omitempty"`
	Position  float64 `json:"position,omitempty"`
	Duration  float64 `json:"duration,omitempty"`
}

// Apps report every few seconds to a minute while playing and stop when paused, so a
// session that reported in the last 90 s is playing; one quiet for up to 10 minutes is
// shown as paused, and after that it's no longer "now".
const (
	listenPlayingWindow = 90 * time.Second
	listenPausedWindow  = 10 * time.Minute
)

// storageVolume is one filesystem, not one folder. Several libraries usually live on
// the same array, and five identical bars say nothing five times over — Roots lists
// every configured folder that resolved to this same filesystem.
type storageVolume struct {
	Roots []string `json:"roots"`
	Path  string   `json:"path"`
	diskspace.Usage
}

type queueSummary struct {
	Downloading int   `json:"downloading"`
	Stalled     int   `json:"stalled"` // stalled by the queue's rule (automation.QueueStall) — not counted in Downloading
	Seeding     int   `json:"seeding"`
	Paused      int   `json:"paused"`
	Errored     int   `json:"errored"`
	DownSpeed   int64 `json:"down_speed"`
	UpSpeed     int64 `json:"up_speed"`
}

// summarizeQueue counts the client's torrents for the Dashboard tile by phase. A stalled
// torrent used to count as downloading, so a queue of dead torrents looked busy. Stalled is
// the Activity queue's own rule (stalled, from queueStallJudge), so the tile and the
// queue's Stalled pill agree; a moment with no peer sending is still downloading.
//
// Phases that aren't a verdict on their own (queued, checking, moving, …) go by whether the
// torrent is finished: a queued seed is still a seed.
func summarizeQueue(items []download.Item, stalled func(download.Item) (time.Time, bool)) queueSummary {
	var q queueSummary
	for _, it := range items {
		q.DownSpeed += it.DownSpeed
		q.UpSpeed += it.UpSpeed
		if _, ok := stalled(it); ok {
			q.Stalled++
			continue
		}
		switch it.Phase() {
		case "error":
			q.Errored++
		case "paused":
			q.Paused++
		case "seeding":
			q.Seeding++
		case "downloading", "stalled", "metadata", "queued", "checking", "moving", "allocating":
			if it.Complete() {
				q.Seeding++
			} else {
				q.Downloading++
			}
		}
	}
	return q
}

type libraryCounts struct {
	Movies          int `json:"movies"`
	MoviesMissing   int `json:"movies_missing"`
	Series          int `json:"series"`
	Episodes        int `json:"episodes"`
	EpisodesMissing int `json:"episodes_missing"`
	Books           int `json:"books"`
	BooksMissing    int `json:"books_missing"`
}

// activityEvent is one line of "what Arrmada actually did", drawn from the per-media
// event tables the detail pages already write. They were only ever readable one title
// at a time; this is the same record, unified and newest-first.
type activityEvent struct {
	Kind   string `json:"kind"` // movie | series | book | music (the artist)
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Event  string `json:"event"`
	Detail string `json:"detail"`
	AtMS   int64  `json:"at_ms"`
}

// streamsNote says which servers' streams are missing from Now playing ("Jellyfin: …").
func streamsNote(errs map[string]string) string {
	var parts []string
	for _, k := range []string{insights.ServerPlex, insights.ServerJellyfin} {
		if e, ok := errs[k]; ok {
			parts = append(parts, mediaserver.Kind(k).Label()+": "+e)
		}
	}
	return strings.Join(parts, "; ")
}

func (a *api) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), dashboardTimeout)
	defer cancel()

	music := a.dashboardMusic(ctx)
	out := dashboardPayload{
		Storage:  a.storageVolumes(ctx),
		Library:  a.libraryCounts(ctx),
		Music:    music,
		Activity: a.recentActivity(ctx, music != nil),
	}

	if a.deps.Downloads != nil {
		if items, err := a.deps.Downloads.Queue(ctx); err != nil {
			out.QueueNote = err.Error()
		} else {
			out.Queue = summarizeQueue(items, a.queueStallJudge(ctx))
		}
	}

	if a.deps.Insights != nil {
		cfg := a.deps.Insights.Config(ctx)
		out.PlexConfigured = cfg.URL != "" && cfg.TokenSet
		out.JellyfinConfigured = a.deps.Insights.JellyfinConfigured(ctx)
		if out.PlexConfigured || out.JellyfinConfigured {
			// Both servers' streams, each with its server; one not answering is a note
			// beside the other's streams.
			if act, err := a.deps.Insights.Activity(ctx, insights.ServerAll); err != nil {
				// The settings can be cleared between the check and the call; that is
				// still "not connected", not a failure to report.
				if errors.Is(err, plex.ErrNotConfigured) {
					out.PlexConfigured, out.JellyfinConfigured = false, false
				} else {
					out.StreamsNote = err.Error()
				}
			} else {
				out.Streams = &act
				if len(act.Errors) > 0 {
					out.StreamsNote = streamsNote(act.Errors)
				}
			}
		}
	}

	out.Listening, out.AudioOff = a.nowListening(ctx, r)

	a.writeJSON(w, http.StatusOK, out)
}

// nowListening lists live audiobook sessions, the viewer's own with their book.
func (a *api) nowListening(ctx context.Context, r *http.Request) ([]nowListening, bool) {
	out := []nowListening{}
	if a.deps.AudioServer == nil {
		return out, true
	}
	if a.deps.AudioManager != nil {
		if running, _ := a.deps.AudioManager.Running(); !running {
			return out, true
		}
	}
	now := time.Now()
	store := a.deps.AudioServer.Listen()
	live, err := store.Live(ctx, now.Add(-listenPausedWindow))
	if err != nil || len(live) == 0 {
		return out, false
	}
	names := map[int64]string{}
	if users, err := a.deps.Auth.ListUsers(ctx); err == nil {
		for _, u := range users {
			names[u.ID] = u.Username
		}
	}
	var me int64
	if u, ok := userFrom(r); ok && u != nil {
		me = u.ID
	}
	for _, l := range live {
		n := nowListening{User: names[l.UserID], Device: l.Device, Client: l.Client, StartedAt: l.StartedAt,
			LastAt: l.LastAt, Seconds: l.Seconds, Playing: now.Sub(time.UnixMilli(l.LastAt)) <= listenPlayingWindow}
		if n.User == "" {
			n.User = "Someone"
		}
		if me != 0 && l.UserID == me {
			n.Mine = true
			if key, pos, ok := store.SessionItem(ctx, me, l.SessionID); ok {
				if info, ok := a.deps.AudioServer.Info(ctx, key); ok {
					n.BookID, n.Title, n.Author, n.CoverURL, n.Position = info.BookID, info.Title, info.Author, info.CoverURL, pos
					if p, ok, _ := store.Progress(ctx, me, key); ok {
						n.Duration = p.Duration
					}
				}
			}
		}
		out = append(out, n)
	}
	return out, false
}

// storageVolumes measures every configured root and folds the ones sharing a
// filesystem together. Two roots on one array report byte-identical totals, which is
// a cheaper and more portable identity than digging out a device id.
func (a *api) storageVolumes(ctx context.Context) []storageVolume {
	// The folders in use now (saved in the app, else the environment's) — not the
	// environment's alone, which named the wrong disks after a folder was changed in the
	// app — plus any extra Movies and TV folders.
	rs := a.roots()
	c := rs.Config(ctx)
	type root struct{ label, path string }
	roots := []root{
		{"Movies", c.MoviesDir},
		{"TV", c.TVDir},
	}
	for _, e := range rs.Extras(ctx) {
		lib := "Movies"
		if e.Media == libroots.MediaSeries {
			lib = "TV"
		}
		roots = append(roots, root{lib + ": " + e.Label, e.Path})
	}
	roots = append(roots,
		root{"Ebooks", c.EbooksDir},
		root{"Audiobooks", c.AudiobooksDir},
		root{"Music", c.MusicDir},
		root{"Downloads", c.DownloadsDir},
	)
	// Library folders' own downloads folders, on their disks.
	for _, d := range health.DownloadFolders(c.DownloadsDir, rs.PerFolderDownloads(ctx)) {
		roots = append(roots, root{d.Label, d.Path})
	}
	roots = append(roots, root{"App data", c.DataDir})

	var measured []measuredRoot
	seenPath := map[string]bool{}
	for _, rt := range roots {
		if strings.TrimSpace(rt.path) == "" {
			continue
		}
		abs, err := filepath.Abs(rt.path)
		if err != nil {
			abs = rt.path
		}
		if seenPath[abs] {
			continue
		}
		seenPath[abs] = true
		u, ok := diskspace.Of(abs)
		if !ok {
			continue // unmounted, or a platform that can't measure — say nothing rather than zero
		}
		dev, devOK := diskspace.Device(abs)
		measured = append(measured, measuredRoot{label: rt.label, path: abs, usage: u, dev: dev, devOK: devOK})
	}
	return foldVolumes(measured)
}

// measuredRoot is one library folder's disk reading and, where the platform can say, the
// id of the filesystem it's on.
type measuredRoot struct {
	label, path string
	usage       diskspace.Usage
	dev         uint64
	devOK       bool
}

// foldVolumes makes one volume per filesystem, fullest first. Folders share a filesystem
// when they share a device id (st_dev). Without one, byte-identical totals stand in —
// but free space moves between two readings while a download writes, so totals alone
// used to show one array twice, and the widget counted its space twice.
func foldVolumes(roots []measuredRoot) []storageVolume {
	var out []storageVolume
	byDev := map[uint64]int{}
	bySize := map[[2]uint64]int{}
	for _, m := range roots {
		size := [2]uint64{m.usage.TotalBytes, m.usage.FreeBytes}
		i, dup := bySize[size]
		if m.devOK {
			i, dup = byDev[m.dev]
		}
		if dup {
			out[i].Roots = append(out[i].Roots, m.label)
			continue
		}
		if m.devOK {
			byDev[m.dev] = len(out)
		} else {
			bySize[size] = len(out)
		}
		out = append(out, storageVolume{Roots: []string{m.label}, Path: m.path, Usage: m.usage})
	}
	// Fullest first: the one about to cause a problem is the one worth seeing.
	sort.SliceStable(out, func(i, j int) bool { return out[i].UsedPct > out[j].UsedPct })
	return out
}

func (a *api) libraryCounts(ctx context.Context) libraryCounts {
	var lc libraryCounts
	if a.deps.Store == nil {
		return lc
	}
	db := a.deps.Store.DB()
	count := func(dst *int, query string) {
		var n sql.NullInt64
		if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			// A module whose tables aren't there yet reports zero, not an error page.
			a.deps.Log.Debug("dashboard: count failed", "query", query, "err", err)
			return
		}
		*dst = int(n.Int64)
	}
	count(&lc.Movies, `SELECT COUNT(*) FROM movies`)
	count(&lc.MoviesMissing, `SELECT COUNT(*) FROM movies WHERE monitored = 1 AND has_file = 0`)
	count(&lc.Series, `SELECT COUNT(*) FROM series`)
	count(&lc.Episodes, `SELECT COUNT(*) FROM episodes`)
	// What the searcher is actually after: aired, file-less episodes that are monitored, in
	// a monitored season of a show that isn't paused. Specials stay out, as in the series
	// list's counts. Counting every monitored row included paused shows and episodes that
	// haven't aired yet.
	count(&lc.EpisodesMissing, `SELECT COUNT(*) FROM episodes e
		JOIN series s ON s.id = e.series_id
		JOIN seasons sn ON sn.series_id = e.series_id AND sn.season_number = e.season_number
		WHERE s.monitored = 1 AND sn.monitored = 1 AND e.monitored = 1 AND e.has_file = 0
		AND e.season_number > 0 AND e.air_date <> '' AND `+store.DateText(a.deps.Store.DB(), "e.air_date")+` <= `+store.UTCDateText(a.deps.Store.DB()))
	count(&lc.Books, `SELECT COUNT(*) FROM books`)
	count(&lc.BooksMissing, `SELECT COUNT(*) FROM books WHERE monitored = 1 AND has_file = 0`)
	// Music's counts are dashboardMusic's, sent only while the module is on.
	return lc
}

// activityLimit is what fits on the page without turning the dashboard into the Logs
// tab. The full history is still on each title's detail page.
const activityLimit = 20

func (a *api) recentActivity(ctx context.Context, withMusic bool) []activityEvent {
	out := make([]activityEvent, 0, activityLimit)
	if a.deps.Store == nil {
		return out
	}
	// The newest slice of History's feed: every event, music's only while Music is on.
	f := eventsFilter{Limit: activityLimit}
	if !withMusic {
		f.Kinds = []string{"movie", "series", "book"}
	}
	events, err := eventsFeed(ctx, a.deps.Store.DB(), f)
	if err != nil {
		a.deps.Log.Debug("dashboard: activity feed unavailable", "err", err)
		return out
	}
	for _, e := range events {
		out = append(out, activityEvent{Kind: e.Kind, ID: e.ItemID, Title: e.Title, Event: e.Event, Detail: e.Detail, AtMS: e.AtMS})
	}
	return out
}
