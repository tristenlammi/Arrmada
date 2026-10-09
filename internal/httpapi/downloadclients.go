package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tristenlammi/arrmada/internal/download"
)

// clientView is a download client as the settings page sees it. Bundled marks the
// packaged qBittorrent: its row is re-created at startup whenever no client has its URL,
// so the page keeps that URL read-only and says a delete won't stick.
type clientView struct {
	download.Client
	Bundled bool `json:"bundled"`
}

func (a *api) clientView(c download.Client) clientView {
	return clientView{Client: c, Bundled: a.isBundledClient(c)}
}

func (a *api) isBundledClient(c download.Client) bool {
	return a.deps.Config.QbittorrentURL != "" && c.URL == a.deps.Config.QbittorrentURL
}

func (a *api) handleListDownloadClients(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Downloads.List(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list download clients")
		return
	}
	out := make([]clientView, 0, len(list))
	for _, c := range list {
		out = append(out, a.clientView(c))
	}
	// The categories are Arrmada's, not the client's: the page lists them read-only so
	// the owner can see what to expect in qBittorrent.
	a.writeJSON(w, http.StatusOK, map[string]any{
		"clients":    out,
		"categories": download.FixedCategories(a.deps.Config.DownloadCategory),
	})
}

type createClientRequest struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	// Category is accepted so an older page's request still parses, and ignored:
	// Arrmada picks the category for every download (download/categories.go).
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
		Enabled:  enabled,
	})
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not create download client")
		return
	}
	a.recheckHealth(r, "downloads")
	a.writeJSON(w, http.StatusCreated, a.clientView(created))
}

// handleUpdateDownloadClient edits a client in place — a changed password or URL no longer
// means delete and re-add. A blank password keeps the stored one; the stored one is never
// sent back. The bundled client's URL can't be changed: startup re-creates a row for that
// URL whenever none has it, so an edited bundled client would come back as a duplicate.
func (a *api) handleUpdateDownloadClient(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req createClientRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	req.Name, req.URL = strings.TrimSpace(req.Name), strings.TrimSpace(req.URL)
	if req.Name == "" || req.URL == "" {
		a.writeError(w, http.StatusBadRequest, "name and url are required")
		return
	}
	if download.Kind(req.Kind) != download.KindQbittorrent {
		a.writeError(w, http.StatusBadRequest, "kind must be 'qbittorrent'")
		return
	}
	cur, err := a.deps.Downloads.Get(r.Context(), id)
	if errors.Is(err, download.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "download client not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read download client")
		return
	}
	if a.isBundledClient(cur) && req.URL != cur.URL {
		a.writeError(w, http.StatusBadRequest, "the bundled qBittorrent's URL can't be changed; add another client instead")
		return
	}
	enabled := cur.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	updated, err := a.deps.Downloads.Update(r.Context(), download.Client{
		ID:       id,
		Name:     req.Name,
		URL:      req.URL,
		Username: req.Username,
		Password: req.Password,
		Enabled:  enabled,
	})
	if errors.Is(err, download.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "download client not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update download client")
		return
	}
	// The health panel's client check reads enabled clients; re-run it so switching one
	// off (or fixing its password) shows now rather than at the next scheduled check.
	a.recheckHealth(r, "downloads")
	a.writeJSON(w, http.StatusOK, a.clientView(updated))
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
	a.recheckHealth(r, "downloads")
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
