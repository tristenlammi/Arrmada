package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// handleBackupNow takes a manual database backup ("Back up now") and returns it. It runs
// synchronously, detached from the request so a closed tab can't cut a copy short; the
// copy is checked before it gets its name, so a half-written file never looks like a
// backup. The answer names the file and its size — never anything inside it.
func (a *api) handleBackupNow(w http.ResponseWriter, r *http.Request) {
	if a.deps.Backups == nil {
		a.writeError(w, http.StatusServiceUnavailable, "database backups aren't available")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	b, err := a.deps.Backups.Create(ctx, store.BackupManual)
	if err != nil {
		a.deps.Log.Error("manual database backup failed", "err", err)
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNoSpace) {
			status = http.StatusInsufficientStorage
		}
		a.writeError(w, status, "couldn't back up the database: "+err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, b)
}
