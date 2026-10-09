package library

import (
	"context"
	"database/sql"
	"errors"
)

// ImportRecord is a row in the import history.
type ImportRecord struct {
	Hash       string `json:"hash"`
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
	Title      string `json:"title"`
	SizeBytes  int64  `json:"size_bytes"`
	ImportedAt string `json:"imported_at"`
	// What attaching the import to its movie needs: the release it came from and the
	// year its title was matched with.
	ReleaseName string `json:"release_name,omitempty"`
	Year        int    `json:"year,omitempty"`
	// Where attaching it to its movie stands (see the Attach* states) — read-only here,
	// so History can show an import that needs attention.
	AttachState    string `json:"attach_state"`
	AttachError    string `json:"attach_error,omitempty"`
	AttachAttempts int    `json:"attach_attempts,omitempty"`
}

// Attach states stored in imports.attach_state.
const (
	AttachStatePending   = "pending"   // recorded, not attached yet; retried with backoff
	AttachStateAttached  = "attached"  // the movie has it (also every row from before attach states)
	AttachStateUnmatched = "unmatched" // no movie in the library fits; not retried
	AttachStateRefused   = "refused"   // the quality gate kept the better file; not retried
	AttachStateGone      = "gone"      // the imported file vanished before it was attached
)

type importRepo struct{ db *sql.DB }

func (r *importRepo) exists(ctx context.Context, hash string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM imports WHERE download_hash = ?`, hash).Scan(&n)
	return n > 0, err
}

// importedHashes returns the set of download hashes already imported into the
// library — used to drop finished-and-imported torrents from the downloads view.
func (r *importRepo) importedHashes(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT download_hash FROM imports WHERE download_hash != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil {
			out[h] = true
		}
	}
	return out, rows.Err()
}

// targetFor returns the recorded target path for a download hash, and whether a
// record exists. Used to verify a prior import's file is still on disk.
func (r *importRepo) targetFor(ctx context.Context, hash string) (string, bool, error) {
	target, done, _, err := r.importState(ctx, hash)
	return target, done, err
}

// importState is targetFor plus whether the user deleted the imported file on purpose.
func (r *importRepo) importState(ctx context.Context, hash string) (target string, done, removed bool, err error) {
	var rem int
	err = r.db.QueryRowContext(ctx, `SELECT target_path, removed FROM imports WHERE download_hash = ?`, hash).Scan(&target, &rem)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	return target, err == nil, rem != 0, err
}

// forgetByHash removes the import record for a download hash so it re-imports.
func (r *importRepo) forgetByHash(ctx context.Context, hash string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM imports WHERE download_hash = ?`, hash)
	return err
}

// record writes an import row. state is its attach state: 'pending' when it still has to
// be attached to its movie, 'attached' when nothing is going to attach it.
func (r *importRepo) record(ctx context.Context, rec ImportRecord, state string) error {
	if state == "" {
		state = AttachStateAttached
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO imports (download_hash, source_path, target_path, title, size_bytes, release_name, year, attach_state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.Hash, rec.SourcePath, rec.TargetPath, rec.Title, rec.SizeBytes, rec.ReleaseName, rec.Year, state)
	return err
}

// setAttachState settles an import's attach state ('attached' or one of the final
// failures) and stores why, when there's a reason.
func (r *importRepo) setAttachState(ctx context.Context, hash, state, reason string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE imports SET attach_state = ?, attach_error = ?, attach_next_at = 0 WHERE download_hash = ?`,
		state, reason, hash)
	return err
}

// setAttachRetry records a failed attach attempt and when to try again; the row stays
// 'pending'.
func (r *importRepo) setAttachRetry(ctx context.Context, hash string, attempts int, reason string, next int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE imports SET attach_state = 'pending', attach_attempts = ?, attach_error = ?, attach_next_at = ?
		 WHERE download_hash = ?`,
		attempts, reason, next, hash)
	return err
}

// pendingAttach lists imports still waiting to be attached whose retry is due, oldest
// first. A file the user deleted on purpose is left alone: there's nothing to attach.
func (r *importRepo) pendingAttach(ctx context.Context, now int64, limit int) ([]ImportRecord, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT download_hash, source_path, target_path, title, size_bytes, imported_at,
		        release_name, year, attach_state, attach_error, attach_attempts
		 FROM imports
		 WHERE attach_state = 'pending' AND removed = 0 AND attach_next_at <= ?
		 ORDER BY id LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportRecord
	for rows.Next() {
		var rec ImportRecord
		if err := rows.Scan(&rec.Hash, &rec.SourcePath, &rec.TargetPath, &rec.Title, &rec.SizeBytes, &rec.ImportedAt,
			&rec.ReleaseName, &rec.Year, &rec.AttachState, &rec.AttachError, &rec.AttachAttempts); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// attachInfo reads a row's attach bookkeeping (for the retry count after a fresh record).
func (r *importRepo) attachInfo(ctx context.Context, hash string) (state string, attempts int, lastErr string, err error) {
	err = r.db.QueryRowContext(ctx,
		`SELECT attach_state, attach_attempts, attach_error FROM imports WHERE download_hash = ?`, hash).
		Scan(&state, &attempts, &lastErr)
	return state, attempts, lastErr, err
}

// markRemovedByTarget flags the import behind a deliberately deleted library file.
// The record stays, so the still-seeding torrent isn't imported straight back; a new
// grab of the same release clears the flag (see ClearRemovedImport).
func (r *importRepo) markRemovedByTarget(ctx context.Context, target string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE imports SET removed = 1 WHERE target_path = ?`, target)
	return err
}

func (r *importRepo) recent(ctx context.Context, limit int) ([]ImportRecord, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT download_hash, source_path, target_path, title, size_bytes, imported_at,
		        release_name, year, attach_state, attach_error, attach_attempts
		 FROM imports ORDER BY imported_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ImportRecord
	for rows.Next() {
		var r ImportRecord
		if err := rows.Scan(&r.Hash, &r.SourcePath, &r.TargetPath, &r.Title, &r.SizeBytes, &r.ImportedAt,
			&r.ReleaseName, &r.Year, &r.AttachState, &r.AttachError, &r.AttachAttempts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
