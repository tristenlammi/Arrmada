package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// The jobs table stores times as unix milliseconds, 0 for not yet.

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) *time.Time {
	if v <= 0 {
		return nil
	}
	t := time.UnixMilli(v).UTC()
	return &t
}

func insertJob(ctx context.Context, db *sql.DB, s Spec, now time.Time) (int64, error) {
	res, err := db.ExecContext(ctx,
		`INSERT INTO jobs (kind, target, trigger, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		s.Kind, s.Target, s.Trigger, StatusQueued, ms(now))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func markRunning(ctx context.Context, db *sql.DB, id int64, at time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE jobs SET status = ?, started_at = ? WHERE id = ?`, StatusRunning, ms(at), id)
	return err
}

func setProgress(ctx context.Context, db *sql.DB, id int64, pct float64, msg string) error {
	// Never over a finished row: work that keeps reporting after its job ended (a goroutine
	// it left behind) mustn't rewrite the result.
	_, err := db.ExecContext(ctx, `UPDATE jobs SET progress = ?, message = ? WHERE id = ? AND finished_at = 0`, pct, msg, id)
	return err
}

func markFinished(ctx context.Context, db *sql.DB, id int64, status string, pct float64, msg, errText, result string, at time.Time) error {
	_, err := db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, progress = ?, message = ?, error = ?, result = ?, finished_at = ? WHERE id = ?`,
		status, pct, msg, errText, result, ms(at), id)
	return err
}

// markInterrupted closes out jobs a previous run of the app never finished.
func markInterrupted(ctx context.Context, db *sql.DB, now time.Time) (int64, error) {
	res, err := db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, error = 'the app stopped before this finished', finished_at = ?
		 WHERE status IN (?, ?)`, StatusInterrupted, ms(now), StatusQueued, StatusRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const jobCols = `id, kind, target, trigger, status, progress, message, error, result, created_at, started_at, finished_at`

func scanJob(sc interface{ Scan(...any) error }) (Job, error) {
	var (
		j                Job
		result           string
		created, started int64
		finished         int64
	)
	if err := sc.Scan(&j.ID, &j.Kind, &j.Target, &j.Trigger, &j.Status, &j.Progress, &j.Message, &j.Error, &result, &created, &started, &finished); err != nil {
		return Job{}, err
	}
	if result != "" && json.Valid([]byte(result)) {
		j.Result = json.RawMessage(result)
	}
	j.CreatedAt, j.StartedAt, j.FinishedAt = fromMS(created), fromMS(started), fromMS(finished)
	return j, nil
}

func getJob(ctx context.Context, db *sql.DB, id int64) (Job, error) {
	j, err := scanJob(db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

func listJobs(ctx context.Context, db *sql.DB, f Filter) ([]Job, error) {
	var where []string
	var args []any
	if f.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, f.Kind)
	}
	if f.Target != "" {
		where, args = append(where, "target = ?"), append(args, f.Target)
	}
	if f.Status != "" {
		where, args = append(where, "status = ?"), append(args, f.Status)
	}
	q := `SELECT ` + jobCols + ` FROM jobs`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func pruneJobs(ctx context.Context, db *sql.DB, before time.Time, keepMax int) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM jobs WHERE finished_at > 0 AND finished_at < ?`, ms(before))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if keepMax > 0 {
		res, err = db.ExecContext(ctx, `
			DELETE FROM jobs WHERE finished_at > 0 AND id NOT IN (
				SELECT id FROM jobs WHERE finished_at > 0 ORDER BY id DESC LIMIT ?)`, keepMax)
		if err != nil {
			return n, err
		}
		m, _ := res.RowsAffected()
		n += m
	}
	return n, nil
}
