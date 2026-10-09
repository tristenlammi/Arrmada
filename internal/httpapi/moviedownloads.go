package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// The movie download join: which film is downloading what, and how far along. It reads
// the acquisition record once (ActiveByItem: one indexed query) and the shared queue
// snapshot once, indexes the queue once by info hash and — for legacy grabs recorded
// without a hash — by release key, and then joins every film in one pass: O(movies +
// queue). The list used to parse the whole queue once per movie.

// queueIndex is one queue read, indexed for the join.
type queueIndex struct {
	byHash map[string]download.Item
	byName map[string]download.Item // legacy hashless grabs: by automation.NormReleaseKey
}

func newQueueIndex(queue []download.Item) queueIndex {
	qi := queueIndex{byHash: make(map[string]download.Item, len(queue)), byName: make(map[string]download.Item, len(queue))}
	for _, it := range queue {
		qi.byHash[strings.ToLower(it.Hash)] = it
		if k := automation.NormReleaseKey(it.Name); k != "" {
			if _, dup := qi.byName[k]; !dup {
				qi.byName[k] = it
			}
		}
	}
	return qi
}

// item is an acquisition's torrent in the queue: by its info hash, or for a legacy row
// without one by its release name (the same rule as automation.QueueItemFor).
func (qi queueIndex) item(a automation.Acquisition) (download.Item, bool) {
	if a.InfoHash != "" {
		it, ok := qi.byHash[strings.ToLower(a.InfoHash)]
		return it, ok
	}
	it, ok := qi.byName[automation.NormReleaseKey(a.Title)]
	return it, ok
}

// downloadKind is what a grab is for: an upgrade replaces a file the track has; otherwise
// it fills a track with no file — an extra version's, or the film's own.
func downloadKind(a automation.Acquisition) string {
	switch {
	case a.Replaces:
		return movies.DownloadUpgrade
	case a.VersionID > 0:
		return movies.DownloadVersion
	}
	return movies.DownloadMissing
}

// kindRank orders a film's downloads for its one indicator: the film's own missing file
// first, then an extra version, then an upgrade.
var kindRank = map[string]int{movies.DownloadMissing: 0, movies.DownloadVersion: 1, movies.DownloadUpgrade: 2}

// movieDownloadOf is a film's download indicator from its in-flight acquisitions: the most
// important one whose torrent is in the queue and not finished (a completed torrent is
// left to the import pipeline, so nothing shows a stuck "100%"). Held grabs are waiting
// in Review, not downloading. label names an extra version by id (called only for one).
func movieDownloadOf(acqs []automation.Acquisition, qi queueIndex, label func(versionID int64) string) *movies.DownloadStatus {
	var best *movies.DownloadStatus
	for _, a := range acqs {
		if automation.AcqHeld(a) {
			continue
		}
		it, ok := qi.item(a)
		if !ok || it.Progress >= 1 {
			continue
		}
		d := &movies.DownloadStatus{State: it.State, Progress: it.Progress, Kind: downloadKind(a)}
		if a.VersionID > 0 {
			d.VersionID = a.VersionID
			if label != nil {
				d.VersionLabel = label(a.VersionID)
			}
		}
		if best == nil || kindRank[d.Kind] < kindRank[best.Kind] {
			best = d
		}
	}
	return best
}

// versionLabeler names extra versions for the join, reading a film's tracks only when one
// of its downloads is for an extra version (rare), once per film.
func (a *api) versionLabeler(ctx context.Context, movieID int64) func(int64) string {
	var labels map[int64]string
	return func(vid int64) string {
		if labels == nil {
			labels = map[int64]string{}
			if vs, err := a.deps.Movies.VersionRows(ctx, movieID); err == nil {
				for _, v := range vs {
					labels[v.ID] = v.Label
				}
			}
		}
		return labels[vid]
	}
}

// movieDownloadRow is one film's download, as GET /api/v1/movies/downloads lists it.
type movieDownloadRow struct {
	MovieID int64 `json:"movie_id"`
	movies.DownloadStatus
}

// handleMovieDownloads is GET /api/v1/movies/downloads[?id=]: only the films downloading
// something right now, with progress, state and what it's for. The library grid and the
// movie page poll this small answer while downloads run, instead of the whole list or
// the whole movie.
func (a *api) handleMovieDownloads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var only int64
	if s := r.URL.Query().Get("id"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id <= 0 {
			a.writeError(w, http.StatusBadRequest, "invalid movie id")
			return
		}
		only = id
	}
	rows := []movieDownloadRow{}
	snap, queueKnown, _ := a.queueSnapshot(ctx)
	if a.deps.Automation != nil && len(snap.Items) > 0 {
		var byItem map[int64][]automation.Acquisition
		if only > 0 {
			if acqs, err := a.deps.Automation.Active(ctx, automation.AttemptMovie, only); err == nil {
				byItem = map[int64][]automation.Acquisition{only: acqs}
			}
		} else {
			byItem, _ = a.deps.Automation.ActiveByItem(ctx, automation.AttemptMovie)
		}
		qi := newQueueIndex(snap.Items)
		for id, acqs := range byItem {
			if d := movieDownloadOf(acqs, qi, a.versionLabeler(ctx, id)); d != nil {
				rows = append(rows, movieDownloadRow{MovieID: id, DownloadStatus: *d})
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].MovieID < rows[j].MovieID })
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"downloads": rows, "client_health": queueHealth{OK: queueKnown}})
}
