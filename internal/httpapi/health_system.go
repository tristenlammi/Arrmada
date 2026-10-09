package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/download"
)

// healthWarning is one operational problem surfaced to the user.
type healthWarning struct {
	Level   string `json:"level"` // "error" (nothing works) | "warning" (degraded)
	Message string `json:"message"`
}

// handleSystemHealth reports operational health: whether the pieces needed to
// actually acquire movies are present and working, plus free disk space. This is
// the "why isn't anything downloading?" panel.
func (a *api) handleSystemHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var warns []healthWarning
	add := func(level, msg string) { warns = append(warns, healthWarning{Level: level, Message: msg}) }

	// Indexers — without one, there's nothing to search.
	if ix, err := a.deps.Indexers.List(ctx); err == nil {
		enabled := 0
		for _, i := range ix {
			if i.Enabled {
				enabled++
			}
		}
		if enabled == 0 {
			add("error", "No indexers are enabled — Arrmada can't search for releases.")
		}
	}

	// Download client — configured and reachable.
	var queue []download.Item
	queueRead := false
	if clients, err := a.deps.Downloads.List(ctx); err != nil || len(clients) == 0 {
		add("error", "No download client is configured — grabbed releases have nowhere to download.")
	} else if q, err := a.deps.Downloads.Queue(ctx); err != nil {
		add("error", "The download client is unreachable: "+err.Error())
	} else {
		queue, queueRead = q, true
	}

	// Library folder — must exist and be writable, or imports fail.
	lib := a.deps.Config.LibraryDir
	if !writable(lib) {
		add("error", "The library folder isn't writable: "+lib)
	}

	// The disk guard actively holding the queue is the single most confusing reason
	// for "nothing is downloading" — everything else looks healthy — so say it plainly
	// and before the raw free-space line.
	if a.deps.DiskGuard != nil {
		g := a.deps.DiskGuard.Status(ctx)
		// Count only held torrents the client still has: one deleted while held would
		// otherwise keep promising a resume that can never happen.
		holding := g.Holding
		if queueRead {
			holding = heldInQueue(a.deps.DiskGuard.Held(ctx), queue)
		}
		if g.Enabled && holding > 0 {
			add("warning", fmt.Sprintf(
				"Downloads are paused: the downloads volume is %.1f%% full (pause at %d%%). "+
					"%d torrent%s will resume automatically once it drops below %d%%.",
				g.UsedPct, g.PausePct, holding, plural(holding), g.ResumePct))
		}
	}

	// Free disk space on the downloads volume.
	var disk map[string]any
	if free, ok := diskspace.FreeGB(a.deps.Config.DownloadsDir); ok {
		disk = map[string]any{"free_gb": fmt.Sprintf("%.1f", free), "path": a.deps.Config.DownloadsDir}
		switch {
		case free < 2:
			add("error", fmt.Sprintf("Very low free disk space (%.1f GB) on the downloads volume.", free))
		case free < 10:
			add("warning", fmt.Sprintf("Low free disk space (%.1f GB) on the downloads volume.", free))
		}
	}

	// The audiobook server is switched on but couldn't start (usually its port is taken).
	if a.deps.AudioManager != nil && a.deps.Settings.GetBool(ctx, audioserver.KeyEnabled, false) {
		if running, lastErr := a.deps.AudioManager.Running(); !running {
			msg := "The audiobook server is switched on but isn't running, so listening apps can't connect."
			if lastErr != "" {
				msg += " " + lastErr
			}
			add("error", msg)
		}
	}

	// Nightly database backups have stopped (usually a full disk on the data volume).
	if a.deps.Backups != nil {
		if msg := a.deps.Backups.HealthWarning(ctx, time.Since(a.start)); msg != "" {
			add("warning", msg)
		}
	}

	status := "ok"
	for _, wrn := range warns {
		if wrn.Level == "error" {
			status = "error"
			break
		}
		status = "warning"
	}
	if warns == nil {
		warns = []healthWarning{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": status, "warnings": warns, "disk": disk})
}

// writable reports whether dir exists and the process can create files in it.
func writable(dir string) bool {
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".arrmada-write-test")
	f, err := os.Create(probe)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return true
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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
