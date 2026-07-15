package httpapi

import (
	"net/http"

	"github.com/tristenlammi/arrmada/internal/library"
)

// handleHistory returns recently imported downloads.
func (a *api) handleHistory(w http.ResponseWriter, r *http.Request) {
	recs, err := a.deps.Library.Recent(r.Context(), 100)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	if recs == nil {
		recs = []library.ImportRecord{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"imports": recs})
}
