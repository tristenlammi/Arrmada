package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// handleDownloadsFeed returns the live download queue, each torrent with the library
// item it was grabbed for and its resolved quality profile. (Served at /downloads, not
// /activity — the latter is blocked by common ad-blocker filter lists.) The titles still
// being searched for are the Wanted view's (GET /api/v1/wanted): it reads the search
// history too, which this three-second poll shouldn't.
func (a *api) handleDownloadsFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// One shared read of the clients. When it failed or missed a client, what's
	// downloading isn't known, and the page must say so rather than show an empty queue.
	snap, _, qerr := a.queueSnapshot(ctx)
	queue := snap.Items

	// The acquisition record, joined to the queue by info hash: which library item each
	// torrent was grabbed for and under which profile, whatever the torrent is called.
	// Matching torrent names against titles mislabelled prettified names and called
	// Arrmada's own downloads "Not managed by Arrmada".
	var live map[string]automation.Acquisition
	var legacy []automation.Acquisition
	if a.deps.Automation != nil {
		live, legacy, _ = a.deps.Automation.LiveByHash(ctx)
	}
	// Resolve each quality-profile reference to a name at most once per request.
	profileCache := map[string]string{}
	pname := func(ref string) string {
		if v, ok := profileCache[ref]; ok {
			return v
		}
		v := a.profileName(ctx, ref)
		profileCache[ref] = v
		return v
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
	// How each in-flight grab stands against its stall window, so the page can say how
	// long a download has made no progress and when another release will be tried.
	var stallInfo map[string]automation.StallState
	if a.deps.Automation != nil {
		seedPolicies = a.deps.Automation.SeedPolicies(ctx)
		stallInfo = a.deps.Automation.StallInfo(ctx)
	}

	// What the disk guard is holding, so a guard pause reads differently from one the
	// user made — and the page doesn't offer a Resume that the server will refuse.
	guardHeld, guardSt := a.guardHolding(ctx)
	heldCount := 0

	downloads := make([]map[string]any, 0, len(queue))
	var totalDown, totalUp int64
	active, stalled := 0, 0
	for _, it := range queue {
		// By hash; a legacy grab recorded without one is the only thing still paired by
		// name. A torrent no grab knows keeps its category's media type and no profile.
		acq, managed := live[strings.ToLower(it.Hash)]
		if !managed {
			acq, managed = automation.LegacyMatchByName(legacy, it)
		}
		profile := "n/a"
		mediaType := queueMediaType(it.Category)
		if managed {
			mediaType = acq.MediaType
			if acq.Profile != "" {
				profile = pname(acq.Profile)
			}
		}
		totalDown += it.DownSpeed
		totalUp += it.UpSpeed
		if it.State == "downloading" {
			active++
		}
		phase := it.Phase()
		if phase == "stalled" {
			stalled++
		}
		entry := map[string]any{
			"hash":        it.Hash,
			"name":        it.Name,
			"state":       it.State,
			"progress":    it.Progress,
			"size_bytes":  it.SizeBytes,
			"down_speed":  it.DownSpeed,
			"up_speed":    it.UpSpeed,
			"eta_seconds": it.ETASeconds,
			// The client's own state and the finer phase read from it, with the swarm
			// numbers: "downloading" alone can't tell a dead torrent from a live one.
			"raw_state":     it.RawState,
			"phase":         phase,
			"seeds":         it.Seeds,
			"peers":         it.Peers,
			"swarm_seeds":   it.SwarmSeeds,
			"last_activity": it.LastActivity,
			"added_on":      it.AddedOn,
			// The computed ratio, not the client's field: qBittorrent reports an unbounded
			// ratio as a 9999 sentinel, so the page would show that while the seed-goal
			// logic (which now works from the byte counters) sees the real figure.
			"ratio":           ratioOrZero(it.SeedRatio()),
			"uploaded_bytes":  it.UploadedBytes,
			"seeding_time":    it.SeedingTime,
			"quality_profile": profile,
			"media_type":      mediaType,
			"imported":        imported[it.Hash],
		}
		if st, ok := stallInfo[strings.ToLower(it.Hash)]; ok && !it.Complete() {
			entry["stall"] = st
		}
		if it.State == "paused" && guardHeld[strings.ToLower(it.Hash)] {
			entry["held_by_guard"] = true
			heldCount++
		}
		// By info hash: the indexer's listing title is often a prettified rendering of the
		// actual torrent, so matching on the name missed entire trackers and labelled
		// genuinely-managed torrents "Not managed by Arrmada". Only a legacy grab without a
		// hash is still keyed by its release name.
		p, ok := seedPolicies[strings.ToLower(it.Hash)]
		if !ok {
			p, ok = seedPolicies[automation.NormReleaseKey(it.Name)]
		}
		if ok {
			entry["seed_enabled"] = p.Enabled
			entry["seed_ratio"] = p.Ratio
			entry["seed_hours"] = p.Hours
			entry["seed_known"] = true
		}
		downloads = append(downloads, entry)
	}

	out := map[string]any{
		"downloads": downloads,
		"totals":    map[string]any{"down_speed": totalDown, "up_speed": totalUp, "active": active, "stalled": stalled},
	}
	// Only a real reading: a folder that can't be measured used to show as "free 0 GB",
	// which reads as a full disk. null says "no figure".
	dlDir := a.roots().Downloads(ctx)
	out["free_gb"], out["disk_path"] = nil, dlDir
	if freeGB, ok := freeGBField(dlDir); ok {
		out["free_gb"] = freeGB
	}
	// Whether there is a download client at all and whether it answered, so the page can
	// say "no download client yet" or "qBittorrent unreachable since 14:02" instead of
	// looking like an empty queue. Left out when the list can't be read rather than
	// guessing.
	if clients, err := a.deps.Downloads.List(ctx); err == nil {
		out["clients"] = clientsStateOf(clients, snap, qerr)
	}
	if heldCount > 0 {
		out["disk_guard"] = map[string]any{
			"holding": heldCount, "used_pct": guardSt.UsedPct,
			"pause_pct": guardSt.PausePct, "resume_pct": guardSt.ResumePct,
		}
	}
	a.writeJSON(w, http.StatusOK, out)
}

