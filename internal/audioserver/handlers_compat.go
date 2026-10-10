package audioserver

import (
	"net/http"
	"strconv"
	"strings"
)

// Routes Audiobookshelf has that this server has nothing behind — collections, playlists,
// narrators, listening-session history, podcasts. Clients written against newer servers
// (Plappa among them) call some of these while loading, and a bare 404 can make them give
// up on the whole screen; an empty answer in the right shape lets them carry on.

// handleAllProgress is every place the user has (Audiobookshelf 2.36+).
func (s *Server) handleAllProgress(w http.ResponseWriter, r *http.Request) {
	all, _ := s.listen.AllProgress(r.Context(), userOf(r).ID)
	out := make([]obj, 0, len(all))
	for _, p := range all {
		out = append(out, mediaProgress(p))
	}
	writeJSON(w, http.StatusOK, obj{"mediaProgress": out})
}

// handleBookmarks is the user's bookmarks — all of them, or one book's (2.36+).
func (s *Server) handleBookmarks(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("id")
	if key != "" {
		if _, err := s.item(r.Context(), key); err != nil {
			writeError(w, http.StatusNotFound, "Item not found")
			return
		}
	}
	bms, _ := s.listen.Bookmarks(r.Context(), userOf(r).ID, key)
	out := make([]obj, 0, len(bms))
	for _, b := range bms {
		out = append(out, bookmarkJSON(b))
	}
	writeJSON(w, http.StatusOK, obj{"bookmarks": out})
}

// handleNoSessions answers the paged session lists (listening history, signed-in devices)
// with an empty page. Places are kept by the progress routes; the history isn't offered
// over this API.
func (s *Server) handleNoSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	per, _ := strconv.Atoi(q.Get("itemsPerPage"))
	if per <= 0 {
		per = 10
	}
	page, _ := strconv.Atoi(q.Get("page"))
	writeJSON(w, http.StatusOK, obj{"total": 0, "numPages": 0, "page": max(page, 0), "itemsPerPage": per, "sessions": []obj{}})
}

// handleSeriesContinue answers hiding (or un-hiding) a series from Continue Series with the
// user, as Audiobookshelf does. Series aren't hidden here — Continue Series just follows
// what's being listened to.
func (s *Server) handleSeriesContinue(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.meJSON(r))
}

// handleEmptyPaged is a library list this server has nothing in (collections, playlists,
// podcast episodes), in the paged shape.
func (s *Server) handleEmptyPaged(w http.ResponseWriter, r *http.Request) {
	if !s.checkLibrary(w, r) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	page, _ := strconv.Atoi(q.Get("page"))
	out := obj{"results": []obj{}, "total": 0, "limit": limit, "page": page}
	if strings.HasSuffix(r.URL.Path, "/recent-episodes") {
		out["episodes"] = []obj{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleNarrators(w http.ResponseWriter, r *http.Request) {
	if !s.checkLibrary(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, obj{"narrators": []obj{}})
}

func (s *Server) handleNoCollections(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, obj{"collections": []obj{}})
}

func (s *Server) handleNoPlaylists(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, obj{"playlists": []obj{}})
}

// handleLibraryStats is the library's totals.
func (s *Server) handleLibraryStats(w http.ResponseWriter, r *http.Request) {
	if !s.checkLibrary(w, r) {
		return
	}
	ctx := r.Context()
	items, err := s.items(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't read the library")
		return
	}
	authors, genres := map[string]bool{}, map[string]bool{}
	var dur float64
	var size int64
	tracks := 0
	for _, it := range items {
		for _, a := range splitAuthors(it.Book.Author) {
			authors[strings.ToLower(a)] = true
		}
		for _, g := range it.Book.Subjects {
			genres[strings.ToLower(g)] = true
		}
		// Only what's already been read — a stats call mustn't probe the whole library.
		files, _ := s.probe.files(ctx, it.Path, false)
		dur += totalDuration(files)
		size += totalSize(files)
		tracks += len(files)
	}
	writeJSON(w, http.StatusOK, obj{
		"totalItems": len(items), "totalAuthors": len(authors), "totalGenres": len(genres), "totalDuration": dur,
		"longestItems": []obj{}, "numAudioTracks": tracks, "totalSize": size, "largestItems": []obj{},
		"authorsWithCount": []obj{}, "genresWithCount": []obj{},
	})
}

// handleUnsupported answers any route this server doesn't have (logged by logRequest).
func (s *Server) handleUnsupported(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "Not Found")
}
