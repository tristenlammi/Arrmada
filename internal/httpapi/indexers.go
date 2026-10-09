package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/indexer"
)

// indexerView is an indexer as the Indexers page reads it: its settings (secrets are
// never marshalled) and how it has been answering.
type indexerView struct {
	indexer.Indexer
	Status *indexerStatus `json:"status,omitempty"`
	// CapsSummary is what the indexer last said it supports, e.g. "Movies (imdbid,
	// tmdbid) · TV (tvdbid, season, ep) · 23 categories"; absent until read.
	CapsSummary string `json:"caps_summary,omitempty"`
}

// indexerStatus is the row's dot and its one line of detail. State is ok, failing (it
// failed last time; sweeps still ask it), backing_off (sweeps leave it alone until
// backoff_until), disabled, or unknown (not asked since it was added or edited). Times
// are omitted when they never happened. last_error is redacted before it's stored.
type indexerStatus struct {
	State               string     `json:"state"`
	LastOKAt            *time.Time `json:"last_ok_at,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	LastErrorAt         *time.Time `json:"last_error_at,omitempty"`
	FailingSince        *time.Time `json:"failing_since,omitempty"`
	BackoffUntil        *time.Time `json:"backoff_until,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	Queries24h          int        `json:"queries_24h"`
	Failures24h         int        `json:"failures_24h"`
}

// statusFor builds an indexer's status for the page.
func statusFor(idx indexer.Indexer, st connstatus.State, counts connstatus.Counts, now time.Time) *indexerStatus {
	out := &indexerStatus{
		State:               st.Phase(now),
		LastOKAt:            timePtr(st.LastOKAt),
		LastError:           st.LastError,
		LastErrorAt:         timePtr(st.LastErrorAt),
		FailingSince:        timePtr(st.FailingSince),
		ConsecutiveFailures: st.ConsecutiveFailures,
		Queries24h:          counts.Queries,
		Failures24h:         counts.Failures,
	}
	if now.Before(st.BackoffUntil) {
		out.BackoffUntil = timePtr(st.BackoffUntil)
	}
	if !idx.Enabled {
		// Nothing asks a disabled indexer, so its last state is history, not health.
		out.State = "disabled"
		out.BackoffUntil = nil
	}
	return out
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (a *api) handleListIndexers(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Indexers.List(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list indexers")
		return
	}
	// The route is staff-only already; the status (error text included) is kept to
	// managers here too, so a looser route later can't leak it.
	u, _ := userFrom(r)
	withStatus := u != nil && u.Role.AtLeast(auth.RoleManager)
	now := time.Now()
	out := make([]indexerView, 0, len(list))
	for _, idx := range list {
		v := indexerView{Indexer: idx, CapsSummary: idx.CapsSummary()}
		if withStatus {
			st, counts, _ := a.deps.Indexers.Status(idx.ID)
			v.Status = statusFor(idx, st, counts, now)
		}
		out = append(out, v)
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"indexers": out})
}

