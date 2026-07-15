package httpapi

import (
	"context"
	"net/http"
	"strings"
	"unicode"

	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/series"
)

// seriesDownloadCategory is the qBittorrent category TV downloads use (kept in sync
// with automation.seriesCategory) so the feed can label series torrents.
const seriesDownloadCategory = "arrmada-tv"
const bookDownloadCategory = "arrmada-books"

// handleDownloadsFeed returns the live acquisition feed: movies that are searching
// (monitored, missing, not yet downloading) plus the download queue — each with
// its resolved quality profile. (Served at /downloads, not /activity — the latter
// is blocked by common ad-blocker filter lists.)
func (a *api) handleDownloadsFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, _ := a.deps.Movies.List(ctx)
	queue, _ := a.deps.Downloads.Queue(ctx)

	searching := []map[string]any{}
	for _, m := range list {
		if !m.Monitored || m.HasFile {
			continue
		}
		if movieInQueue(queue, m) {
			continue // a download for it is already in flight
		}
		searching = append(searching, map[string]any{
			"movie_id":        m.ID,
			"title":           m.Title,
			"year":            m.Year,
			"poster_url":      m.PosterURL,
			"quality_profile": a.profileName(ctx, m.QualityProfile),
		})
	}

	// Downloads already imported into the library are dropped from the view (they
	// keep seeding in the client, but they're done as far as Arrmada is concerned).
	imported := map[string]bool{}
	if a.deps.Library != nil {
		if s, err := a.deps.Library.ImportedHashes(ctx); err == nil {
			imported = s
		}
	}

	downloads := make([]map[string]any, 0, len(queue))
	var totalDown, totalUp int64
	active := 0
	for _, it := range queue {
		if imported[it.Hash] {
			continue // imported — hide it, even though it's still seeding
		}
		profile := "n/a"
		mediaType := "movie"
		switch it.Category {
		case seriesDownloadCategory:
			mediaType = "series"
			if a.deps.Series != nil {
				if sr, ok := a.deps.Series.MatchByTitle(ctx, series.NormTitle(parser.Parse(it.Name).Title)); ok {
					profile = a.profileName(ctx, sr.QualityProfile)
				}
			}
		case bookDownloadCategory:
			mediaType = "book"
		default:
			if mv, ok := a.deps.Movies.MatchRelease(ctx, it.Name); ok {
				profile = a.profileName(ctx, mv.QualityProfile)
			}
		}
		totalDown += it.DownSpeed
		totalUp += it.UpSpeed
		if it.State == "downloading" {
			active++
		}
		downloads = append(downloads, map[string]any{
			"hash":            it.Hash,
			"name":            it.Name,
			"state":           it.State,
			"progress":        it.Progress,
			"size_bytes":      it.SizeBytes,
			"down_speed":      it.DownSpeed,
			"up_speed":        it.UpSpeed,
			"eta_seconds":     it.ETASeconds,
			"ratio":           it.Ratio,
			"quality_profile": profile,
			"media_type":      mediaType,
		})
	}

	freeGB, _ := diskspace.FreeGB(a.deps.Config.DownloadsDir)
	a.writeJSON(w, http.StatusOK, map[string]any{
		"searching": searching,
		"downloads": downloads,
		"totals":    map[string]any{"down_speed": totalDown, "up_speed": totalUp, "active": active},
		"free_gb":   freeGB,
	})
}

// profileName resolves a profile reference to a friendly name.
func (a *api) profileName(ctx context.Context, ref string) string {
	if sp, err := a.deps.Quality.GetStored(ctx, ref); err == nil && sp.Name != "" {
		return sp.Name
	}
	return ref
}

// downloadFor returns the in-progress download for a movie that doesn't yet have
// a file. Only actively-downloading items (progress < 100%) are reported — a
// completed torrent is left to the import pipeline, so the UI never shows a
// stuck "importing 100%" for a seed that isn't really being imported.
func downloadFor(queue []download.Item, m movies.Movie) *movies.DownloadStatus {
	if m.HasFile {
		return nil
	}
	want := normKey(m.Title)
	for i := range queue {
		it := queue[i]
		if it.Progress >= 1 {
			continue // finished — not "downloading"; import handles it
		}
		r := parser.Parse(it.Name)
		if normKey(r.Title) != want || (r.Year != 0 && m.Year != 0 && absInt(r.Year-m.Year) > 1) {
			continue
		}
		return &movies.DownloadStatus{State: it.State, Progress: it.Progress}
	}
	return nil
}

// movieInQueue reports whether a download in the queue matches the movie.
func movieInQueue(queue []download.Item, m movies.Movie) bool {
	want := normKey(m.Title)
	for _, it := range queue {
		r := parser.Parse(it.Name)
		if normKey(r.Title) == want && (r.Year == 0 || m.Year == 0 || absInt(r.Year-m.Year) <= 1) {
			return true
		}
	}
	return false
}

func normKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
