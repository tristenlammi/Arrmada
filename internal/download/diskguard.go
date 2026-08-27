package download

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"sync"

	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/settings"
)

// A full downloads volume is the worst kind of failure here: qBittorrent errors every
// torrent at once, imports fail, and on a shared cache pool it takes everything else
// on that pool down with it. The guard stops downloads before the disk gets there.
//
// It pauses only what it needs to and remembers exactly which torrents it paused, so
// resuming never touches one you paused by hand. That list is persisted, so a restart
// mid-hold doesn't strand your queue.
const (
	KeyDiskGuard      = "downloads_disk_guard"
	KeyDiskGuardPause = "downloads_disk_guard_pause_pct"
	KeyDiskGuardResum = "downloads_disk_guard_resume_pct"
	// keyDiskGuardHeld is internal bookkeeping, not a preference — the hashes the
	// guard paused and therefore owns.
	keyDiskGuardHeld = "downloads_disk_guard_held"
)

// Defaults. On by default: an unguarded full disk is a failure nobody opts into
// deliberately, and 85/80 leaves room to notice without pausing over normal churn.
const (
	DefaultDiskGuard      = true
	DefaultDiskGuardPause = 85
	DefaultDiskGuardResum = 80
)

// DiskGuard watches one volume and holds downloads while it's too full.
type DiskGuard struct {
	svc      *Service
	settings *settings.Service
	log      *slog.Logger
	dir      string // the volume to watch — where the download client writes

	// mu serialises Check against itself. The scheduler won't overlap runs, but a
	// manual trigger from the API can land alongside one, and two passes racing
	// would double-pause and lose track of what's held.
	mu sync.Mutex
}

// NewDiskGuard wires a guard over the volume at dir.
func NewDiskGuard(svc *Service, set *settings.Service, log *slog.Logger, dir string) *DiskGuard {
	return &DiskGuard{svc: svc, settings: set, log: log, dir: dir}
}

// GuardStatus is what the guard is currently doing, for the API and the health panel.
type GuardStatus struct {
	Enabled    bool    `json:"enabled"`
	Measurable bool    `json:"measurable"` // false = this platform/path can't be measured
	Path       string  `json:"path"`
	UsedPct    float64 `json:"used_pct"`
	PausePct   int     `json:"pause_pct"`
	ResumePct  int     `json:"resume_pct"`
	Holding    int     `json:"holding"` // torrents the guard has paused
}

// Status reports the guard's current view without changing anything.
func (g *DiskGuard) Status(ctx context.Context) GuardStatus {
	pause, resume := g.thresholds(ctx)
	st := GuardStatus{
		Enabled:   g.settings.GetBool(ctx, KeyDiskGuard, DefaultDiskGuard),
		Path:      g.dir,
		PausePct:  pause,
		ResumePct: resume,
		Holding:   len(g.held(ctx)),
	}
	if u, ok := diskspace.Of(g.dir); ok {
		st.Measurable, st.UsedPct = true, u.UsedPct
	}
	return st
}

// thresholds reads the pause/resume points, repairing anything nonsensical.
//
// A resume point at or above the pause point would pause and resume on alternate
// passes forever, hammering the client — so it is forced below rather than trusted.
func (g *DiskGuard) thresholds(ctx context.Context) (pause, resume int) {
	pause = g.intSetting(ctx, KeyDiskGuardPause, DefaultDiskGuardPause)
	resume = g.intSetting(ctx, KeyDiskGuardResum, DefaultDiskGuardResum)
	if pause < 1 {
		pause = 1
	}
	if pause > 99 {
		pause = 99
	}
	if resume >= pause {
		resume = pause - 1
	}
	if resume < 0 {
		resume = 0
	}
	return pause, resume
}

func (g *DiskGuard) intSetting(ctx context.Context, key string, def int) int {
	n, err := strconv.Atoi(g.settings.Get(ctx, key, ""))
	if err != nil {
		return def
	}
	return n
}

