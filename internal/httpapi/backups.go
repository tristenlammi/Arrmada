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
	out["can_restart"] = a.canRestart()
	out["pending_restore"] = nil
	if m, err := svc.PendingRestore(); err == nil && m != nil {
		out["pending_restore"] = map[string]any{"name": m.Name(), "requested_by": m.RequestedBy, "at": m.At}
	}
	out["last_restore"] = nil
	if res, err := svc.LastRestore(); err == nil && res != nil {
		out["last_restore"] = res
	}
	a.writeJSON(w, http.StatusOK, out)
}

// canRestart says whether the app can restart itself (inside Docker, whose restart
// policy brings it back); otherwise the owner restarts it by hand.
func (a *api) canRestart() bool { return a.deps.Restart != nil && inContainer() }

// restoreConfirmPhrase must be typed to restore: a restore replaces every module's state,
// audiobook places included.
const restoreConfirmPhrase = "RESTORE"

// manualRestartCommand is what the card shows when the app can't restart itself.
const manualRestartCommand = "docker restart Arrmada-app"

// handleBackupRestore — POST /api/v1/system/backups/{name}/restore {confirm:"RESTORE"}.
// Validates the backup and stages it; the swap itself happens at the next start, never
// while the app has the database open. When the app can restart itself it does so right
// after answering.
func (a *api) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	if !a.backupsReady(w) {
		return
	}
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Confirm != restoreConfirmPhrase {
		a.writeError(w, http.StatusBadRequest, `type RESTORE to confirm`)
		return
	}
	by := ""
	if u, ok := userFrom(r); ok && u != nil {
		by = u.Username
	}
	name := r.PathValue("name")
	info, err := a.deps.Backups.StageRestore(name, by)
	if err != nil {
		if errors.Is(err, backup.ErrBadName) || errors.Is(err, os.ErrNotExist) {
			a.backupNameErr(w, err)
			return
		}
		a.deps.Log.Warn("restore refused", "backup", name, "err", err)
		a.writeError(w, http.StatusBadRequest, "can't restore this backup: "+err.Error())
		return
	}
	restarting := a.canRestart()
	a.writeJSON(w, http.StatusOK, map[string]any{
		"staged":         true,
		"restarting":     restarting,
		"manual_command": manualRestartCommand,
		"schema_version": info.SchemaVersion,
	})
	if !restarting {
		return
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	a.deps.Log.Info("restarting to restore the database", "backup", name)
	a.deps.Restart()
}

// handleBackupRestoreCancel — DELETE /api/v1/system/backups/restore-pending. Drops a
// staged restore that hasn't run yet (one waiting for a manual restart).
func (a *api) handleBackupRestoreCancel(w http.ResponseWriter, r *http.Request) {
	if !a.backupsReady(w) {
		return
	}
	ok, err := a.deps.Backups.CancelRestore()
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "couldn't cancel the restore: "+err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"cancelled": ok})
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
	case errors.Is(err, backup.ErrStaged):
		a.writeError(w, http.StatusConflict, err.Error())
	default:
		a.writeError(w, http.StatusInternalServerError, err.Error())
	}
	return false
}
