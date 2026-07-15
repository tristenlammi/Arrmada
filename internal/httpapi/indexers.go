package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/indexer"
)

func (a *api) handleListIndexers(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Indexers.List(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list indexers")
		return
	}
	if list == nil {
		list = []indexer.Indexer{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"indexers": list})
}

type createIndexerRequest struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	URL        string `json:"url"`
	APIKey     string `json:"api_key"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Categories []int    `json:"categories"`
	MediaTypes []string `json:"media_types"`
	Priority   int      `json:"priority"`
	MinSeeders  int     `json:"min_seeders"`
	SeedEnabled *bool   `json:"seed_enabled"`
	SeedRatio   float64 `json:"seed_ratio"`
	SeedHours   int     `json:"seed_hours"`
	Enabled     *bool   `json:"enabled"`
}

func (a *api) handleCreateIndexer(w http.ResponseWriter, r *http.Request) {
	var req createIndexerRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		a.writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	kind := indexer.Kind(req.Kind)
	switch kind {
	case indexer.KindTorznab, indexer.KindNewznab:
		if req.URL == "" {
			a.writeError(w, http.StatusBadRequest, "url is required for torznab/newznab indexers")
			return
		}
	case indexer.KindTorrentLeech:
		if req.Username == "" || req.Password == "" {
			a.writeError(w, http.StatusBadRequest, "username and password are required for torrentleech")
			return
		}
	case indexer.KindMAM:
		if req.APIKey == "" {
			a.writeError(w, http.StatusBadRequest, "mam_id session is required for myanonamouse")
			return
		}
	case indexer.KindX1337:
		// Public — no credentials; URL optional (mirror).
	default:
		a.writeError(w, http.StatusBadRequest, "unknown indexer kind")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	seedEnabled := true
	if req.SeedEnabled != nil {
		seedEnabled = *req.SeedEnabled
	}

	created, err := a.deps.Indexers.Create(r.Context(), indexer.Indexer{
		Name:       req.Name,
		Kind:       kind,
		URL:        req.URL,
		APIKey:     req.APIKey,
		Username:   req.Username,
		Password:   req.Password,
		Categories: req.Categories,
		MediaTypes: req.MediaTypes,
		Priority:   req.Priority,
		MinSeeders: req.MinSeeders,
		SeedEnabled: seedEnabled,
		SeedRatio:  req.SeedRatio,
		SeedHours:  req.SeedHours,
		Enabled:    enabled,
	})
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not create indexer")
		return
	}
	a.writeJSON(w, http.StatusCreated, created)
}

func (a *api) handleUpdateIndexer(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req createIndexerRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		a.writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	kind := indexer.Kind(req.Kind)
	switch kind {
	case indexer.KindTorznab, indexer.KindNewznab, indexer.KindTorrentLeech, indexer.KindX1337, indexer.KindMAM:
	default:
		a.writeError(w, http.StatusBadRequest, "invalid kind")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	seedEnabled := true
	if req.SeedEnabled != nil {
		seedEnabled = *req.SeedEnabled
	}

	err := a.deps.Indexers.Update(r.Context(), indexer.Indexer{
		ID:         id,
		Name:       req.Name,
		Kind:       kind,
		URL:        req.URL,
		APIKey:     req.APIKey, // blank = keep existing
		Username:   req.Username,
		Password:   req.Password, // blank = keep existing
		Categories: req.Categories,
		MediaTypes: req.MediaTypes,
		Priority:   req.Priority,
		MinSeeders: req.MinSeeders,
		SeedEnabled: seedEnabled,
		SeedRatio:  req.SeedRatio,
		SeedHours:  req.SeedHours,
		Enabled:    enabled,
	})
	if errors.Is(err, indexer.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "indexer not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update indexer")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) handleDeleteIndexer(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Indexers.Delete(r.Context(), id); err != nil {
		if errors.Is(err, indexer.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "indexer not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete indexer")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) handleTestIndexer(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	err := a.deps.Indexers.Test(r.Context(), id)
	if errors.Is(err, indexer.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "indexer not found")
		return
	}
	if err != nil {
		// A failed connection test is a valid result, not a server error.
		a.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *api) handleSearch(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	result, err := a.deps.Indexers.Search(r.Context(), indexer.SearchQuery{
		Text:  r.URL.Query().Get("q"),
		Limit: limit,
	})
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "search failed")
		return
	}
	if result.Releases == nil {
		result.Releases = []indexer.Release{}
	}
	a.writeJSON(w, http.StatusOK, result)
}

// pathID parses the {id} path segment, writing a 400 on failure.
func (a *api) pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return a.pathValueID(w, r, "id")
}

// pathValueID parses a named path segment as an int64.
func (a *api) pathValueID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid "+name)
		return 0, false
	}
	return id, true
}