// Check runs one pass: pause if the volume is too full, resume once it has drained
// back below the resume point. Safe to call on a timer.
func (g *DiskGuard) Check(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	held := g.held(ctx)

	if !g.settings.GetBool(ctx, KeyDiskGuard, DefaultDiskGuard) {
		// Turning the guard off must not strand whatever it was holding.
		if len(held) > 0 {
			g.log.Info("disk guard disabled — releasing the downloads it was holding", "torrents", len(held))
			g.resume(ctx, held)
		}
		return nil
	}

	u, ok := diskspace.Of(g.dir)
	if !ok {
		// Can't measure: do nothing at all. Guessing here would either pause a
		// perfectly healthy queue or give false assurance.
		return nil
	}
	pause, resume := g.thresholds(ctx)

	switch {
	case u.UsedPct >= float64(pause):
		g.pauseActive(ctx, held, u.UsedPct, pause)
	case u.UsedPct <= float64(resume) && len(held) > 0:
		g.log.Info("disk guard: space recovered, resuming downloads",
			"used_pct", round1(u.UsedPct), "resume_at_pct", resume, "torrents", len(held), "path", g.dir)
		g.resume(ctx, held)
	}
	return nil
}

// pauseActive pauses every torrent still pulling data, and adds them to the held set.
//
// Seeding torrents are deliberately left alone: they aren't writing anything, and
// pausing them would stall seed goals and put the private trackers' hit-and-run
// clocks at risk for a problem they aren't causing.
func (g *DiskGuard) pauseActive(ctx context.Context, held []string, usedPct float64, pausePct int) {
	items, err := g.svc.Queue(ctx)
	if err != nil {
		g.log.Warn("disk guard: could not read the queue", "err", err)
		return
	}
	owned := map[string]bool{}
	for _, h := range held {
		owned[h] = true
	}

	var newly []string
	for _, it := range items {
		if it.State != "downloading" || owned[it.Hash] {
			continue
		}
		if err := g.svc.Pause(ctx, it.Hash); err != nil {
			g.log.Warn("disk guard: could not pause a download", "name", it.Name, "err", err)
			continue
		}
		newly = append(newly, it.Hash)
		owned[it.Hash] = true
	}
	if len(newly) == 0 {
		return
	}
	// Loud on purpose: "nothing is downloading" is otherwise a mystery, and this is
	// the first place anyone will look for the reason.
	g.log.Warn("disk guard: paused downloads — the volume is too full",
		"used_pct", round1(usedPct), "pause_at_pct", pausePct,
		"paused", len(newly), "path", g.dir)
	g.save(ctx, append(held, newly...))
}

// resume restarts the held torrents and clears the set. A hash that no longer exists
// (deleted while held) fails harmlessly and is dropped either way — keeping it would
// leave the guard holding a ghost forever.
func (g *DiskGuard) resume(ctx context.Context, held []string) {
	for _, h := range held {
		if err := g.svc.Resume(ctx, h); err != nil {
			g.log.Debug("disk guard: could not resume a download", "hash", h, "err", err)
		}
	}
	g.save(ctx, nil)
}

// held returns the hashes the guard has paused.
func (g *DiskGuard) held(ctx context.Context) []string {
	raw := g.settings.Get(ctx, keyDiskGuardHeld, "")
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func (g *DiskGuard) save(ctx context.Context, hashes []string) {
	if len(hashes) == 0 {
		if err := g.settings.Set(ctx, keyDiskGuardHeld, ""); err != nil {
			g.log.Warn("disk guard: could not clear the held set", "err", err)
		}
		return
	}
	sort.Strings(hashes)
	b, err := json.Marshal(hashes)
	if err != nil {
		return
	}
	if err := g.settings.Set(ctx, keyDiskGuardHeld, string(b)); err != nil {
		// Worth a warning: if this doesn't stick, a restart forgets what it paused
		// and those torrents stay paused until someone notices.
		g.log.Warn("disk guard: could not record which downloads it paused", "err", err)
	}
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
