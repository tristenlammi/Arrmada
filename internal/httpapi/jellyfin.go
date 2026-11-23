package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/jellyfinsvc"
	"github.com/tristenlammi/arrmada/internal/plexscan"
)

// The Jellyfin connection's routes (its card on Settings → Connections). The API key goes
// up on PUT and never comes back: answers say key_set only, and a Test's detail never
// carries it.

const jellyfinHealthKey = "jellyfin.connection"

func (a *api) jellyfinReady(w http.ResponseWriter) bool {
	if a.deps.Jellyfin == nil {
		a.writeError(w, http.StatusServiceUnavailable, "Jellyfin isn't available")
		return false
	}
	return true
}

// jellyfinConfigView is the saved connection plus which server it is: "jellyfin" or
// "emby" (Emby connects through the same slot).
type jellyfinConfigView struct {
	jellyfinsvc.Config
	ServerKind string `json:"server_kind"`
}

func (a *api) jellyfinConfig(ctx context.Context) jellyfinConfigView {
	return jellyfinConfigView{Config: a.deps.Jellyfin.Config(ctx), ServerKind: a.deps.Jellyfin.ServerKind(ctx)}
}

// handleJellyfinConfig answers GET /api/v1/jellyfin.
func (a *api) handleJellyfinConfig(w http.ResponseWriter, r *http.Request) {
	if !a.jellyfinReady(w) {
		return
	}
	a.writeJSON(w, http.StatusOK, a.jellyfinConfig(r.Context()))
}

// handleJellyfinSave answers PUT /api/v1/jellyfin {server_kind?, url, public_url, api_key?}
// (server_kind "emby" connects an Emby server through the same slot): a missing
// or blank key keeps the saved one. Answers with the saved connection.
func (a *api) handleJellyfinSave(w http.ResponseWriter, r *http.Request) {
	if !a.jellyfinReady(w) {
		return
	}
	var req struct {
		// ServerKind is "jellyfin" or "emby"; missing or blank keeps the saved kind.
		ServerKind string `json:"server_kind"`
		URL        string `json:"url"`
		PublicURL  string `json:"public_url"`
		APIKey     string `json:"api_key"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := a.deps.Jellyfin.SaveFor(ctx, req.ServerKind, req.URL, req.PublicURL, req.APIKey); err != nil {
		if errors.Is(err, jellyfinsvc.ErrInvalid) {
			a.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.deps.Log.Warn("jellyfin: couldn't save the connection", "err", err)
		a.writeError(w, http.StatusInternalServerError, "could not save the "+a.deps.Jellyfin.Label(r.Context())+" connection")
		return
	}
	a.recheckHealth(r, jellyfinHealthKey) // a fixed key clears its warning now
	a.writeJSON(w, http.StatusOK, a.jellyfinConfig(r.Context()))
}

// handleJellyfinDisconnect answers DELETE /api/v1/jellyfin (admin): the address, the key
// and the server's identity are forgotten; how Arrmada used it stays for a reconnect.
// Answers with the (now empty) connection.
func (a *api) handleJellyfinDisconnect(w http.ResponseWriter, r *http.Request) {
	if !a.jellyfinReady(w) {
		return
	}
	if err := a.deps.Jellyfin.Disconnect(r.Context()); err != nil {
		a.deps.Log.Warn("jellyfin: couldn't disconnect", "err", err)
		a.writeError(w, http.StatusInternalServerError, "could not disconnect Jellyfin")
		return
	}
	a.deps.Log.Info("jellyfin: disconnected")
	a.recheckHealth(r, jellyfinHealthKey) // an unconfigured server has nothing to warn about
	a.writeJSON(w, http.StatusOK, a.jellyfinConfig(r.Context()))
}

// handleJellyfinTest answers POST /api/v1/jellyfin/test {url?, api_key?}: the typed values
// (blank = the saved ones) tested without saving. Always 200: ok says how it went.
func (a *api) handleJellyfinTest(w http.ResponseWriter, r *http.Request) {
	if !a.jellyfinReady(w) {
		return
	}
	var req struct {
		ServerKind string `json:"server_kind"` // "" = the saved kind
		URL        string `json:"url"`
		APIKey     string `json:"api_key"`
	}
	if !a.decodeJSON(w, r, &req) { // {} tests the saved connection
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res := a.deps.Jellyfin.TestFor(ctx, req.ServerKind, req.URL, req.APIKey)
	res.Detail = connstatus.Redact(res.Detail)
	a.writeJSON(w, http.StatusOK, res)
}

// GET/PUT /api/v1/jellyfin/scan and POST /api/v1/jellyfin/scan/test: Jellyfin's library
// updates, the same shapes as Plex's (/insights/plex/scan). PUT takes {enabled, path_map};
// scan_on_import is accepted for enabled.
func (a *api) handleJellyfinScanView(w http.ResponseWriter, r *http.Request) {
	if !a.scannerReady(w, a.deps.JellyfinScan, "Jellyfin") {
		return
	}
	a.scanView(w, r, a.deps.JellyfinScan)
}

func (a *api) handleJellyfinScanSave(w http.ResponseWriter, r *http.Request) {
	if !a.scannerReady(w, a.deps.JellyfinScan, "Jellyfin") {
		return
	}
	var req struct {
		Enabled      *bool               `json:"enabled"`
		ScanOnImport *bool               `json:"scan_on_import"`
		PathMap      *[]plexscan.PathMap `json:"path_map"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		req.Enabled = req.ScanOnImport
	}
	a.scanSave(w, r, a.deps.JellyfinScan, req.Enabled, req.PathMap)
}

func (a *api) handleJellyfinScanTest(w http.ResponseWriter, r *http.Request) {
	if !a.scannerReady(w, a.deps.JellyfinScan, "Jellyfin") {
		return
	}
	a.scanTest(w, r, a.deps.JellyfinScan)
}
