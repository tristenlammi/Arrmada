package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/insights"
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
	Queue       queueSummary       `json:"queue"`
	QueueNote   string             `json:"queue_note,omitempty"`
	Library     libraryCounts      `json:"library"`
	Activity    []activityEvent    `json:"activity"`
}

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
	Seeding     int   `json:"seeding"`
	Paused      int   `json:"paused"`
	Errored     int   `json:"errored"`
	DownSpeed   int64 `json:"down_speed"`
	UpSpeed     int64 `json:"up_speed"`
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
			for _, it := range items {
				out.Queue.DownSpeed += it.DownSpeed
				out.Queue.UpSpeed += it.UpSpeed
				switch {
				case strings.Contains(it.State, "error") || strings.Contains(it.State, "missingFiles"):
					out.Queue.Errored++
				case strings.Contains(strings.ToLower(it.State), "paus"):
					out.Queue.Paused++
				case strings.Contains(it.State, "download") || it.State == "stalledDL":
					out.Queue.Downloading++
				case strings.Contains(strings.ToLower(it.State), "seed") || strings.Contains(it.State, "UP"):
					out.Queue.Seeding++
				}
			}
		}
	}

	if a.deps.Insights != nil {
		if act, err := a.deps.Insights.Activity(ctx); err != nil {
			out.StreamsNote = err.Error()
		} else {
			out.Streams = &act
		}
	}

	a.writeJSON(w, http.StatusOK, out)
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
