package requests

import (
	"context"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
)

// Request stages, in the order a request moves through them.
const (
	StagePending     = "pending"     // waiting for someone to approve it
	StageDeclined    = "declined"    // turned down
	StageSearching   = "searching"   // approved; nothing grabbed yet (or not out yet)
	StageQueued      = "queued"      // grabbed, the download hasn't started
	StageDownloading = "downloading" // bytes are arriving
	StagePaused      = "paused"      // its download is paused
	StageFailed      = "failed"      // its download errored
	StageImporting   = "importing"   // downloaded; being moved into the library
	StagePartial     = "partial"     // a series with some episodes ready
	StageAvailable   = "available"   // ready to watch / read / listen
)

// Tracking is where a request has got to, for the requester's progress view.
type Tracking struct {
	Stage      string  `json:"stage"`
	Progress   float64 `json:"progress,omitempty"`    // 0..1 across its active downloads
	ETASeconds int64   `json:"eta_seconds,omitempty"` // longest remaining, when the client knows
	SpeedBps   int64   `json:"speed_bps,omitempty"`   // combined download speed
	SizeBytes  int64   `json:"size_bytes,omitempty"`  // combined size of its active downloads
	Downloads  int     `json:"downloads,omitempty"`   // how many downloads are in flight for it
	Have       int     `json:"have,omitempty"`        // series: episodes on disk
	Total      int     `json:"total,omitempty"`       // series: aired episodes wanted
	Note       string  `json:"note,omitempty"`        // a short explanation ("Not out yet")
}

// startingWindow is how long a grab with no download in the client yet still counts as
// "queued": the client can take a moment to pick up a new torrent. A grab older than that
// whose download has gone isn't in flight any more.
const startingWindow = 30 * time.Minute

type grabRow struct {
	hash      string
	title     string
	grabbedAt time.Time
}

// Track works out each request's stage from the library and the download client's queue.
// Downloads are found by the grabs recorded for the request's library item, matched to
// the queue by info hash — the torrent's real identity — falling back to the release name
// for grabs recorded before hashes were. Call after List (which fills the library links).
func (s *Service) Track(ctx context.Context, reqs []Request, queue []download.Item) {
	byHash := map[string]*download.Item{}
	byName := map[string]*download.Item{}
	for i := range queue {
		if h := strings.ToLower(queue[i].Hash); h != "" {
			byHash[h] = &queue[i]
		}
		byName[normName(queue[i].Name)] = &queue[i]
	}
	for i := range reqs {
		reqs[i].Tracking = s.track(ctx, &reqs[i], byHash, byName)
		// Keep the older field in step for anything still reading it.
		if t := reqs[i].Tracking; t != nil && t.Stage == StageDownloading {
			reqs[i].DownloadProgress = t.Progress
		}
	}
}

func (s *Service) track(ctx context.Context, rq *Request, byHash, byName map[string]*download.Item) *Tracking {
	t := &Tracking{}
	if rq.MediaType == "series" {
		t.Have, t.Total = rq.epHave, rq.epTotal
	}
	complete := rq.Available && (rq.MediaType != "series" || rq.epTotal == 0 || rq.epHave >= rq.epTotal)
	switch {
	case rq.Status == StatusDeclined && !rq.Available:
		t.Stage = StageDeclined
		return t
	case rq.Status == StatusPending && !rq.Available:
		t.Stage = StagePending
		return t
	}

	// What's in flight for it.
	var active, done, failed, paused int
	var size, got, speed, eta int64
	matched := 0
	if rq.libID > 0 {
		for _, g := range s.activeGrabs(ctx, rq.MediaType, rq.libID) {
			it := byHash[strings.ToLower(g.hash)]
			if it == nil && g.hash == "" {
				it = byName[normName(g.title)]
			}
			if it == nil {
				if time.Since(g.grabbedAt) < startingWindow {
					matched++
					active++ // just grabbed; the client hasn't shown it yet
				}
				continue
			}
			matched++
			switch {
			case it.Progress >= 1:
				done++
			case it.State == "error":
				failed++
			case it.State == "paused":
				paused++
				size, got = size+it.SizeBytes, got+it.DownloadedBytes
			default:
				active++
				size, got, speed = size+it.SizeBytes, got+it.DownloadedBytes, speed+it.DownSpeed
				if it.ETASeconds > eta && it.ETASeconds < 100*24*3600 { // qBittorrent's "infinite" is 8640000
					eta = it.ETASeconds
				}
			}
		}
	}

	switch {
	case complete:
		t.Stage = StageAvailable
		return t
	case active > 0:
		t.Stage = StageDownloading
		if size == 0 {
			t.Stage, t.Note = StageQueued, "Starting the download"
		} else if speed == 0 {
			t.Note = "Waiting for peers"
		}
	case paused > 0:
		t.Stage = StagePaused
	case done > 0:
		t.Stage = StageImporting
	case failed > 0:
		t.Stage, t.Note = StageFailed, "The download failed — it will be retried"
	case rq.Available:
		t.Stage = StagePartial // a series with some episodes, nothing more coming yet
		return t
	default:
		t.Stage = StageSearching
		if !rq.released {
			t.Note = "Not out yet"
		}
		return t
	}
	if size > 0 {
		t.Progress = float64(got) / float64(size)
	}
	t.SizeBytes, t.SpeedBps, t.ETASeconds, t.Downloads = size, speed, eta, matched
	return t
}

// activeGrabs is every grab for one library item that hasn't been closed out: still
// downloading, or finished and waiting in Review. Resolving the review closes the grab, so
// the requester stops seeing "Importing" then. movie_id holds the series or book id for
// those media types.
func (s *Service) activeGrabs(ctx context.Context, mediaType string, id int64) []grabRow {
	rows, err := s.repo.db.QueryContext(ctx,
		`SELECT info_hash, title, grabbed_at FROM grabs WHERE media_type = ? AND movie_id = ? AND `+automation.GrabInFlightWhere,
		mediaType, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []grabRow
	for rows.Next() {
		var g grabRow
		var at string
		if rows.Scan(&g.hash, &g.title, &at) == nil {
			g.grabbedAt = parseSQLiteTime(at)
			out = append(out, g)
		}
	}
	return out
}

// parseSQLiteTime reads a CURRENT_TIMESTAMP value; an unreadable one counts as long ago.
func parseSQLiteTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// normName folds a release name to letters and digits, for grabs recorded without a hash.
func normName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
