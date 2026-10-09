package httpapi

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/backup"
	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Every /system/backups route is admin only: a backup holds API keys, the Plex token,
// password hashes and everyone's listening places. Nothing here ever reads or returns
// what is inside a backup beyond its schema version.

// backupsReady answers 503 when no backup service is wired (tests, or a store that
// couldn't be opened for backups) and reports whether the caller may go on.
func (a *api) backupsReady(w http.ResponseWriter) bool {
	if a.deps.Backups == nil {
		a.writeError(w, http.StatusServiceUnavailable, "database backups aren't available")
		return false
	}
	return true
}

// handleBackupsList — GET /api/v1/system/backups. The Backups card: every backup with
// its kind, size and schema version, their total, the free space beside them and the
// nightly schedule.
func (a *api) handleBackupsList(w http.ResponseWriter, r *http.Request) {
	if !a.backupsReady(w) {
		return
	}
	svc := a.deps.Backups
	list, err := svc.List(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "couldn't read the backups folder: "+err.Error())
		return
	}
	var total int64
	for _, b := range list {
		total += b.SizeBytes
	}
	out := map[string]any{
		"backups":         list,
		"total_bytes":     total,
		"free_bytes":      nil, // unknown where free space can't be measured
		"dir":             svc.Dir(),
		"settings":        svc.Schedule(r.Context()),
		"last_nightly_at": nil,
	}
	if u, ok := diskspace.Of(svc.Dir()); ok {
		out["free_bytes"] = u.FreeBytes
	} else if u, ok := diskspace.Of(a.deps.Config.DataDir); ok {
		out["free_bytes"] = u.FreeBytes // no backups folder yet: the data dir's disk is the same one
	}
	if t := svc.LastNightly(); !t.IsZero() {
		out["last_nightly_at"] = t
	}
	a.writeJSON(w, http.StatusOK, out)
}

// handleBackupNow takes a manual database backup ("Back up now") and returns it. It runs
// synchronously, detached from the request so a closed tab can't cut a copy short; the
// copy is checked before it gets its name, so a half-written file never looks like a
// backup. The answer names the file and its size — never anything inside it.
func (a *api) handleBackupNow(w http.ResponseWriter, r *http.Request) {
	if !a.backupsReady(w) {
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

// handleBackupSettings — PUT /api/v1/system/backups/settings {enabled?, hour?, keep_nightly?}.
// Saved straight to the settings store rather than through the general Settings save, so
// the card works on its own. The hourly check picks the change up on its next run; a
// smaller keep_nightly prunes at the next nightly, not now.
func (a *api) handleBackupSettings(w http.ResponseWriter, r *http.Request) {
	if !a.backupsReady(w) {
		return
	}
	var req struct {
		Enabled     *bool `json:"enabled"`
		Hour        *int  `json:"hour"`
		KeepNightly *int  `json:"keep_nightly"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Hour != nil && (*req.Hour < 0 || *req.Hour > backup.MaxHour) {
		a.writeError(w, http.StatusBadRequest, "hour must be 0-23")
		return
	}
	if req.KeepNightly != nil && (*req.KeepNightly < 1 || *req.KeepNightly > backup.MaxKeepNightly) {
		a.writeError(w, http.StatusBadRequest, "keep must be 1-"+strconv.Itoa(backup.MaxKeepNightly))
		return
	}
	ctx := r.Context()
	set := func(key, v string) bool {
		if err := a.deps.Settings.Set(ctx, key, v); err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not save")
			return false
		}
		return true
	}
	if req.Enabled != nil && !set(backup.KeyEnabled, strconv.FormatBool(*req.Enabled)) {
		return
	}
	if req.Hour != nil && !set(backup.KeyHour, strconv.Itoa(*req.Hour)) {
		return
	}
	if req.KeepNightly != nil && !set(backup.KeyKeepNightly, strconv.Itoa(*req.KeepNightly)) {
		return
	}
	a.writeJSON(w, http.StatusOK, a.deps.Backups.Schedule(ctx))
}

// handleBackupDownload — GET /api/v1/system/backups/{name}/download. Streams the backup
// gzipped on the fly (a database compresses well, and nothing extra lands on disk). The
// response is never cached: it holds every secret the app has.
func (a *api) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	if !a.backupsReady(w) {
		return
	}
	name := r.PathValue("name")
	f, b, err := a.deps.Backups.Open(name)
	if !a.backupNameErr(w, err) {
		return
	}
	defer f.Close()

	h := w.Header()
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Disposition", `attachment; filename="`+b.Name+`.gz"`)
	h.Set("Cache-Control", "no-store, private")
	h.Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)

	// BestSpeed: most of the saving for a fraction of the CPU of the default level.
	gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	gz.Name = b.Name
	gz.ModTime = b.CreatedAt
	start := time.Now()
	_, err = io.Copy(gz, f)
	if err == nil {
		err = gz.Close()
	}
	if err != nil {
		// Headers are gone already; the browser sees a cut-off download.
		a.deps.Log.Warn("backup download didn't finish", "name", b.Name, "err", err)
		return
	}
	a.deps.Log.Info("database backup downloaded", "name", b.Name, "size_bytes", b.SizeBytes, "duration", time.Since(start).Round(time.Millisecond))
}

// handleBackupDelete — DELETE /api/v1/system/backups/{name}.
func (a *api) handleBackupDelete(w http.ResponseWriter, r *http.Request) {
	if !a.backupsReady(w) {
		return
	}
	name := r.PathValue("name")
	if !a.backupNameErr(w, a.deps.Backups.Delete(name)) {
		return
	}
	a.deps.Log.Info("database backup deleted", "name", name)
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// backupNameErr answers for a backup lookup's error — 400 for a name that isn't a backup
// (a path, "..", another file), 404 for one that isn't there — and reports whether the
// caller may go on.
func (a *api) backupNameErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, backup.ErrBadName):
		a.writeError(w, http.StatusBadRequest, "not a backup name")
	case errors.Is(err, os.ErrNotExist):
		a.writeError(w, http.StatusNotFound, "no such backup")
	default:
		a.writeError(w, http.StatusInternalServerError, err.Error())
	}
	return false
}
