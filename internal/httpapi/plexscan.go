package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/plexscan"
)

// The Plex library-updates card: whether Arrmada tells Plex to scan after changes, how
// each library folder maps onto Plex's, and the last scan. Paths only — the Plex token
// and server address never appear here.

func (a *api) plexScanReady(w http.ResponseWriter) bool {
	if a.deps.PlexScan == nil {
		a.writeError(w, http.StatusServiceUnavailable, "Plex scanning isn't available")
		return false
	}
	return true
}

// handlePlexScanView answers GET /insights/plex/scan.
func (a *api) handlePlexScanView(w http.ResponseWriter, r *http.Request) {
	if !a.plexScanReady(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	a.writeJSON(w, http.StatusOK, a.deps.PlexScan.View(ctx))
}

// handlePlexScanSave answers PUT /insights/plex/scan {enabled, path_map}.
func (a *api) handlePlexScanSave(w http.ResponseWriter, r *http.Request) {
	if !a.plexScanReady(w) {
		return
	}
	var req struct {
		Enabled bool               `json:"enabled"`
		PathMap []plexscan.PathMap `json:"path_map"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if len(req.PathMap) > plexscan.MaxPathMaps*2 {
		a.writeError(w, http.StatusBadRequest, "too many path mappings")
		return
	}
	if err := a.deps.PlexScan.Save(r.Context(), req.Enabled, req.PathMap); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	a.writeJSON(w, http.StatusOK, a.deps.PlexScan.View(ctx))
}

// handlePlexScanTest answers POST /insights/plex/scan/test {kind, run}: where kind's
// library folder resolves on Plex, and with run, a scan of it now.
func (a *api) handlePlexScanTest(w http.ResponseWriter, r *http.Request) {
	if !a.plexScanReady(w) {
		return
	}
	var req struct {
		Kind string `json:"kind"`
		Run  bool   `json:"run"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rv, err := a.deps.PlexScan.ScanNow(ctx, req.Kind, req.Run)
	if err != nil {
		status := http.StatusBadGateway // Plex didn't answer, or refused
		if errors.Is(err, plexscan.ErrUnavailable) {
			status = http.StatusBadRequest
		}
		a.writeJSON(w, status, map[string]any{"status": "error", "message": err.Error(), "root": rv})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"root": rv, "last_scan": a.deps.PlexScan.LastScan()})
}
