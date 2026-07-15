package httpapi

import (
	"errors"
	"net/http"

	"github.com/tristenlammi/arrmada/internal/download"
)

func (a *api) handleListDownloadClients(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Downloads.List(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list download clients")
		return
	}
	if list == nil {
		list = []download.Client{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"clients": list})
}

type createClientRequest struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	Category string `json:"category"`
	Enabled  *bool  `json:"enabled"`
}

func (a *api) handleCreateDownloadClient(w http.ResponseWriter, r *http.Request) {
	var req createClientRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" || req.URL == "" {
		a.writeError(w, http.StatusBadRequest, "name and url are required")
		return
	}
	if download.Kind(req.Kind) != download.KindQbittorrent {
		a.writeError(w, http.StatusBadRequest, "kind must be 'qbittorrent'")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	created, err := a.deps.Downloads.Create(r.Context(), download.Client{
		Name:     req.Name,
		Kind:     download.Kind(req.Kind),
		URL:      req.URL,
		Username: req.Username,
		Password: req.Password,
		Category: req.Category,
		Enabled:  enabled,
	})
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not create download client")
		return
	}
	a.writeJSON(w, http.StatusCreated, created)
}

func (a *api) handleDeleteDownloadClient(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Downloads.Delete(r.Context(), id); err != nil {
		if errors.Is(err, download.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "download client not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete download client")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) handleTestDownloadClient(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	err := a.deps.Downloads.Test(r.Context(), id)
	if errors.Is(err, download.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "download client not found")
		return
	}
	if err != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDownloadClientStatus reports live client info — currently the incoming
// BitTorrent port, so the UI can tell the user which port to forward.
func (a *api) handleDownloadClientStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	port, err := a.deps.Downloads.ListenPort(r.Context(), id)
	if err != nil {
		// Not fatal — the client may be briefly unreachable. Report 0.
		a.writeJSON(w, http.StatusOK, map[string]any{"listen_port": 0})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"listen_port": port})
}

func (a *api) handleQueue(w http.ResponseWriter, r *http.Request) {
	items, err := a.deps.Downloads.Queue(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read queue")
		return
	}
	if items == nil {
		items = []download.Item{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
