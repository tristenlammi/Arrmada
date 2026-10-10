package requests

import (
	"context"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/series"
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
	StageAdding      = "adding"      // on disk; waiting for Plex to show it before saying it's ready
	StageAvailable   = "available"   // ready to watch / read / listen
)

// addingToPlex reports whether a complete request is still waiting for Plex: approved, a
// movie or show, nobody told yet, and either seen waiting (on_disk_at) or Plex is set up
// so its 'ready' will wait. The card then says 'Adding to Plex…' rather than Ready until
// the notice goes out, so card and message agree.
func addingToPlex(rq Request, gated bool) bool {
	return rq.Status == StatusApproved && rq.ReadyAt == 0 && (rq.MediaType == "movie" || rq.MediaType == "series") &&
		(rq.onDiskAt > 0 || gated)
}

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
	// NextCheckAt is when a book that hasn't been found yet will be searched for again
	// (RFC3339, UTC). The page formats it in the viewer's own locale; the server never
	// writes a date into the copy.
	NextCheckAt string `json:"next_check_at,omitempty"`
	// While it's searching: when it was last checked (RFC3339, UTC — the later of the
	// sweep's own stamp and the newest stored search), how many searches in a row have
	// found nothing suitable, and for a book, whether the ladder has slowed to a monthly
	// check. Times and counts only: a requester never sees indexers, releases or reasons.
	LastSearchAt  string `json:"last_search_at,omitempty"`
	Misses        int    `json:"misses,omitempty"`
	SearchStopped bool   `json:"search_stopped,omitempty"`
}

// seriesComplete is the one rule for "this series request is done": something is on disk
// and every episode it counts is. have and total are the series' Stats roll-up — files on
// disk, against those plus the aired episodes still wanted (monitored, in a monitored
// season, specials left out) — so a show whose older seasons nobody monitors is complete
// once the seasons that are wanted are in. The request card (Track), the ready notice and
// the ready sweep all ask this, so the card says Ready exactly when the message goes out.
// A season-scoped request asks it of its own seasons' numbers.
func seriesComplete(have, total int) bool {
	return have > 0 && have >= total
}

// statsComplete is seriesComplete over a series' Stats roll-up.
func statsComplete(st *series.Stats) bool {
	return st != nil && seriesComplete(st.HaveFiles, st.Episodes)
}

// startingWindow is how long a grab with no download in the client yet still counts as
// "queued": the client can take a moment to pick up a new torrent. A grab older than that
// whose download has gone isn't in flight any more.
const startingWindow = 30 * time.Minute

// Track works out each request's stage from the library and the acquisition record: the
// grabs in flight for its library item, read once for the whole page, and their torrents
// found in the download queue by info hash — the torrent's real identity. Nothing is
// matched by name. queueKnown false says the queue couldn't be read; a download the
// record last saw running then still counts as in flight. Call after List (which fills
// the library links).
func (s *Service) Track(ctx context.Context, reqs []Request, queue []download.Item, queueKnown bool) {
	acqs := s.inFlightFor(ctx, reqs)
	byHash := make(map[string]*download.Item, len(queue))
	for i := range queue {
		if h := strings.ToLower(queue[i].Hash); h != "" {
			byHash[h] = &queue[i]
		}
	}
	now := time.Now()
	gated := s.plex != nil && s.plex.Configured(ctx)
	for i := range reqs {
		var mine []automation.Acquisition
		if reqs[i].libID > 0 {
			mine = acqs[reqs[i].MediaType][reqs[i].libID]
		}
		reqs[i].Tracking = track(&reqs[i], mine, byHash, queueKnown, now)
		if t := reqs[i].Tracking; t != nil && t.Stage == StageAvailable && addingToPlex(reqs[i], gated) {
			t.Stage = StageAdding
		}
		// Keep the older field in step for anything still reading it.
		if t := reqs[i].Tracking; t != nil && t.Stage == StageDownloading {
			reqs[i].DownloadProgress = t.Progress
		}
	}
	s.lastChecked(ctx, reqs)
}

