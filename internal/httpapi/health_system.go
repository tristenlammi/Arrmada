package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/health"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/metadata"
)

// refreshGap is how often "Check now" may re-run every check; a refresh asked for sooner
// is answered from the results already held.
const refreshGap = 10 * time.Second

// handleSystemHealth reports operational health — whether the pieces needed to acquire
// and import media are present and working — plus free disk space. It's the "why isn't
// anything downloading?" panel. Every answer comes from the health registry's cache: the
// checks run in the background, so a hung qBittorrent or Plex can't slow this down.
// ?refresh=1 re-runs them first (at most once per refreshGap).
func (a *api) handleSystemHealth(w http.ResponseWriter, r *http.Request) {
	rep := health.Report{Status: "ok", Warnings: []health.Warning{}, Checks: []health.CheckResult{}}
	if reg := a.deps.Health; reg != nil {
		if r.URL.Query().Get("refresh") == "1" {
			// Detached from the request: a closed tab mustn't cut the checks short and
			// leave them half-recorded.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Minute)
			reg.Refresh(ctx, refreshGap)
			cancel()
		}
		rep = reg.Results()
	}
	// Free space is a statfs on one folder — cheap and local, so it's read live.
	var disk map[string]any
	if dl := a.roots().Downloads(r.Context()); dl != "" {
		if free, ok := diskspace.FreeGB(dl); ok {
			disk = map[string]any{"free_gb": fmt.Sprintf("%.1f", free), "path": dl}
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"status": rep.Status, "warnings": rep.Warnings, "checks": rep.Checks, "disk": disk,
	})
}

// registerHealthChecks adds the checks built from the API's own dependencies. Each reads
// what it needs when it runs, so a setting changed since boot (a folder picked, a module
// switched off) is what's judged.
func (a *api) registerHealthChecks(reg *health.Registry) {
	if a.deps.Indexers != nil {
		reg.Register(health.IndexersCheck(func(ctx context.Context) (int, error) {
			ix, err := a.deps.Indexers.List(ctx)
			n := 0
			for _, i := range ix {
				// A usenet indexer with no usenet client is never searched, so it
				// doesn't count as somewhere to search.
				if a.deps.Indexers.Searched(i) {
					n++
				}
			}
			return n, err
		}))
		// Indexers backing off after repeated failures, from the integration status
		// tracker (the same state as the dots on the Indexers page).
		reg.Register(health.IndexerStatusCheck(func(ctx context.Context) (int, []health.PausedIndexer, error) {
			ix, err := a.deps.Indexers.List(ctx)
			if err != nil {
				return 0, nil, err
			}
			now := time.Now()
			enabled := 0
			var paused []health.PausedIndexer
			for _, i := range ix {
				if !a.deps.Indexers.Searched(i) {
					continue
				}
				enabled++
				st, _, ok := a.deps.Indexers.Status(i.ID)
				if ok && now.Before(st.BackoffUntil) {
					paused = append(paused, health.PausedIndexer{ID: i.ID, Name: i.Name, Failures: st.ConsecutiveFailures, Until: st.BackoffUntil, LastError: st.LastError})
				}
			}
			return enabled, paused, nil
		}))
	}

	if a.deps.Downloads != nil {
		reg.Register(health.DownloadClientsCheck(func(ctx context.Context) (int, []download.ClientState, error) {
			all, err := a.deps.Downloads.List(ctx)
			if err != nil {
				return 0, nil, err
			}
			// The shared queue read's per-client health, not a poll of its own: the
			// Downloads page, the sweeps and this check all see the same answer.
			snap, err := a.deps.Downloads.Snapshot(ctx)
			if err != nil && len(snap.Health) == 0 {
				return 0, nil, err // the client list itself couldn't be read
			}
			return len(all), download.EnabledStates(snap.Health), nil
		}))
	}

	if a.deps.Insights != nil {
		reg.Register(health.PlexCheck(
			func(ctx context.Context) health.PlexState {
				h := a.deps.Insights.PollHealth(ctx)
				return health.PlexState{
					Configured: h.Configured, Monitoring: h.Monitoring, LastOKAt: h.LastOKAt,
					FailingSince: h.FailingSince, LastErr: h.LastErr, Unauthorized: h.Unauthorized, Consecutive: h.Consecutive,
				}
			},
			func(ctx context.Context) error { return a.deps.Insights.ProbeIdentity(ctx) }))
	}

	if v, ok := a.deps.Discovery.(tmdbValidator); ok && a.deps.APIKeys != nil {
		reg.Register(health.TMDBCheck(a.deps.APIKeys.Func("tmdb"), func(ctx context.Context) error {
			return tmdbValidate(ctx, v)
		}))
	}

	// The probe cache keeps the check from writing a probe file into every library folder
	// each time it runs — on Unraid that can wake sleeping array disks. A folder that
	// passed is trusted for an hour; one that failed is re-checked each run.
	if a.deps.Settings != nil {
		reg.Register(health.LibraryFoldersCheck(a.libraryState, health.NewProbeCache(time.Hour)))
	}

	if a.deps.Recycle != nil {
		reg.Register(health.RecycleDriveCheck(func() []health.RecycleBinProblem {
			var out []health.RecycleBinProblem
			for _, p := range a.deps.Recycle.Problems() {
				out = append(out, health.RecycleBinProblem{Dir: p.Dir, Legacy: p.Legacy, OtherDrive: p.OtherDrive, LegacyFull: p.LegacyFull})
			}
			return out
		}))
	}

	if a.deps.DiskGuard != nil {
		reg.Register(health.DiskGuardCheck(a.diskGuardState))
	}

	// The Downloads folder is read live: one picked since boot is the one filling up.
	reg.Register(health.DiskFreeCheck(func() string { return a.roots().Downloads(a.runCtx()) }))

	if a.deps.AudioManager != nil && a.deps.Settings != nil {
		reg.Register(health.AudiobookServerCheck(func(ctx context.Context) (bool, bool, string) {
			if !a.deps.Settings.GetBool(ctx, audioserver.KeyEnabled, false) {
				return false, false, ""
			}
			running, lastErr := a.deps.AudioManager.Running()
			return true, running, lastErr
		}))
	}

	if a.deps.Backups != nil {
		reg.Register(health.BackupsCheck(func(ctx context.Context) string {
			return a.deps.Backups.HealthWarning(ctx, time.Since(a.start))
		}))
	}
}

