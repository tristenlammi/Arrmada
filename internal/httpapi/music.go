package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// handleListArtists returns the music library.
func (a *api) handleListArtists(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Music.ListArtists(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list artists")
		return
	}
	if list == nil {
		list = []music.Artist{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"artists": list})
}

// handleLookupArtists searches MusicBrainz for artists to add.
func (a *api) handleLookupArtists(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		a.writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	res, err := a.deps.Music.Lookup(r.Context(), q)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "artist search failed: "+err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"results": res})
}

// handleAddArtist adds an artist and pulls its album list.
func (a *api) handleAddArtist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MBID           string `json:"mbid"`
		QualityProfile string `json:"quality_profile"`
		Monitored      *bool  `json:"monitored"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.MBID) == "" {
		a.writeError(w, http.StatusBadRequest, "mbid is required")
		return
	}
	if req.QualityProfile == "" {
		req.QualityProfile = a.deps.Quality.DefaultProfile(r.Context(), quality.MediaMusic)
	}
	monitored := req.Monitored == nil || *req.Monitored
	art, err := a.deps.Music.AddArtist(r.Context(), strings.TrimSpace(req.MBID), req.QualityProfile, monitored)
	switch {
	case errors.Is(err, music.ErrExists):
		a.writeError(w, http.StatusConflict, "that artist is already in your library")
		return
	case err != nil:
		a.writeError(w, http.StatusBadGateway, "could not add the artist: "+err.Error())
		return
	}
	a.writeJSON(w, http.StatusCreated, art)
}

// handleGetArtist returns one artist with its albums.
func (a *api) handleGetArtist(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	art, err := a.deps.Music.GetArtist(r.Context(), id)
	if err != nil {
		a.musicErr(w, err, "could not load the artist")
		return
	}
	a.writeJSON(w, http.StatusOK, art)
}

// handleRefreshArtist re-pulls an artist's metadata and album list.
func (a *api) handleRefreshArtist(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	art, err := a.deps.Music.Refresh(r.Context(), id)
	if err != nil {
		a.musicErr(w, err, "could not refresh the artist")
		return
	}
	a.writeJSON(w, http.StatusOK, art)
}

// handleSetArtistMonitored toggles an artist (cascading to its albums and tracks).
func (a *api) handleSetArtistMonitored(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Monitored bool `json:"monitored"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Music.SetArtistMonitored(r.Context(), id, req.Monitored); err != nil {
		a.musicErr(w, err, "could not update the artist")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"monitored": req.Monitored})
}

// handleSetArtistProfile changes an artist's quality profile.
func (a *api) handleSetArtistProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		QualityProfile string `json:"quality_profile"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if !a.deps.Automation.KnownProfile(r.Context(), req.QualityProfile) {
		a.writeError(w, http.StatusBadRequest, "unknown quality profile")
		return
	}
	if err := a.deps.Music.SetQualityProfile(r.Context(), id, req.QualityProfile); err != nil {
		a.musicErr(w, err, "could not update the profile")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"quality_profile": req.QualityProfile})
}

// handleDeleteArtist removes an artist; albums and tracks cascade away.
func (a *api) handleDeleteArtist(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Music.Delete(r.Context(), id); err != nil {
		a.musicErr(w, err, "could not delete the artist")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// handleArtistHistory returns an artist's activity timeline.
func (a *api) handleArtistHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	events, err := a.deps.Music.Events(r.Context(), id, 100)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	if events == nil {
		events = []music.Event{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// handleGetAlbum returns one album with its track listing. The listing is fetched from
// MusicBrainz the first time an album is opened — see music.Service.EnsureTracks for why it
// isn't pulled when the artist is added.
func (a *api) handleGetAlbum(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	al, err := a.deps.Music.GetAlbum(r.Context(), id)
	if err != nil {
		a.musicErr(w, err, "could not load the album")
		return
	}
	a.writeJSON(w, http.StatusOK, al)
}

// handleSetAlbumMonitored toggles one album (and its tracks).
func (a *api) handleSetAlbumMonitored(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Monitored bool `json:"monitored"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Music.SetAlbumMonitored(r.Context(), id, req.Monitored); err != nil {
		a.musicErr(w, err, "could not update the album")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"monitored": req.Monitored})
}

// musicErr maps a module error to a status, so a missing id is a 404 rather than a 500 —
// the mistake the Books handlers made on seven endpoints.
func (a *api) musicErr(w http.ResponseWriter, err error, msg string) {
	if errors.Is(err, music.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "not found")
		return
	}
	a.writeError(w, http.StatusInternalServerError, msg)
}

// handleScanMusicLibrary catalogues music already on disk. Runs in the background: a big
// collection means one MusicBrainz lookup per unknown artist at one request per second,
// far longer than any sensible request timeout.
func (a *api) handleScanMusicLibrary(w http.ResponseWriter, r *http.Request) {
	if !a.musicScan.CompareAndSwap(false, true) {
		a.writeError(w, http.StatusConflict, "a music scan is already running")
		return
	}
	go func() {
		defer a.musicScan.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		res, err := a.deps.Automation.ScanMusicLibrary(ctx)
		if err != nil {
			a.deps.Log.Warn("music scan failed", "err", err)
			return
		}
		a.deps.Log.Info("music scan complete", "artists", res.Artists, "albums", res.Albums,
			"tracks", res.Tracks, "unmatched", len(res.Unmatched))
		a.deps.Bus.Publish("library.scanned", map[string]any{
			"media": "music", "artists": res.Artists, "albums": res.Albums,
			"tracks": res.Tracks, "unmatched": len(res.Unmatched),
		})
	}()
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "scanning"})
}
