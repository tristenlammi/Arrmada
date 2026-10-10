package insights

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Tautulli imports as tracked runs (insights_import_runs, migration 0176): progress while
// one runs, a summary after, Retry from the saved connection, and undo of exactly the plays
// one run brought in.

const (
	keyTautulliURL = "tautulli_url"
	keyTautulliKey = "tautulli_api_key"
)

// Import run statuses.
const (
	RunRunning     = "running"
	RunDone        = "done"
	RunFailed      = "failed"
	RunTimeout     = "timeout"
	RunInterrupted = "interrupted"
)

// TautulliConfig is the saved Tautulli connection as the UI sees it: the key itself is
// never sent back, only whether one is saved.
type TautulliConfig struct {
	URL       string `json:"url"`
	APIKeySet bool   `json:"api_key_set"`
}

func (s *Service) TautulliConfig(ctx context.Context) TautulliConfig {
	return TautulliConfig{
		URL:       s.settings.Get(ctx, keyTautulliURL, ""),
		APIKeySet: s.settings.Get(ctx, keyTautulliKey, "") != "",
	}
}

// TautulliCredentials is the saved connection, key included — for the server's own use.
func (s *Service) TautulliCredentials(ctx context.Context) (url, key string) {
	return s.settings.Get(ctx, keyTautulliURL, ""), s.settings.Get(ctx, keyTautulliKey, "")
}

// SaveTautulli stores the connection an import used, so Retry can run it again. An empty
// key keeps the saved one.
func (s *Service) SaveTautulli(ctx context.Context, url, key string) error {
	if err := s.settings.Set(ctx, keyTautulliURL, strings.TrimSpace(url)); err != nil {
		return err
	}
	if key = strings.TrimSpace(key); key != "" {
		return s.settings.Set(ctx, keyTautulliKey, key)
	}
	return nil
}

// ImportRun is one import's record.
type ImportRun struct {
	ID          int64  `json:"id"`
	JobID       int64  `json:"job_id"`
	Source      string `json:"source"`
	StartedAt   int64  `json:"started_at"`
	FinishedAt  int64  `json:"finished_at"`
	Status      string `json:"status"`
	Total       int    `json:"total"`
	Processed   int    `json:"processed"`
	Imported    int    `json:"imported"`
	Duplicates  int    `json:"duplicates"`
	Overlaps    int    `json:"overlaps"`
	Invalid     int    `json:"invalid"`
	AfterCutoff int    `json:"after_cutoff"`
	Failed      int    `json:"failed"`
	Error       string `json:"error"`
	CutoffAt    int64  `json:"cutoff_at"`
	// Rows is how many of the run's plays are in the history now (what undo would remove).
	Rows        int   `json:"rows"`
	RemovedAt   int64 `json:"removed_at"`
	RemovedRows int   `json:"removed_rows"`
}

var (
	ErrRunNotFound    = errors.New("import run not found")
	ErrRunRunning     = errors.New("that import is still running — wait for it to finish")
	ErrRunRowsChanged = errors.New("the number of plays in this import changed since you checked — check again")
)

