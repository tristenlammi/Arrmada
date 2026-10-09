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
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/plex"
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
	PlexConfigured bool            `json:"plex_configured"`
	Queue          queueSummary    `json:"queue"`
	QueueNote      string          `json:"queue_note,omitempty"`
	Library        libraryCounts   `json:"library"`
	Activity       []activityEvent `json:"activity"`
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
	Stalled     int   `json:"stalled"` // incomplete with no peer sending — not counted in Downloading
	Seeding     int   `json:"seeding"`
	Paused      int   `json:"paused"`
	Errored     int   `json:"errored"`
	DownSpeed   int64 `json:"down_speed"`
	UpSpeed     int64 `json:"up_speed"`
}

// summarizeQueue counts the client's torrents for the Dashboard tile by phase. A stalled
// torrent used to count as downloading, so a queue of dead torrents looked busy.
//
// Phases that aren't a verdict on their own (queued, checking, moving, …) go by whether the
// torrent is finished: a queued seed is still a seed.
func summarizeQueue(items []download.Item) queueSummary {
	var q queueSummary
	for _, it := range items {
		q.DownSpeed += it.DownSpeed
		q.UpSpeed += it.UpSpeed
		switch it.Phase() {
		case "error":
			q.Errored++
		case "paused":
			q.Paused++
		case "stalled":
			q.Stalled++
		case "seeding":
			q.Seeding++
		case "downloading", "metadata", "queued", "checking", "moving", "allocating":
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
	Artists         int `json:"artists"`
	Albums          int `json:"albums"`
}

// activityEvent is one line of "what Arrmada actually did", drawn from the per-media
// event tables the detail pages already write. They were only ever readable one title
// at a time; this is the same record, unified and newest-first.
type activityEvent struct {
	Kind   string `json:"kind"` // movie | series | book
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Event  string `json:"event"`
	Detail string `json:"detail"`
	AtMS   int64  `json:"at_ms"`
}

func (a *api) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), dashboardTimeout)
	defer cancel()

	out := dashboardPayload{
		Storage:  a.storageVolumes(),
		Library:  a.libraryCounts(ctx),
		Activity: a.recentActivity(ctx),
	}

	if a.deps.Downloads != nil {
		if items, err := a.deps.Downloads.Queue(ctx); err != nil {
			out.QueueNote = err.Error()
		} else {
			out.Queue = summarizeQueue(items)
		}
	}

	if a.deps.Insights != nil {
		cfg := a.deps.Insights.Config(ctx)
		out.PlexConfigured = cfg.URL != "" && cfg.TokenSet
		if out.PlexConfigured {
			if act, err := a.deps.Insights.Activity(ctx); err != nil {
				// The settings can be cleared between the check and the call; that is
				// still "not connected", not a failure to report.
				if errors.Is(err, plex.ErrNotConfigured) {
					out.PlexConfigured = false
				} else {
					out.StreamsNote = err.Error()
				}
			} else {
				out.Streams = &act
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
func (a *api) storageVolumes() []storageVolume {
	c := a.deps.Config
	type root struct{ label, path string }
	roots := []root{
		{"Movies", c.MoviesDir},
		{"TV", c.TVDir},
		{"Ebooks", c.EbooksDir},
		{"Audiobooks", c.AudiobooksDir},
		{"Music", c.MusicDir},
		{"Downloads", c.DownloadsDir},
		{"App data", c.DataDir},
	}

	var out []storageVolume
	index := map[[2]uint64]int{}
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
		key := [2]uint64{u.TotalBytes, u.FreeBytes}
		if i, dup := index[key]; dup {
			out[i].Roots = append(out[i].Roots, rt.label)
			continue
		}
		index[key] = len(out)
		out = append(out, storageVolume{Roots: []string{rt.label}, Path: abs, Usage: u})
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
	count(&lc.EpisodesMissing, `SELECT COUNT(*) FROM episodes WHERE monitored = 1 AND has_file = 0`)
	count(&lc.Books, `SELECT COUNT(*) FROM books`)
	count(&lc.BooksMissing, `SELECT COUNT(*) FROM books WHERE monitored = 1 AND has_file = 0`)
	count(&lc.Artists, `SELECT COUNT(*) FROM artists`)
	count(&lc.Albums, `SELECT COUNT(*) FROM albums`)
	return lc
}

// activityLimit is what fits on the page without turning the dashboard into the Logs
// tab. The full history is still on each title's detail page.
const activityLimit = 20

func (a *api) recentActivity(ctx context.Context) []activityEvent {
	out := make([]activityEvent, 0, activityLimit)
	if a.deps.Store == nil {
		return out
	}
	const q = `
SELECT kind, id, title, event, detail, created_at FROM (
    SELECT 'movie'  AS kind, m.id AS id, m.title AS title, e.event, e.detail, e.created_at
      FROM movie_events e JOIN movies m ON m.id = e.movie_id
    UNION ALL
    SELECT 'series' AS kind, s.id, s.title, e.event, e.detail, e.created_at
      FROM series_events e JOIN series s ON s.id = e.series_id
    UNION ALL
    SELECT 'book'   AS kind, b.id, b.title, e.event, e.detail, e.created_at
      FROM book_events e JOIN books b ON b.id = e.book_id
)
ORDER BY created_at DESC LIMIT ?`
	rows, err := a.deps.Store.DB().QueryContext(ctx, q, activityLimit)
	if err != nil {
		a.deps.Log.Debug("dashboard: activity feed unavailable", "err", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var ev activityEvent
		var at time.Time
		if err := rows.Scan(&ev.Kind, &ev.ID, &ev.Title, &ev.Event, &ev.Detail, &at); err != nil {
			continue
		}
		ev.AtMS = at.UnixMilli()
		out = append(out, ev)
	}
	return out
}
