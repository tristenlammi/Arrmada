package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/tristenlammi/arrmada/internal/diskspace"
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
	if clients, err := a.deps.Downloads.List(ctx); err != nil || len(clients) == 0 {
		add("error", "No download client is configured — grabbed releases have nowhere to download.")
	} else if _, err := a.deps.Downloads.Queue(ctx); err != nil {
		add("error", "The download client is unreachable: "+err.Error())
	}

	// Library folder — must exist and be writable, or imports fail.
	lib := a.deps.Config.LibraryDir
	if !writable(lib) {
		add("error", "The library folder isn't writable: "+lib)
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
