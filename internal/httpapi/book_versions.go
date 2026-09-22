package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
)

// Extra audiobook versions of a book (a full-cast production beside the standard
// narration, another narrator): add, edit, remove, search, and drop a version's files.

type audioVersionBody struct {
	Label     string   `json:"label"`
	Terms     []string `json:"terms"`
	Monitored *bool    `json:"monitored"`
}

func (a *api) writeVersionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, books.ErrVersionInvalid):
		a.writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, books.ErrVersionNotFound), errors.Is(err, books.ErrNotFound):
		a.writeError(w, http.StatusNotFound, err.Error())
	default:
		a.writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// handleAddAudioVersion — POST /api/v1/books/{id}/audio-versions
func (a *api) handleAddAudioVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req audioVersionBody
	if !a.decodeJSON(w, r, &req) {
		return
	}
	monitored := req.Monitored == nil || *req.Monitored
	v, err := a.deps.Books.AddAudioVersion(r.Context(), id, req.Label, req.Terms, monitored)
	if err != nil {
		a.writeVersionError(w, err)
		return
	}
	if monitored && len(v.Terms) > 0 {
		// A book that already gave up on automatic searches (two empty tries) would
		// never look for the new version; give it a fresh start and search now.
		a.deps.Books.ResetSearchMisses(r.Context(), id)
		go func(bookID, vid int64) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if _, err := a.deps.Automation.SearchAudioVersionNow(ctx, bookID, vid); err != nil {
				a.deps.Log.Warn("book: first search for a new audiobook version failed", "book", bookID, "version", vid, "err", err)
			}
		}(id, v.ID)
	}
	a.writeJSON(w, http.StatusCreated, v)
}

// handleUpdateAudioVersion — PUT /api/v1/books/{id}/audio-versions/{vid}
func (a *api) handleUpdateAudioVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	vid, ok := a.pathValueID(w, r, "vid")
	if !ok {
		return
	}
	cur, err := a.deps.Books.GetAudioVersion(r.Context(), id, vid)
	if err != nil {
		a.writeVersionError(w, err)
		return
	}
	var req audioVersionBody
	if !a.decodeJSON(w, r, &req) {
		return
	}
	monitored := cur.Monitored
	if req.Monitored != nil {
		monitored = *req.Monitored
	}
	terms := req.Terms
	if terms == nil {
		terms = cur.Terms
	}
	label := req.Label
	if label == "" {
		label = cur.Label
	}
	v, err := a.deps.Books.UpdateAudioVersion(r.Context(), id, vid, label, terms, monitored)
	if err != nil {
		a.writeVersionError(w, err)
		return
	}
	if monitored && !cur.Monitored {
		a.deps.Books.ResetSearchMisses(r.Context(), id)
	}
	a.writeJSON(w, http.StatusOK, v)
}

// handleDeleteAudioVersion — DELETE /api/v1/books/{id}/audio-versions/{vid}[?delete_files=true]
func (a *api) handleDeleteAudioVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	vid, ok := a.pathValueID(w, r, "vid")
	if !ok {
		return
	}
	if err := a.deps.Automation.RemoveAudioVersion(r.Context(), id, vid, r.URL.Query().Get("delete_files") == "true"); err != nil {
		a.writeVersionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteAudioVersionFile — DELETE /api/v1/books/{id}/audio-versions/{vid}/file
func (a *api) handleDeleteAudioVersionFile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	vid, ok := a.pathValueID(w, r, "vid")
	if !ok {
		return
	}
	if err := a.deps.Automation.DeleteAudioVersionFile(r.Context(), id, vid); err != nil {
		a.writeVersionError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// handleSearchAudioVersion — POST /api/v1/books/{id}/audio-versions/{vid}/search
func (a *api) handleSearchAudioVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	vid, ok := a.pathValueID(w, r, "vid")
	if !ok {
		return
	}
	grabbed, err := a.deps.Automation.SearchAudioVersionNow(r.Context(), id, vid)
	if err != nil {
		if errors.Is(err, books.ErrVersionNotFound) || errors.Is(err, books.ErrNotFound) {
			a.writeVersionError(w, err)
			return
		}
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"grabbed": grabbed})
}
