package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/health"
)

// recheckTMDB re-runs the health panel's TMDB key check after the key changed or was
// tested, so a fixed key clears the warning now instead of at the next six-hourly check.
func (a *api) recheckTMDB() {
	if a.deps.Health == nil {
		return
	}
	a.bg("health check", "tmdb.key", 30*time.Second, func(ctx context.Context) error {
		a.deps.Health.RunNow(ctx, "tmdb.key")
		return nil
	})
}

// handleGetAPIKeys returns the state of every credential — configured or not, from where,
// and a short hint — but never a secret itself.
func (a *api) handleGetAPIKeys(w http.ResponseWriter, r *http.Request) {
	if a.deps.APIKeys == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"keys": []any{}})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"keys": a.deps.APIKeys.Status(r.Context())})
}

// handleTestAPIKey makes a real request with a saved key and reports the outcome. Only
// keys the catalogue marks testable have a check wired up.
func (a *api) handleTestAPIKey(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var detail string
	var err error
	switch r.PathValue("id") {
	case "hardcover":
		detail, err = a.deps.Books.VerifyHardcover(ctx)
	case "tmdb":
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
		a.recheckTMDB()
	default:
		a.writeError(w, http.StatusBadRequest, "no test is available for that key")
		return
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
		a.recheckTMDB()
	}
	// A Hardcover key makes Hardcover the books catalogue; bring the library across now.
	if id == "hardcover" && strings.TrimSpace(req.Value) != "" && a.deps.Books != nil {
		a.deps.Books.MaybeStartUpgrade(a.runCtx())
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
		a.recheckTMDB()
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"keys": a.deps.APIKeys.Status(r.Context())})
}
