package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/health"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/subtitles"
)

// recheckTMDB re-runs the health panel's TMDB key check after the key changed or was
// tested, so a fixed key clears the warning now instead of at the next six-hourly check.
func (a *api) recheckTMDB(r *http.Request) { a.recheckHealth(r, "tmdb.key") }

// handleGetAPIKeys returns the state of every credential — configured or not, from where,
// and a short hint — but never a secret itself.
func (a *api) handleGetAPIKeys(w http.ResponseWriter, r *http.Request) {
	if a.deps.APIKeys == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"keys": []any{}})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"keys": a.deps.APIKeys.Status(r.Context())})
}

// handleTestAPIKey makes a real request with a key and reports the outcome. With no body
// it tests the saved key; with {"value": "..."} it tests that candidate instead — a key
// typed and not yet saved — which is sent to the provider once and never stored or
// logged. Only keys with a verifier wired up can be tested; anything else is a 400.
func (a *api) handleTestAPIKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Value string `json:"value"`
	}
	// The body is optional: the Test button on a saved key sends none.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		a.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	candidate := strings.TrimSpace(body.Value)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var detail string
	var err error
	switch verify, ok := a.deps.KeyVerifiers[id]; {
	case id == "hardcover":
		if candidate != "" {
			a.writeError(w, http.StatusBadRequest, "Hardcover can only test the saved key; save it, then Test")
			return
		}
		if a.deps.Books == nil {
			a.writeError(w, http.StatusBadRequest, "no test is available for that key")
			return
		}
		detail, err = a.deps.Books.VerifyHardcover(ctx)
	case id == "flaresolverr":
		if candidate != "" {
			a.writeError(w, http.StatusBadRequest, "FlareSolverr can only test the saved URL; save it, then Test")
			return
		}
		detail, err = testFlareSolverr(ctx, a.deps.FlareSolverr)
	case ok:
		detail, err = verify(ctx, candidate)
		if errors.Is(err, metadata.ErrNotConfigured) || errors.Is(err, subtitles.ErrNotConfigured) {
			err = errors.New("no key is set to test")
		}
	case id == "tmdb" && candidate == "":
		// No verifier wired: fall back to the health check's own TMDB validation.
		v, ok := a.deps.Discovery.(tmdbValidator)
		if !ok {
			a.writeError(w, http.StatusBadRequest, "no test is available for that key")
			return
		}
		switch err = tmdbValidate(ctx, v); {
		case err == nil:
			detail = "TMDB accepted the key."
		case errors.Is(err, health.ErrKeyRejected):
			err = errors.New("TMDB rejected the key — check it's the v3 API key, copied in full")
		case errors.Is(err, health.ErrKeyMissing):
			err = errors.New("no TMDB key is set")
		}
	default:
		a.writeError(w, http.StatusBadRequest, "no test is available for that key")
		return
	}
	// A test of the saved TMDB key settles the health panel's TMDB warning either way.
	// A candidate says nothing about the key in use, so it leaves the panel alone.
	if id == "tmdb" && candidate == "" {
		a.recheckTMDB(r)
	}
	if err != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": err.Error()})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": detail})
}

// handleSetAPIKey saves one credential. A blank value is refused: clearing a key is its own
// confirmed DELETE, so an empty Save can never wipe a working key by accident.
func (a *api) handleSetAPIKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		a.writeError(w, http.StatusBadRequest, "missing key id")
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if a.deps.APIKeys == nil {
		a.writeError(w, http.StatusInternalServerError, "key store unavailable")
		return
	}
	switch err := a.deps.APIKeys.Set(r.Context(), id, req.Value); {
	case errors.Is(err, apikeys.ErrEmptyValue):
		a.writeError(w, http.StatusBadRequest, "value is required — use Clear to remove a key")
		return
	case errors.Is(err, apikeys.ErrUnknownKey):
		a.writeError(w, http.StatusNotFound, "unknown key")
		return
	case err != nil:
		a.writeError(w, http.StatusInternalServerError, "could not save the key")
		return
	}
	if id == "tmdb" {
		a.recheckTMDB(r)
	}
	// A Hardcover key makes Hardcover the books catalogue; bring the library across now.
	if id == "hardcover" && strings.TrimSpace(req.Value) != "" && a.deps.Books != nil {
		a.deps.Books.MaybeSubmitUpgrade(a.runCtx(), a.jobSubmitter(), triggerFor(r))
	}
	// Return the fresh status so the UI reflects the new state (masked) without a reload.
	a.writeJSON(w, http.StatusOK, map[string]any{"keys": a.deps.APIKeys.Status(r.Context())})
}

// handleClearAPIKey removes a saved credential, so it falls back to the install-time env
// var if there is one. Clearing Hardcover leaves books on the ids they already have; new
// lookups go back to Open Library.
func (a *api) handleClearAPIKey(w http.ResponseWriter, r *http.Request) {
	if a.deps.APIKeys == nil {
		a.writeError(w, http.StatusInternalServerError, "key store unavailable")
		return
	}
	switch err := a.deps.APIKeys.Clear(r.Context(), r.PathValue("id")); {
	case errors.Is(err, apikeys.ErrUnknownKey):
		a.writeError(w, http.StatusNotFound, "unknown key")
		return
	case err != nil:
		a.writeError(w, http.StatusInternalServerError, "could not clear the key")
		return
	}
	if r.PathValue("id") == "tmdb" {
		a.recheckTMDB(r)
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"keys": a.deps.APIKeys.Status(r.Context())})
}
