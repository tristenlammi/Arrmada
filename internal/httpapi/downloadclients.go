package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
)

// clientView is a download client as the settings page sees it: its settings (the
// password is never marshalled) and, when it has failed, since when and why.
type clientView struct {
	download.Client
	Status *clientStatus `json:"status,omitempty"`
}

// clientStatus is what the card says about a client that isn't answering. state is ok,
// failing or unknown (not asked since it was added or edited); last_error is redacted
// before it's stored.
type clientStatus struct {
	State        string     `json:"state"`
	FailingSince *time.Time `json:"failing_since,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	LastErrorAt  *time.Time `json:"last_error_at,omitempty"`
}

func (a *api) clientView(c download.Client) clientView {
	v := clientView{Client: c}
	if st, ok := a.deps.Downloads.Status(c.ID); ok {
		v.Status = &clientStatus{State: st.Phase(time.Now()), FailingSince: timePtr(st.FailingSince)}
		if st.ConsecutiveFailures > 0 {
			v.Status.LastError, v.Status.LastErrorAt = st.LastError, timePtr(st.LastErrorAt)
		}
	}
	return v
}

func (a *api) handleListDownloadClients(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Downloads.List(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list download clients")
		return
	}
	out := make([]clientView, 0, len(list))
	hasBundled := false
	for _, c := range list {
		out = append(out, a.clientView(c))
		hasBundled = hasBundled || c.Bundled
	}
	// The categories are Arrmada's, not the client's: the page lists them read-only so
	// the owner can see what to expect in qBittorrent. can_restore_bundled offers the
	// bundled qBittorrent back when this install has one and its row is gone.
	a.writeJSON(w, http.StatusOK, map[string]any{
		"clients":             out,
		"categories":          download.FixedCategories(a.deps.Config.DownloadCategory),
		"can_restore_bundled": a.deps.Config.QbittorrentURL != "" && !hasBundled,
	})
}

// maxClientPriority bounds the order field; anything past it is a typo.
const maxClientPriority = 99

// clientPriority checks an optional priority from a request: nil means keep (0 to the
// store), otherwise 1..maxClientPriority.
func clientPriority(p *int) (int, error) {
	if p == nil {
		return 0, nil
	}
	if *p < 1 || *p > maxClientPriority {
		return 0, fmt.Errorf("order must be between 1 and %d", maxClientPriority)
	}
	return *p, nil
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
	// Priority is the client's place in the order new downloads try (1 = first); left
	// out, a new client gets the default and an edited one keeps its own.
	Priority *int `json:"priority"`
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
	priority, err := clientPriority(req.Priority)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	created, err := a.deps.Downloads.Create(r.Context(), download.Client{
		Name:     req.Name,
		Kind:     download.Kind(req.Kind),
		URL:      req.URL,
		Username: req.Username,
		Password: req.Password,
		Enabled:  enabled,
		Priority: priority,
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
// sent back. The bundled client's URL can be edited too: startup finds it by its bundled
// flag now, not its URL, so an edit no longer brings a duplicate back.
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
	enabled := cur.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	priority, err := clientPriority(req.Priority)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := a.deps.Downloads.Update(r.Context(), download.Client{
		ID:       id,
		Name:     req.Name,
		URL:      req.URL,
		Username: req.Username,
		Password: req.Password,
		Enabled:  enabled,
		Priority: priority,
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

// handleRestoreBundledClient brings back the packaged qBittorrent after it was deleted:
// a deleted bundled client now stays deleted across restarts, so this is the way back.
func (a *api) handleRestoreBundledClient(w http.ResponseWriter, r *http.Request) {
	url := a.deps.Config.QbittorrentURL
	if url == "" {
		a.writeError(w, http.StatusBadRequest, "this install has no bundled qBittorrent")
		return
	}
	if err := a.deps.Downloads.RestoreBundled(r.Context(), url); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not restore the bundled qBittorrent")
		return
	}
	a.recheckHealth(r, "downloads")
	a.writeJSON(w, http.StatusOK, map[string]any{"restored": true})
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
