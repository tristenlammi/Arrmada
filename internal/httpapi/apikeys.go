package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"
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
	// A Hardcover key makes Hardcover the books catalogue; bring the library across now.
	if id == "hardcover" && strings.TrimSpace(req.Value) != "" && a.deps.Books != nil {
		a.deps.Books.MaybeStartUpgrade(a.runCtx())
	}
	// Return the fresh status so the UI reflects the new state (masked) without a reload.
	a.writeJSON(w, http.StatusOK, map[string]any{"keys": a.deps.APIKeys.Status(r.Context())})
}