// recheckHealth re-runs the named checks in the background after something they judge was
// just changed (a key saved, a folder picked), so the panel catches up now rather than at
// the check's next turn.
func (a *api) recheckHealth(r *http.Request, keys ...string) {
	if a.deps.Health == nil {
		return
	}
	_, _, _ = a.submit(r, jobs.Spec{Kind: "health.check", Target: strings.Join(keys, ","), Timeout: time.Minute,
		Fn: errFn(func(ctx context.Context) error {
			for _, k := range keys {
				a.deps.Health.RunNow(ctx, k)
			}
			return nil
		})})
}

// tmdbValidator is the TMDB provider's key check (metadata.TMDB.Validate).
type tmdbValidator interface {
	Validate(ctx context.Context) error
}

// tmdbValidate maps TMDB's answer onto the health check's terms.
func tmdbValidate(ctx context.Context, v tmdbValidator) error {
	err := v.Validate(ctx)
	switch {
	case errors.Is(err, metadata.ErrInvalidKey):
		return health.ErrKeyRejected
	case errors.Is(err, metadata.ErrNotConfigured):
		return health.ErrKeyMissing
	}
	return err
}

// libraryState is the folders the user picked (saved in the app, not just the ones the
// app started with), for the library-folders check.
func (a *api) libraryState(ctx context.Context) health.LibraryState {
	picked := a.pickedConfig(ctx)
	return health.LibraryState{
		Folders: health.LibraryFolders(picked, a.booksEnabled(ctx), a.musicEnabled(ctx)),
		All:     health.LibraryFolders(picked, true, true),
		DataDir: a.deps.Config.DataDir,
	}
}

// diskGuardState is the disk guard's view for the health check. While it's holding
// torrents the queue is read once, to count only the held ones the client still has.
func (a *api) diskGuardState(ctx context.Context) (health.DiskGuardState, bool) {
	g := a.deps.DiskGuard.Status(ctx)
	st := health.DiskGuardState{Enabled: g.Enabled, UsedPct: g.UsedPct, PausePct: g.PausePct, ResumePct: g.ResumePct, Holding: g.Holding}
	if g.Enabled && g.Holding > 0 && a.deps.Downloads != nil {
		if queue, err := a.deps.Downloads.Queue(ctx); err == nil {
			st.Holding = heldInQueue(a.deps.DiskGuard.Held(ctx), queue)
		}
	}
	return st, true
}

// heldInQueue counts the guard's held torrents that the client still has.
func heldInQueue(held map[string]bool, queue []download.Item) int {
	n := 0
	for _, it := range queue {
		if held[strings.ToLower(it.Hash)] {
			n++
		}
	}
	return n
}