type createIndexerRequest struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	URL         string   `json:"url"`
	APIKey      string   `json:"api_key"`
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	Categories  []int    `json:"categories"`
	MediaTypes  []string `json:"media_types"`
	Priority    int      `json:"priority"`
	MinSeeders  int      `json:"min_seeders"`
	SeedEnabled *bool    `json:"seed_enabled"`
	SeedRatio   float64  `json:"seed_ratio"`
	SeedHours   int      `json:"seed_hours"`
	Enabled     *bool    `json:"enabled"`
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
	// Seeding with no ratio AND no time goal means "seed forever" — the torrent is never
	// removed and its data occupies the downloads dir permanently, the single biggest way
	// to fill a cache drive. Default a NEW indexer to a time-only goal; time-only (rather
	// than a ratio) can never remove a torrent early and breach a private tracker's
	// minimum-seed-time rule. Editing an indexer respects whatever is sent, so 0/0 stays
	// deliberately selectable.
	seedRatio, seedHours := req.SeedRatio, req.SeedHours
	if seedEnabled && seedRatio == 0 && seedHours == 0 {
		seedHours = indexer.DefaultSeedHours
	}

	created, err := a.deps.Indexers.Create(r.Context(), indexer.Indexer{
		Name:        req.Name,
		Kind:        kind,
		URL:         req.URL,
		APIKey:      req.APIKey,
		Username:    req.Username,
		Password:    req.Password,
		Categories:  req.Categories,
		MediaTypes:  req.MediaTypes,
		Priority:    req.Priority,
		MinSeeders:  req.MinSeeders,
		SeedEnabled: seedEnabled,
		SeedRatio:   seedRatio,
		SeedHours:   seedHours,
		Enabled:     enabled,
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
	old, err := a.deps.Indexers.Get(r.Context(), id)
	if errors.Is(err, indexer.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "indexer not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update indexer")
		return
	}
	// A row synced from Prowlarr takes its name, address and key from Prowlarr: an edit
	// here would only be undone by the next sync, and its address is how the sync finds it.
	// Everything else on it is the owner's to change.
	if old.ProwlarrID > 0 {
		req.Name, req.Kind, req.URL, req.APIKey = old.Name, string(old.Kind), old.URL, ""
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

	err = a.deps.Indexers.Update(r.Context(), indexer.Indexer{
		ID:          id,
		Name:        req.Name,
		Kind:        kind,
		URL:         req.URL,
		APIKey:      req.APIKey, // blank = keep existing
		Username:    req.Username,
		Password:    req.Password, // blank = keep existing
		Categories:  req.Categories,
		MediaTypes:  req.MediaTypes,
		Priority:    req.Priority,
		MinSeeders:  req.MinSeeders,
		SeedEnabled: seedEnabled,
		SeedRatio:   req.SeedRatio,
		SeedHours:   req.SeedHours,
		Enabled:     enabled,
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
	out := map[string]any{"ok": true}
	if idx, err := a.deps.Indexers.Get(r.Context(), id); err == nil {
		if s := idx.CapsSummary(); s != "" {
			out["caps_summary"] = s
		}
	}
	a.writeJSON(w, http.StatusOK, out)
}

// testIndexerRequest is a createIndexerRequest plus, optionally, the row being edited:
// a blank key or password then means "the one already saved", as it does on Save.
type testIndexerRequest struct {
	createIndexerRequest
	ID int64 `json:"id"`
}

// handleTestIndexerSettings tests settings that may not be saved yet (the Add form, or
// an edit before Save) without creating or changing a row. A key or password typed for
// the test is used for this one check and never stored or logged.
func (a *api) handleTestIndexerSettings(w http.ResponseWriter, r *http.Request) {
	var req testIndexerRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	idx := indexer.Indexer{
		Kind: indexer.Kind(req.Kind), Name: req.Name, URL: req.URL, APIKey: req.APIKey,
		Username: req.Username, Password: req.Password, Categories: req.Categories,
	}
	if req.ID > 0 {
		stored, err := a.deps.Indexers.Get(r.Context(), req.ID)
		if errors.Is(err, indexer.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "indexer not found")
			return
		}
		if err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not read indexer")
			return
		}
		// A secret saved for one kind means nothing to another, so it's only filled in
		// for the same kind.
		if stored.Kind == idx.Kind {
			if idx.APIKey == "" {
				idx.APIKey = stored.APIKey
				// A MyAnonaMouse session can be rotated by the site on any request; keeping
				// the id lets the rotated one be saved instead of lost.
				if idx.Kind == indexer.KindMAM {
					idx.ID = stored.ID
				}
			}
			if idx.Password == "" {
				idx.Password = stored.Password
			}
		}
		if idx.Name == "" {
			idx.Name = stored.Name
		}
	}
	switch idx.Kind {
	case indexer.KindTorznab, indexer.KindNewznab:
		if idx.URL == "" {
			a.writeError(w, http.StatusBadRequest, "url is required for torznab/newznab indexers")
			return
		}
	case indexer.KindTorrentLeech, indexer.KindMAM, indexer.KindX1337:
	default:
		a.writeError(w, http.StatusBadRequest, "unknown indexer kind")
		return
	}
	if idx.Name == "" {
		idx.Name = string(idx.Kind)
	}
	summary, err := a.deps.Indexers.TestSettings(r.Context(), idx)
	if err != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := map[string]any{"ok": true}
	if summary != "" {
		out["caps_summary"] = summary
	}
	a.writeJSON(w, http.StatusOK, out)
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
