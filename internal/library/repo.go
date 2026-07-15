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
}

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
	var target string
	err := r.db.QueryRowContext(ctx, `SELECT target_path FROM imports WHERE download_hash = ?`, hash).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return target, true, err
}

// forgetByHash removes the import record for a download hash so it re-imports.
func (r *importRepo) forgetByHash(ctx context.Context, hash string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM imports WHERE download_hash = ?`, hash)
	return err
}

func (r *importRepo) record(ctx context.Context, rec ImportRecord) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO imports (download_hash, source_path, target_path, title, size_bytes)
		 VALUES (?, ?, ?, ?, ?)`,
		rec.Hash, rec.SourcePath, rec.TargetPath, rec.Title, rec.SizeBytes)
	return err
}

// forgetByTarget removes the import record for a target path so the same
// download can be imported again after its file was deleted.
func (r *importRepo) forgetByTarget(ctx context.Context, target string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM imports WHERE target_path = ?`, target)
	return err
}

func (r *importRepo) recent(ctx context.Context, limit int) ([]ImportRecord, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT download_hash, source_path, target_path, title, size_bytes, imported_at
		 FROM imports ORDER BY imported_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ImportRecord
	for rows.Next() {
		var r ImportRecord
		if err := rows.Scan(&r.Hash, &r.SourcePath, &r.TargetPath, &r.Title, &r.SizeBytes, &r.ImportedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
