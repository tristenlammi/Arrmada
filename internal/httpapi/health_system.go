package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/health"
	"github.com/tristenlammi/arrmada/internal/libroots"
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

	// Media in or above the data folder mixes it with the database, backups and logs.
	// Saving such a folder is refused now, but an older save or the environment can
	// still carry one, and the app keeps running on it — so say it in red.
	picked := a.pickedConfig(ctx)
	underData := map[string]bool{}
	for _, d := range libraryDirSettings(&picked) {
		if *d.field != "" && libroots.UnderDataDir(*d.field, a.deps.Config.DataDir) {
			underData[*d.field] = true
			add("error", fmt.Sprintf("The %s folder (%s) is inside (or contains) Arrmada's data folder — move it to its own mount.",
				folderLabel[d.name], *d.field))
		}
	}

	// Each folder the user picked must be there and writable, or imports (and
	// downloads) fail. Only modules that are on are checked.
	folders := health.LibraryFolders(picked, a.booksEnabled(ctx), a.musicEnabled(ctx))
	for _, f := range folders {
		if underData[f.Path] {
			continue // already reported, and nothing is probed inside the data folder
		}
		if level, msg := folderProblem(f, folderProbes.ProbeFolder(f.Path)); msg != "" {
			add(level, msg)
		}
	}

	// Deletes move files into a recycle bin; a bin on another drive from the library it
	// serves turns every delete into a full copy. The old shared bin still holding files
	// is worth a nudge too: it drains only when someone empties it or it ages out.
	if a.deps.Recycle != nil {
		for _, p := range a.deps.Recycle.Problems() {
			// The legacy bin gets its own line below: only stray files go there now, so
			// "every delete is a copy" would overstate it.
			if p.OtherDrive && !p.Legacy {
				add("warning", fmt.Sprintf("The recycle bin %s is on a different drive from your library, so every delete is a full copy, which is slow and fills that drive.", p.Dir))
			}
			if p.LegacyFull {
				add("warning", fmt.Sprintf("The old shared recycle bin (%s) still holds deleted files; new deletes no longer go there. Empty it in Settings → System → Recycle bin once you've checked what's in it.", p.Dir))
			}
		}
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
	dl := picked.DownloadsDir
	if free, ok := diskspace.FreeGB(dl); ok {
		disk = map[string]any{"free_gb": fmt.Sprintf("%.1f", free), "path": dl}
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

// folderProbes keeps the polled health panel from writing a probe file into every
// library folder on every poll — on Unraid that can wake sleeping array disks. A folder
// that passed is trusted for an hour; one that failed is re-checked each time.
var folderProbes = health.NewProbeCache(time.Hour)

// folderProblem turns a folder probe into a health line, or "" when the folder is fine.
// A missing folder whose parent can be written is only a warning — imports create it —
// but a missing parent means the share isn't mounted at all.
func folderProblem(f health.Folder, st health.FolderState) (level, msg string) {
	switch {
	case !st.Exists && st.Err != nil:
		return "error", fmt.Sprintf("Arrmada can't look at the %s folder %s: %v.", f.Label, f.Path, st.Err)
	case !st.Exists && st.ParentExists && st.ParentWritable:
		return "warning", fmt.Sprintf("The %s folder %s doesn't exist yet. Arrmada will create it when it's first needed; if it should be an existing share, check the container's volume mapping.", f.Label, f.Path)
	case !st.Exists && st.ParentExists:
		return "error", fmt.Sprintf("The %s folder %s doesn't exist, and Arrmada can't create it (check the container's volume mapping and the share's permissions).", f.Label, f.Path)
	case !st.Exists:
		return "error", fmt.Sprintf("The %s folder %s isn't there — the share isn't mounted.", f.Label, f.Path)
	case !st.IsDir:
		return "error", fmt.Sprintf("The %s folder %s is a file, not a folder.", f.Label, f.Path)
	case !st.Writable:
		return "error", fmt.Sprintf("Arrmada can't write to the %s folder %s (check PUID/PGID and the share's permissions).", f.Label, f.Path)
	}
	return "", ""
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