// queueMediaType is what a queued torrent is by its download category — each kind is
// grabbed into its own (download.Categories). It labels a torrent no grab knows; one
// Arrmada grabbed takes its media type and profile from the acquisition record.
func queueMediaType(category string) string {
	switch category {
	case download.CategoryTV:
		return "series"
	case download.CategoryBooks:
		return "book"
	case download.CategoryMusic:
		return "music"
	}
	return "movie"
}

// profileName resolves a profile reference to a friendly name.
func (a *api) profileName(ctx context.Context, ref string) string {
	if sp, err := a.deps.Quality.GetStored(ctx, ref); err == nil && sp.Name != "" {
		return sp.Name
	}
	return ref
}

// movieDownload returns the in-progress download for a movie that doesn't yet have a file,
// through its acquisitions: the torrent each was grabbed as, found by info hash, so a
// torrent named nothing like the film still shows its progress. Only actively-downloading
// items (progress < 100%) are reported — a completed torrent is left to the import
// pipeline, so the UI never shows a stuck "importing 100%" for a seed that isn't really
// being imported.
func movieDownload(m movies.Movie, acqs []automation.Acquisition, byHash map[string]download.Item, queue []download.Item) *movies.DownloadStatus {
	if m.HasFile {
		return nil
	}
	if it, ok := inProgress(acqs, byHash, queue); ok {
		return &movies.DownloadStatus{State: it.State, Progress: it.Progress}
	}
	return nil
}

// inProgress is the first of acqs whose torrent is in the queue and not finished.
func inProgress(acqs []automation.Acquisition, byHash map[string]download.Item, queue []download.Item) (download.Item, bool) {
	for _, a := range acqs {
		if it, ok := automation.QueueItemFor(a, byHash, queue); ok && it.Progress < 1 {
			return it, true
		}
	}
	return download.Item{}, false
}

// queueByHash indexes a queue read by lowercased info hash.
func queueByHash(queue []download.Item) map[string]download.Item {
	out := make(map[string]download.Item, len(queue))
	for _, it := range queue {
		out[strings.ToLower(it.Hash)] = it
	}
	return out
}

// ratioOrZero renders an unknowable ratio as 0 rather than the -1 sentinel, which would
// show up in the UI as a negative ratio.
func ratioOrZero(r float64) float64 {
	if r < 0 {
		return 0
	}
	return r
}

// freeGBField is the downloads feed's free-space figure and whether there is one to show.
func freeGBField(path string) (float64, bool) {
	if path == "" {
		return 0, false
	}
	return diskspace.FreeGB(path)
}
