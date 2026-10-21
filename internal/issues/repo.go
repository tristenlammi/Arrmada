package issues

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
)

// issueCols is every column an Issue is read from, in scanIssue's order.
const issueCols = `id, media_type, library_id, tmdb_id, title, year, poster_url, season, episode, category,
	note, reported_by, reported_by_name, status, resolution, action, action_note, action_file, action_size,
	action_job, action_at, created_at, updated_at, resolved_at, resolved_by, resolved_by_name, told_at`

type scanner interface{ Scan(dest ...any) error }

func scanIssue(r scanner) (Issue, error) {
	var i Issue
	err := r.Scan(&i.ID, &i.MediaType, &i.LibraryID, &i.TMDBID, &i.Title, &i.Year, &i.PosterURL, &i.Season, &i.Episode, &i.Category,
		&i.Note, &i.ReportedBy, &i.ReportedByName, &i.Status, &i.Resolution, &i.Action, &i.ActionNote, &i.ActionFile, &i.ActionSize,
		&i.ActionJob, &i.ActionAt, &i.CreatedAt, &i.UpdatedAt, &i.ResolvedAt, &i.ResolvedBy, &i.ResolvedByName, &i.toldAt)
	return i, err
}

func queryIssues(ctx context.Context, db store.Execer, q string, args ...any) ([]Issue, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func getIssue(ctx context.Context, db store.Execer, id int64) (Issue, error) {
	i, err := scanIssue(db.QueryRowContext(ctx, `SELECT `+issueCols+` FROM issues WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, ErrNotFound
	}
	return i, err
}

// liveIssue is the open or fixing issue for one problem, if there is one.
func liveIssue(ctx context.Context, db store.Execer, t Target, category string) (Issue, bool, error) {
	i, err := scanIssue(db.QueryRowContext(ctx, `SELECT `+issueCols+` FROM issues
		WHERE media_type = ? AND library_id = ? AND season = ? AND episode = ? AND category = ? AND status IN ('open', 'fixing')`,
		t.MediaType, t.LibraryID, t.Season, t.Episode, category))
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, false, nil
	}
	return i, err == nil, err
}

// addReporter records a reporter of an issue. A repeat by the same person keeps their
// first note unless they have written a new one.
func addReporter(ctx context.Context, db store.Execer, issueID, userID int64, name, note string, at int64) error {
	_, err := db.ExecContext(ctx, `INSERT INTO issue_reporters (issue_id, user_id, user_name, note, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (issue_id, user_id) DO UPDATE SET note = CASE WHEN excluded.note != '' THEN excluded.note ELSE issue_reporters.note END`,
		issueID, userID, name, note, at)
	return err
}

// reporters lists an issue's reporters, first first.
func reporters(ctx context.Context, db store.Execer, issueID int64) ([]Reporter, error) {
	rows, err := db.QueryContext(ctx, `SELECT user_id, user_name, note, created_at FROM issue_reporters WHERE issue_id = ? ORDER BY created_at, user_id`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reporter
	for rows.Next() {
		var r Reporter
		if err := rows.Scan(&r.UserID, &r.Name, &r.Note, &r.At); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// reportersFor reads the reporters of several issues at once, by issue id.
func reportersFor(ctx context.Context, db store.Execer, ids []int64) (map[int64][]Reporter, error) {
	out := map[int64][]Reporter{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `SELECT issue_id, user_id, user_name, note, created_at FROM issue_reporters
		WHERE issue_id IN (?`+strings.Repeat(",?", len(ids)-1)+`) ORDER BY created_at, user_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var r Reporter
		if err := rows.Scan(&id, &r.UserID, &r.Name, &r.Note, &r.At); err != nil {
			return nil, err
		}
		out[id] = append(out[id], r)
	}
	return out, rows.Err()
}

// listWhere builds a List query's WHERE clause and arguments.
func listWhere(f ListFilter) (string, []any) {
	var conds []string
	var args []any
	switch f.Status {
	case FilterOpen:
		conds = append(conds, `status IN ('open', 'fixing')`)
	case FilterClosed:
		conds = append(conds, `status IN ('resolved', 'dismissed')`)
	}
	if f.UserID > 0 {
		conds = append(conds, `id IN (SELECT issue_id FROM issue_reporters WHERE user_id = ?)`)
		args = append(args, f.UserID)
	}
	if f.ID > 0 {
		conds = append(conds, `id = ?`)
		args = append(args, f.ID)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// list reads one page of issues, newest first, and how many match in all.
func list(ctx context.Context, db store.Execer, f ListFilter) ([]Issue, int, error) {
	where, args := listWhere(f)
	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > maxPage {
		limit = maxPage
	}
	out, err := queryIssues(ctx, db, `SELECT `+issueCols+` FROM issues`+where+` ORDER BY created_at DESC, id DESC LIMIT ?`, append(args, limit)...)
	return out, total, err
}

// counts is how many issues are open (open or fixing) and closed, optionally for one
// reporter.
func counts(ctx context.Context, db store.Execer, userID int64) (Counts, error) {
	q := `SELECT COALESCE(SUM(status IN ('open', 'fixing')), 0), COALESCE(SUM(status IN ('resolved', 'dismissed')), 0) FROM issues`
	var args []any
	if userID > 0 {
		q += ` WHERE id IN (SELECT issue_id FROM issue_reporters WHERE user_id = ?)`
		args = append(args, userID)
	}
	var c Counts
	err := db.QueryRowContext(ctx, q, args...).Scan(&c.Open, &c.Closed)
	return c, err
}

// fixingFor lists the issues being fixed for one movie, or for any episode of one show.
func fixingFor(ctx context.Context, db store.Execer, media string, libraryID int64) ([]Issue, error) {
	return queryIssues(ctx, db, `SELECT `+issueCols+` FROM issues WHERE media_type = ? AND library_id = ? AND status = 'fixing'`, media, libraryID)
}

// close moves a live issue to resolved or dismissed. It reports false when the issue was
// no longer open or fixing (someone else closed it first), so the caller tells nobody.
func closeIssue(ctx context.Context, db store.Execer, id int64, status, resolution string, by int64, byName string, at int64) (bool, error) {
	res, err := db.ExecContext(ctx, `UPDATE issues SET status = ?, resolution = ?, resolved_at = ?, resolved_by = ?, resolved_by_name = ?, updated_at = ?
		WHERE id = ? AND status IN ('open', 'fixing')`, status, resolution, at, by, byName, at, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func markTold(ctx context.Context, db store.Execer, id, at int64) error {
	_, err := db.ExecContext(ctx, `UPDATE issues SET told_at = ? WHERE id = ?`, at, id)
	return err
}

// untold lists closed issues whose reporters haven't all been told yet, closed since
// `since` (unix seconds): the notice retry.
func untold(ctx context.Context, db store.Execer, since int64) ([]Issue, error) {
	return queryIssues(ctx, db, `SELECT `+issueCols+` FROM issues
		WHERE status IN ('resolved', 'dismissed') AND told_at = 0 AND resolved_at >= ? ORDER BY resolved_at LIMIT 200`, since)
}

func fixing(ctx context.Context, db store.Execer) ([]Issue, error) {
	return queryIssues(ctx, db, `SELECT `+issueCols+` FROM issues WHERE status = 'fixing' ORDER BY action_at LIMIT 500`)
}

func insertIssue(ctx context.Context, db store.Execer, i Issue) (int64, error) {
	id, err := store.InsertID(ctx, db, `INSERT INTO issues (media_type, library_id, tmdb_id, title, year, poster_url, season, episode, category,
		note, reported_by, reported_by_name, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)`,
		i.MediaType, i.LibraryID, i.TMDBID, i.Title, i.Year, i.PosterURL, i.Season, i.Episode, i.Category,
		i.Note, i.ReportedBy, i.ReportedByName, i.CreatedAt, i.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("record the issue: %w", err)
	}
	return id, nil
}
