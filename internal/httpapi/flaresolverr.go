package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/flaresolverr"
)

// handleFlareSolverrStatus says whether FlareSolverr is set up and answering, for the
// Indexers page, which used to claim it was wired up even when the container was dead.
// The answer is reused for up to a minute.
func (a *api) handleFlareSolverrStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	// A nil client answers "not set up".
	a.writeJSON(w, http.StatusOK, a.deps.FlareSolverr.Status(ctx))
}

// testFlareSolverr is the API-keys Test for the FlareSolverr URL: a ping, reporting its
// version or the exact failure.
func testFlareSolverr(ctx context.Context, fs *flaresolverr.Client) (string, error) {
	version, err := fs.Ping(ctx)
	if err != nil {
		return "", err
	}
	if version == "" {
		return "FlareSolverr is answering.", nil
	}
	return "FlareSolverr " + version + " is answering.", nil
}
