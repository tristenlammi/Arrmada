package httpapi

import (
	"net/http"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// handleParse parses a release name into structured attributes. A debugging/
// authoring aid for the acquisition pipeline: GET /api/v1/parse?name=<release>.
func (a *api) handleParse(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		a.writeError(w, http.StatusBadRequest, "missing ?name= query parameter")
		return
	}
	a.writeJSON(w, http.StatusOK, parser.Parse(name))
}