// lastChecked fills in when each searching request was last looked for: the sweep's own
// stamp, or a stored search (a manual one, or a request's first) if that's newer — one
// query per media type for the lot.
func (s *Service) lastChecked(ctx context.Context, reqs []Request) {
	ids := map[string][]int64{}
	for i := range reqs {
		rq := &reqs[i]
		t := rq.Tracking
		if t == nil || t.Stage != StageSearching || !rq.released || rq.libID <= 0 {
			continue
		}
		t.Misses = rq.searchMisses
		t.LastSearchAt = rfc3339(parseSearchStamp(rq.lastSearchAt))
		// Slowed to monthly, but still on the ladder (an unmonitored book isn't searched
		// again at all, and says only "Not found yet").
		t.SearchStopped = rq.MediaType == "book" && rq.searchMisses > books.MonthlyAfter && rq.nextCheckAt != ""
		ids[rq.MediaType] = append(ids[rq.MediaType], rq.libID)
	}
	if s.coord == nil {
		return
	}
	for kind, list := range ids {
		times, err := s.coord.LastSearchedAt(ctx, kind, list)
		if err != nil {
			continue // the sweep's stamp stands
		}
		for i := range reqs {
			rq := &reqs[i]
			t := rq.Tracking
			if t == nil || t.Stage != StageSearching || rq.MediaType != kind {
				continue
			}
			ms, ok := times[rq.libID]
			if !ok || ms <= 0 {
				continue
			}
			at := time.UnixMilli(ms)
			if last := parseSearchStamp(rq.lastSearchAt); at.After(last) {
				t.LastSearchAt = rfc3339(at)
			}
		}
	}
}

// parseSearchStamp reads a last_search_at as stored: SQLite's datetime('now') for movies
// and series, RFC 3339 once the books repo has read it.
func parseSearchStamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	return parseSQLiteTime(s)
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// NeedsQueue reports whether any of reqs could have a download in flight, so a caller
// reads the download client only when it matters: approved, in the library and not told
// it's ready yet — or a series, which keeps getting new episodes. Call after List.
func NeedsQueue(reqs []Request) bool {
	for _, rq := range reqs {
		if rq.Status == StatusApproved && rq.libID > 0 && (rq.ReadyAt == 0 || rq.MediaType == "series") {
			return true
		}
	}
	return false
}

// tracked reports whether a request's stage can depend on downloads: it's in the library,
// and approved or already (partly) there.
func tracked(rq Request) bool {
	return rq.libID > 0 && (rq.Available || rq.Status == StatusApproved)
}

// inFlightFor reads the acquisition record once per media type on the page — never once
// per request. An unreadable record leaves the page without in-flight stages (searching,
// or ready when the file is there) rather than failing it.
func (s *Service) inFlightFor(ctx context.Context, reqs []Request) map[string]map[int64][]automation.Acquisition {
	out := map[string]map[int64][]automation.Acquisition{}
	if s.coord == nil {
		return out
	}
	for _, rq := range reqs {
		if !tracked(rq) {
			continue
		}
		if _, done := out[rq.MediaType]; done {
			continue
		}
		byItem, err := s.coord.ActiveByItem(ctx, rq.MediaType)
		if err != nil {
			s.log.Warn("requests: couldn't read what's downloading", "media", rq.MediaType, "err", err)
		}
		out[rq.MediaType] = byItem
	}
	return out
}

func track(rq *Request, acqs []automation.Acquisition, byHash map[string]*download.Item, queueKnown bool, now time.Time) *Tracking {
	t := &Tracking{}
	if rq.MediaType == "series" {
		t.Have, t.Total = rq.epHave, rq.epTotal
	}
	complete := rq.Available && (rq.MediaType != "series" || seriesComplete(rq.epHave, rq.epTotal))
	if rq.MediaType == "series" && len(rq.Seasons) > 0 {
		// A request for some seasons is complete over its own seasons, by the same rule
		// asked of each of them (seasonsProgress).
		complete = rq.Available && rq.seasonsDone
	}
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
	for _, a := range acqs {
		var it *download.Item
		if a.InfoHash != "" {
			it = byHash[strings.ToLower(a.InfoHash)]
		}
		if it == nil {
			switch {
			case automation.AcqHeld(a):
				// Finished and waiting in Review: it's here, being decided on.
				matched++
				done++
			case !queueKnown && automation.AcqFinished(a):
				// The queue can't be read; the record last saw it finished.
				matched++
				done++
			case !queueKnown && a.Phase != "" && !automation.AcqGone(a):
				// The queue can't be read; the record last saw it under way.
				matched++
				active++
			case now.Sub(parseSQLiteTime(a.GrabbedAt)) < startingWindow:
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
	case rq.partNote != "":
		// A book asked for in both formats with one of them here.
		t.Stage, t.Note = StagePartial, rq.partNote
		return t
	default:
		t.Stage = StageSearching
		switch {
		case !rq.released:
			t.Note = "Not out yet"
		case rq.MediaType == "book" && rq.searchMisses > 0:
			// Searched and not found, as opposed to just added: say so, and when the
			// next look is, instead of an open-ended "Searching".
			t.Note = "Not found yet"
			t.NextCheckAt = rq.nextCheckAt
		}
		return t
	}
	if size > 0 {
		t.Progress = float64(got) / float64(size)
	}
	t.SizeBytes, t.SpeedBps, t.ETASeconds, t.Downloads = size, speed, eta, matched
	return t
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
