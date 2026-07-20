package httpapi

import (
	"net/http"
	"strings"
)

// handleGetAPIKeys returns the state of every credential — configured or not, from where,
// and a short hint — but never a secret itself.
func (a *api) handleGetAPIKeys(w http.ResponseWriter, r *http.Request) {
	if a.deps.APIKeys == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"keys": []any{}})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"keys": a.deps.APIKeys.Status(r.Context())})
}

// handleSetAPIKey saves (or, with an empty value, clears) one credential. An empty value
// falls back to the env var if one was supplied at install.
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
	if err := a.deps.APIKeys.Set(r.Context(), id, strings.TrimSpace(req.Value)); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save the key")
		return
	}
	// Return the fresh status so the UI reflects the new state (masked) without a reload.
	a.writeJSON(w, http.StatusOK, map[string]any{"keys": a.deps.APIKeys.Status(r.Context())})
}