// StartImportRun records a run as running and returns its id.
func (s *Service) StartImportRun(ctx context.Context, jobID, cutoff int64) (int64, error) {
	res, err := s.repo.db.ExecContext(ctx,
		`INSERT INTO insights_import_runs (job_id, source, started_at, status, cutoff_at) VALUES (?, 'tautulli', ?, ?, ?)`,
		jobID, time.Now().Unix(), RunRunning, cutoff)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateImportRun records progress: Tautulli's total and the counts so far.
func (s *Service) UpdateImportRun(ctx context.Context, id int64, total int, c ImportCounts) error {
	_, err := s.repo.db.ExecContext(ctx, `UPDATE insights_import_runs SET total = ?, processed = ?, imported = ?,
		duplicates = ?, overlaps = ?, invalid = ?, after_cutoff = ?, failed = ? WHERE id = ? AND status = ?`,
		total, c.Processed(), c.Imported, c.Duplicate, c.Overlap, c.Invalid, c.AfterCutoff, c.Failed, id, RunRunning)
	return err
}

// FinishImportRun closes a run with its final counts and status. errText is why it stopped
// early; a run that finished with plays that couldn't be saved keeps the first such error.
func (s *Service) FinishImportRun(ctx context.Context, id int64, status string, total int, c ImportCounts, errText string) error {
	if errText == "" && c.Failed > 0 {
		errText = fmt.Sprintf("%d play(s) couldn't be saved: %s", c.Failed, c.FirstError)
	}
	if status == RunDone && c.Processed() > total {
		total = c.Processed() // Tautulli recorded more plays while the import ran
	}
	_, err := s.repo.db.ExecContext(ctx, `UPDATE insights_import_runs SET status = ?, finished_at = ?, total = ?,
		processed = ?, imported = ?, duplicates = ?, overlaps = ?, invalid = ?, after_cutoff = ?, failed = ?, error = ?
		WHERE id = ?`,
		status, time.Now().Unix(), total, c.Processed(), c.Imported, c.Duplicate, c.Overlap, c.Invalid, c.AfterCutoff,
		c.Failed, errText, id)
	return err
}

// MarkInterruptedImports closes runs a previous process left running: the app stopped
// mid-import, and those runs will never finish. Call once at startup, before any import.
func (s *Service) MarkInterruptedImports(ctx context.Context) (int64, error) {
	res, err := s.repo.db.ExecContext(ctx, `UPDATE insights_import_runs SET status = ?, finished_at = ?,
		error = 'Arrmada stopped before this import finished' WHERE status = ?`, RunInterrupted, time.Now().Unix(), RunRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const importRunCols = `r.id, r.job_id, r.source, r.started_at, r.finished_at, r.status, r.total, r.processed, r.imported,
	r.duplicates, r.overlaps, r.invalid, r.after_cutoff, r.failed, r.error, r.cutoff_at, r.removed_at, r.removed_rows,
	(SELECT COUNT(*) FROM stream_sessions s WHERE s.import_run_id = r.id)`

func scanImportRun(sc interface{ Scan(...any) error }) (ImportRun, error) {
	var r ImportRun
	err := sc.Scan(&r.ID, &r.JobID, &r.Source, &r.StartedAt, &r.FinishedAt, &r.Status, &r.Total, &r.Processed,
		&r.Imported, &r.Duplicates, &r.Overlaps, &r.Invalid, &r.AfterCutoff, &r.Failed, &r.Error, &r.CutoffAt,
		&r.RemovedAt, &r.RemovedRows, &r.Rows)
	return r, err
}

// ImportRuns lists the latest runs, newest first.
func (s *Service) ImportRuns(ctx context.Context, limit int) ([]ImportRun, error) {
	if limit <= 0 || limit > 50 {
		limit = 5
	}
	rows, err := s.repo.db.QueryContext(ctx, `SELECT `+importRunCols+` FROM insights_import_runs r ORDER BY r.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ImportRun{}
	for rows.Next() {
		r, err := scanImportRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ImportRun reads one run.
func (s *Service) ImportRun(ctx context.Context, id int64) (ImportRun, error) {
	r, err := scanImportRun(s.repo.db.QueryRowContext(ctx, `SELECT `+importRunCols+` FROM insights_import_runs r WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ImportRun{}, ErrRunNotFound
	}
	return r, err
}

// RemoveImportRun deletes exactly the plays one run imported, with their buffer events, in
// one transaction. Live-recorded plays carry no run id and plays from other runs carry
// theirs, so nothing else can match. expected is the count the owner confirmed: if it no
// longer matches, nothing is deleted.
func (s *Service) RemoveImportRun(ctx context.Context, id int64, expected int) (int64, error) {
	var removed int64
	err := store.WithTx(ctx, s.repo.db, func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM insights_import_runs WHERE id = ?`, id).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrRunNotFound
			}
			return err
		}
		if status == RunRunning {
			return ErrRunRunning
		}
		const mine = `SELECT id FROM stream_sessions WHERE import_run_id = ? AND session_key = ''`
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+mine+`)`, id).Scan(&n); err != nil {
			return err
		}
		if n != expected {
			return ErrRunRowsChanged
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM buffer_events WHERE session_id IN (`+mine+`)`, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM stream_sessions WHERE import_run_id = ? AND session_key = ''`, id)
		if err != nil {
			return err
		}
		if removed, err = res.RowsAffected(); err != nil {
			return err
		}
		if removed != int64(n) {
			return fmt.Errorf("undo would remove %d plays but counted %d — nothing was changed", removed, n)
		}
		_, err = tx.ExecContext(ctx, `UPDATE insights_import_runs SET removed_at = ?, removed_rows = removed_rows + ? WHERE id = ?`,
			time.Now().Unix(), removed, id)
		return err
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}
