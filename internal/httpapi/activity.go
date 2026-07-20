package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/tristenlammi/arrmada/internal/automation"
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

	// Monitored, missing movies split into two buckets: those actually being
	// searched (past their minimum-availability threshold — same gate the
	// automation uses) and those still awaiting release. An unreleased film must
	// not claim to be "Searching" — nothing is looking for it yet.
	searching := []map[string]any{}
	upcoming := []map[string]any{}
	for _, m := range list {
		if !m.Monitored || m.HasFile {
			continue
		}
		if movieInQueue(queue, m) {
			continue // a download for it is already in flight
		}
		entry := map[string]any{
			"movie_id":        m.ID,
			"title":           m.Title,
			"year":            m.Year,
			"poster_url":      m.PosterURL,
			"quality_profile": a.profileName(ctx, m.QualityProfile),
		}
		if a.deps.Movies.IsAvailable(m) {
			searching = append(searching, entry)
			continue
		}
		if m.Extra != nil && m.Extra.ReleaseDate != "" {
			entry["available_at"] = m.Extra.ReleaseDate
		}
		upcoming = append(upcoming, entry)
	}

	// Series contribute to the same two buckets, grouped per show: a series with aired,
	// monitored, missing episodes is "searching" (labeled with the count); its soonest
	// unaired monitored episode is "upcoming".
	if a.deps.Series != nil {
		for _, sa := range a.deps.Series.AcquisitionSummary(ctx) {
			if sa.SearchingCount > 0 {
				searching = append(searching, map[string]any{
					"series_id": sa.ID, "title": sa.Title, "year": sa.Year,
					"poster_url": sa.PosterURL, "quality_profile": a.profileName(ctx, sa.QualityProfile),
					"media_type": "series", "episode_count": sa.SearchingCount,
				})
			}
			if sa.NextAir != "" {
				upcoming = append(upcoming, map[string]any{
					"series_id": sa.ID, "title": sa.Title, "year": sa.Year,
					"poster_url": sa.PosterURL, "quality_profile": a.profileName(ctx, sa.QualityProfile),
					"media_type": "series", "available_at": sa.NextAir, "next_label": sa.NextLabel,
				})
			}
		}
	}

	// Imported hashes are flagged (not hidden) so the Seeding tab can show every seeding
	// torrent — the user wants to see each one's seed rule and progress, not just the
	// untracked ones.
	imported := map[string]bool{}
	if a.deps.Library != nil {
		if s, err := a.deps.Library.ImportedHashes(ctx); err == nil {
			imported = s
		}
	}

	// Seed goals recorded at grab time, so the Seeding tab can show each torrent's
	// target ratio / time and whether it's set to seed at all.
	var seedPolicies map[string]automation.SeedPolicy
	if a.deps.Automation != nil {
		seedPolicies = a.deps.Automation.SeedPolicies(ctx)
	}

	downloads := make([]map[string]any, 0, len(queue))
	var totalDown, totalUp int64
	var unmatched []string
	active := 0
	for _, it := range queue {
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
		entry := map[string]any{
			"hash":            it.Hash,
			"name":            it.Name,
			"state":           it.State,
			"progress":        it.Progress,
			"size_bytes":      it.SizeBytes,
			"down_speed":      it.DownSpeed,
			"up_speed":        it.UpSpeed,
			"eta_seconds":     it.ETASeconds,
			"ratio":           it.Ratio,
			"seeding_time":    it.SeedingTime,
			"quality_profile": profile,
			"media_type":      mediaType,
			"imported":        imported[it.Hash],
		}
		// Info hash first: the indexer's listing title is often a prettified rendering of
		// the actual torrent, so matching on the name alone missed entire trackers and
		// labelled genuinely-managed torrents "Not managed by Arrmada".
		p, ok := seedPolicies[strings.ToLower(it.Hash)]
		if !ok {
			p, ok = seedPolicies[automation.NormReleaseKey(it.Name)]
		}
		if ok {
			entry["seed_enabled"] = p.Enabled
			entry["seed_ratio"] = p.Ratio
			entry["seed_hours"] = p.Hours
			entry["seed_known"] = true
		} else {
			unmatched = append(unmatched, it.Name)
		}
		downloads = append(downloads, entry)
	}
	a.logUnmatchedSeeds(ctx, unmatched, seedPolicies)

	freeGB, _ := diskspace.FreeGB(a.deps.Config.DownloadsDir)
	a.writeJSON(w, http.StatusOK, map[string]any{
		"searching": searching,
		"upcoming":  upcoming,
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

// normKey folds accents and keeps alphanumerics, so "Pokémon" matches "Pokemon".
func normKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(parser.FoldAccents(s)) {
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

// logUnmatchedSeeds explains torrents the Seeding tab is labelling "Not managed by
// Arrmada — no seed rule".
//
// That message covers two very different situations and gives no way to tell them apart:
// the torrent genuinely wasn't grabbed by Arrmada (added by hand, or left over from
// another tool), or it WAS grabbed and its seed rule simply isn't being found — a stale
// grab row, or a torrent whose name in the client no longer normalizes to the release
// title we recorded. The second is a bug wearing the first one's label, and reading the
// UI can't distinguish them.
//
// So log the normalized key we looked up, how many rules we hold, and — decisively — the
// status of any grab row that DOES match the name across all statuses. rules_held proved
// the policy set is healthy, which rules out missing rows wholesale; grab_status then
// separates "never grabbed" ("") from a row parked in a status the seeding view skips
// ('seeded' after ManageSeeding removes a torrent, 'failed' after stall fail-over).
func (a *api) logUnmatchedSeeds(ctx context.Context, names []string, policies map[string]automation.SeedPolicy) {
	if len(names) == 0 || a.deps.Log == nil {
		return
	}
	// The Downloads page polls continuously; without a throttle this would bury the log
	// it's meant to help you read.
	if !a.seedDiagAt.CompareAndSwap(0, 1) {
		return
	}
	go func() {
		time.Sleep(10 * time.Minute)
		a.seedDiagAt.Store(0)
	}()

	sample := names
	if len(sample) > 5 {
		sample = sample[:5]
	}
	// Report the near misses, not an exact-match verdict. An exact lookup uses the very
	// comparison under suspicion, so "not found" can't distinguish "never grabbed" from
	// "grabbed, recorded under a title that no longer matches" — printing both strings is
	// the only way to see how they diverge.
	for _, n := range sample {
		key := automation.NormReleaseKey(n)
		a.deps.Log.Warn("seeding: no seed rule matched this torrent",
			"torrent", n, "lookup_key", key, "rules_held", len(policies))
		if a.deps.Automation == nil {
			continue
		}
		near := a.deps.Automation.NearestGrabs(ctx, n, 3)
		if len(near) == 0 {
			a.deps.Log.Warn("seeding:   no grab row even resembles it", "torrent", n)
			continue
		}
		for _, g := range near {
			a.deps.Log.Warn("seeding:   closest recorded grab",
				"recorded_title", g.Title, "recorded_key", g.Key, "status", g.Status,
				"shared_prefix", automation.SharedPrefixLen(g.Key, key))
		}
	}
	if len(names) > len(sample) {
		a.deps.Log.Warn("seeding: more torrents without a seed rule",
			"shown", len(sample), "total", len(names))
	}
}
